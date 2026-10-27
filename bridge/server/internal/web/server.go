// Package web serves the link page, where Navidrome users connect and
// disconnect their Discord account.
package web

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"navidrome-discord-status/bridge/internal/discord"
	"navidrome-discord-status/bridge/internal/navidrome"
	"navidrome-discord-status/bridge/internal/ratelimit"
	"navidrome-discord-status/bridge/internal/store"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static/style.css
var styleSheet []byte

var (
	pageTemplate    = parsePage("page.html")
	privacyTemplate = parsePage("privacy.html")
)

// page is a template that fills the "content" block of base.html.
type page struct {
	name     string
	template *template.Template
}

// parsePage parses base.html first, so the page can override its blocks.
func parsePage(name string) page {
	t := template.Must(template.ParseFS(templateFiles, "templates/base.html", "templates/"+name))
	return page{name: name, template: t}
}

func (p page) render(w http.ResponseWriter, data any) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return p.template.ExecuteTemplate(w, p.name, data)
}

const (
	maxFormSize = 4 << 10

	loginAttemptsPerIP   = 10
	loginAttemptsPerUser = 5
	loginWindow          = 15 * time.Minute

	contentSecurityPolicy = "default-src 'none'; style-src 'self'; img-src 'self'; " +
		"form-action 'self' https://discord.com; frame-ancestors 'none'; base-uri 'none'"
)

// Credentials verifies Navidrome logins.
type Credentials interface {
	Verify(ctx context.Context, username, password string) (string, error)
}

type OAuth interface {
	AuthorizeURL(state, codeChallenge string) string
	Exchange(ctx context.Context, code, codeVerifier string) (discord.Token, error)
	UserInfo(ctx context.Context, accessToken string) (discord.User, error)
	Revoke(ctx context.Context, refreshToken string) error
}

type Links interface {
	Get(username string) (store.Link, error)
	Put(username string, link store.Link) error
	Delete(username string) error
}

// Sessions is notified when a user unlinks, so their status is cleared.
type Sessions interface {
	Forget(username string)
}

// Options configures the link page.
type Options struct {
	SecureCookies  bool
	TrustedProxies []net.IP
	Privacy        PrivacyInfo
}

// PrivacyInfo fills in the operator-specific parts of the privacy notice.
type PrivacyInfo struct {
	Contact           string
	LinkRetentionDays int // 0: kept until the user unlinks
	LogRetentionDays  int
}

type Server struct {
	credentials Credentials
	oauth       OAuth
	links       Links
	presence    Sessions
	opts        Options
	sessions    *sessionStore
	ipLimiter   *ratelimit.Limiter
	userLimiter *ratelimit.Limiter
	log         *slog.Logger
	mux         *http.ServeMux
}

func NewServer(credentials Credentials, oauth OAuth, links Links, presence Sessions, opts Options, log *slog.Logger) *Server {
	s := &Server{
		credentials: credentials,
		oauth:       oauth,
		links:       links,
		presence:    presence,
		opts:        opts,
		sessions:    newSessionStore(opts.SecureCookies),
		ipLimiter:   ratelimit.New(loginAttemptsPerIP, loginWindow),
		userLimiter: ratelimit.New(loginAttemptsPerUser, loginWindow),
		log:         log,
		mux:         http.NewServeMux(),
	}
	s.mux.HandleFunc("GET /{$}", s.withSession(s.handleIndex))
	s.mux.HandleFunc("POST /login", s.withSession(s.requireCSRF(s.handleLogin)))
	s.mux.HandleFunc("POST /logout", s.withSession(s.requireCSRF(s.handleLogout)))
	s.mux.HandleFunc("POST /link", s.withSession(s.requireCSRF(s.requireLogin(s.handleLink))))
	s.mux.HandleFunc("POST /unlink", s.withSession(s.requireCSRF(s.requireLogin(s.handleUnlink))))
	s.mux.HandleFunc("GET /callback", s.withSession(s.requireLogin(s.handleCallback)))
	s.mux.HandleFunc("GET /privacy", s.handlePrivacy)
	s.mux.HandleFunc("GET /static/style.css", serveStyleSheet)
	return s
}

func serveStyleSheet(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(styleSheet) // nothing to do if the client went away
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cache-Control", "no-store")
	if s.opts.SecureCookies {
		header.Set("Strict-Transport-Security", "max-age=31536000")
	}
	s.mux.ServeHTTP(w, r)
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess *session)

func (s *Server) withSession(next sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.sessions.get(w, r)
		if err != nil {
			s.log.Error("creating session failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		sess.mu.Lock()
		defer sess.mu.Unlock()
		next(w, r, sess)
	}
}

func (s *Server) requireCSRF(next sessionHandler) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		r.Body = http.MaxBytesReader(w, r.Body, maxFormSize)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !equal(r.PostFormValue("csrf"), sess.csrfToken) {
			http.Error(w, "the form expired, please reload the page", http.StatusForbidden)
			return
		}
		next(w, r, sess)
	}
}

func (s *Server) requireLogin(next sessionHandler) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		if !sess.loggedIn() {
			s.redirectHome(w, r, sess, "Please sign in first.")
			return
		}
		next(w, r, sess)
	}
}

type pageData struct {
	CSRFToken    string
	Username     string
	Linked       bool
	DiscordName  string
	LinkedAt     time.Time
	Flash        string
	DiscordScope string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request, sess *session) {
	data := pageData{CSRFToken: sess.csrfToken, Username: sess.username, Flash: sess.flash, DiscordScope: discord.Scopes}
	sess.flash = ""

	if sess.loggedIn() {
		link, err := s.links.Get(sess.username)
		switch {
		case err == nil:
			data.Linked, data.DiscordName, data.LinkedAt = true, link.DiscordName, link.LinkedAt
		case !errors.Is(err, store.ErrNotLinked):
			s.log.Error("reading link failed", "user", sess.username, "error", err)
		}
	}

	if err := pageTemplate.render(w, data); err != nil {
		s.log.Error("rendering page failed", "error", err)
	}
}

func (s *Server) handlePrivacy(w http.ResponseWriter, _ *http.Request) {
	if err := privacyTemplate.render(w, s.opts.Privacy); err != nil {
		s.log.Error("rendering privacy notice failed", "error", err)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request, sess *session) {
	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	ip := s.clientIP(r)
	userKey := strings.ToLower(username)

	if username == "" || password == "" {
		s.redirectHome(w, r, sess, "Please enter your Navidrome username and password.")
		return
	}
	if !s.ipLimiter.Allow(ip) || !s.userLimiter.Allow(userKey) {
		s.log.Warn("login rate limited", "ip", ip, "user", username)
		s.redirectHome(w, r, sess, "Too many sign-in attempts. Please wait a few minutes.")
		return
	}

	canonical, err := s.credentials.Verify(r.Context(), username, password)
	if err != nil {
		if errors.Is(err, navidrome.ErrInvalidCredentials) {
			s.log.Info("login failed", "ip", ip, "user", username)
			s.redirectHome(w, r, sess, "Wrong username or password.")
		} else {
			s.log.Error("navidrome login check failed", "error", err)
			s.redirectHome(w, r, sess, "Navidrome could not be reached. Please try again later.")
		}
		return
	}

	s.userLimiter.Reset(userKey)
	if err := s.sessions.rotate(w, sess); err != nil {
		s.log.Error("rotating session failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sess.signIn(canonical)
	if err := sess.renewCSRFToken(); err != nil {
		s.log.Error("renewing CSRF token failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.log.Info("signed in", "ip", ip, "user", canonical)
	s.redirectHome(w, r, sess, "")
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, sess *session) {
	s.sessions.destroy(w, sess)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLink(w http.ResponseWriter, r *http.Request, sess *session) {
	state, err := randomToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	verifier, challenge, err := discord.NewPKCE()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sess.oauthState, sess.oauthVerifier = state, verifier
	sess.oauthExpires = time.Now().Add(oauthStateTimeout)
	http.Redirect(w, r, s.oauth.AuthorizeURL(state, challenge), http.StatusSeeOther)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request, sess *session) {
	query := r.URL.Query()
	verifier, ok := sess.consumeOAuthState(query.Get("state"), time.Now())
	if !ok {
		s.redirectHome(w, r, sess, "That link request expired or didn't come from this page. Please try again.")
		return
	}
	if code := query.Get("error"); code != "" {
		s.log.Warn("Discord refused the link", "user", sess.username,
			"error", shorten(code, 64), "description", shorten(query.Get("error_description"), 200))
		if code == "access_denied" {
			s.redirectHome(w, r, sess, "Linking was cancelled on Discord.")
		} else {
			s.redirectHome(w, r, sess, "Discord refused the request ("+oauthErrorCode(code)+"). The bridge log has the details.")
		}
		return
	}

	token, err := s.oauth.Exchange(r.Context(), query.Get("code"), verifier)
	if err != nil {
		s.log.Warn("code exchange failed", "user", sess.username, "error", err)
		s.redirectHome(w, r, sess, "Discord did not accept the request. Please try again.")
		return
	}
	user, err := s.oauth.UserInfo(r.Context(), token.AccessToken)
	if err != nil {
		s.log.Warn("reading Discord account failed", "user", sess.username, "error", err)
		s.revoke(r.Context(), token.RefreshToken)
		s.redirectHome(w, r, sess, "Your Discord account could not be read. Please try again.")
		return
	}

	previous, previousErr := s.links.Get(sess.username)
	link := store.Link{DiscordID: user.ID, DiscordName: user.Name, RefreshToken: token.RefreshToken, LinkedAt: time.Now().UTC()}
	if err := s.links.Put(sess.username, link); err != nil {
		s.log.Error("saving link failed", "user", sess.username, "error", err)
		s.revoke(r.Context(), token.RefreshToken)
		s.redirectHome(w, r, sess, "The link could not be saved. Please try again.")
		return
	}
	if previousErr == nil {
		s.revoke(r.Context(), previous.RefreshToken)
		s.presence.Forget(sess.username)
	}

	s.log.Info("linked Discord account", "user", sess.username)
	s.log.Debug("linked Discord account details", "user", sess.username, "discord", user.ID)
	s.redirectHome(w, r, sess, "Your Discord account is linked. Start playing something!")
}

func (s *Server) handleUnlink(w http.ResponseWriter, r *http.Request, sess *session) {
	link, err := s.links.Get(sess.username)
	if errors.Is(err, store.ErrNotLinked) {
		s.redirectHome(w, r, sess, "")
		return
	}
	if err != nil {
		s.log.Error("reading link failed", "user", sess.username, "error", err)
	}
	if err := s.links.Delete(sess.username); err != nil {
		s.log.Error("deleting link failed", "user", sess.username, "error", err)
		s.redirectHome(w, r, sess, "The link could not be removed. Please try again.")
		return
	}
	s.presence.Forget(sess.username)
	if link.RefreshToken != "" {
		s.revoke(r.Context(), link.RefreshToken)
	}

	s.log.Info("unlinked Discord account", "user", sess.username)
	s.redirectHome(w, r, sess, "Your Discord account is unlinked and access was revoked.")
}

// revoke invalidates a refresh token at Discord. Failures are only logged:
// the token is already gone from the store.
func (s *Server) revoke(ctx context.Context, refreshToken string) {
	if err := s.oauth.Revoke(ctx, refreshToken); err != nil {
		s.log.Warn("revoking token at Discord failed", "error", err)
	}
}

func (s *Server) redirectHome(w http.ResponseWriter, r *http.Request, sess *session, flash string) {
	sess.flash = flash
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// clientIP returns the address of the browser. X-Forwarded-For is only used
// when the request comes from a trusted reverse proxy, and then only its last
// entry, which that proxy added itself.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote := net.ParseIP(host)
	for _, proxy := range s.opts.TrustedProxies {
		if remote != nil && remote.Equal(proxy) {
			forwarded := r.Header.Values("X-Forwarded-For")
			if len(forwarded) == 0 {
				break
			}
			hops := strings.Split(forwarded[len(forwarded)-1], ",")
			if last := strings.TrimSpace(hops[len(hops)-1]); net.ParseIP(last) != nil {
				return last
			}
			break
		}
	}
	return host
}

// oauthErrorCode returns the error code Discord sent back, or a placeholder
// when it doesn't look like one, so the page never echoes arbitrary input.
func oauthErrorCode(code string) string {
	if len(code) == 0 || len(code) > 64 {
		return "unknown error"
	}
	for _, c := range code {
		if (c < 'a' || c > 'z') && c != '_' {
			return "unknown error"
		}
	}
	return code
}

func shorten(s string, max int) string {
	if runes := []rune(s); len(runes) > max {
		return string(runes[:max]) + "..."
	}
	return s
}
