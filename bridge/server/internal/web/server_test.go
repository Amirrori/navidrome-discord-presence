package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"navidrome-discord-status/bridge/internal/discord"
	"navidrome-discord-status/bridge/internal/navidrome"
	"navidrome-discord-status/bridge/internal/store"
)

type fakeCredentials struct{}

func (fakeCredentials) Verify(_ context.Context, username, password string) (string, error) {
	if strings.EqualFold(username, "alice") && password == "right" {
		return "alice", nil
	}
	return "", navidrome.ErrInvalidCredentials
}

type fakeOAuth struct {
	mu        sync.Mutex
	exchanged []string
	revoked   []string
}

func (f *fakeOAuth) AuthorizeURL(state, challenge string) string {
	return "https://discord.com/oauth2/authorize?state=" + url.QueryEscape(state) + "&code_challenge=" + challenge
}

func (f *fakeOAuth) Exchange(_ context.Context, code, verifier string) (discord.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchanged = append(f.exchanged, code)
	if verifier == "" {
		return discord.Token{}, errors.New("missing verifier")
	}
	return discord.Token{AccessToken: "at", RefreshToken: "rt-" + code}, nil
}

func (f *fakeOAuth) UserInfo(context.Context, string) (discord.User, error) {
	return discord.User{ID: "42", Name: "alice_discord"}, nil
}

func (f *fakeOAuth) Revoke(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, token)
	return nil
}

type memoryLinks struct {
	mu    sync.Mutex
	links map[string]store.Link
}

func (m *memoryLinks) Get(u string) (store.Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[u]
	if !ok {
		return store.Link{}, store.ErrNotLinked
	}
	return l, nil
}

func (m *memoryLinks) Put(u string, l store.Link) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.links[u] = l
	return nil
}

func (m *memoryLinks) Delete(u string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.links, u)
	return nil
}

type fakeSessions struct{ forgotten []string }

func (f *fakeSessions) Forget(u string) { f.forgotten = append(f.forgotten, u) }

type testEnv struct {
	server   *httptest.Server
	client   *http.Client
	oauth    *fakeOAuth
	links    *memoryLinks
	presence *fakeSessions
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	env := &testEnv{oauth: &fakeOAuth{}, links: &memoryLinks{links: map[string]store.Link{}}, presence: &fakeSessions{}}
	opts := Options{Privacy: PrivacyInfo{Contact: "admin@example.com", LinkRetentionDays: 365, LogRetentionDays: 14}}
	handler := NewServer(fakeCredentials{}, env.oauth, env.links, env.presence, opts,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	env.server = httptest.NewServer(handler)
	t.Cleanup(env.server.Close)

	jar, _ := cookiejar.New(nil)
	env.client = &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return env
}

var csrfPattern = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// page fetches the index page and returns its body and CSRF token.
func (e *testEnv) page(t *testing.T) (string, string) {
	t.Helper()
	resp, err := e.client.Get(e.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	match := csrfPattern.FindSubmatch(body)
	if match == nil {
		t.Fatalf("no CSRF token in page:\n%s", body)
	}
	return string(body), string(match[1])
}

func (e *testEnv) post(t *testing.T, path string, form url.Values) *http.Response {
	t.Helper()
	resp, err := e.client.PostForm(e.server.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func (e *testEnv) login(t *testing.T) string {
	t.Helper()
	_, csrf := e.page(t)
	resp := e.post(t, "/login", url.Values{"csrf": {csrf}, "username": {"Alice"}, "password": {"right"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	body, csrf := e.page(t)
	if !strings.Contains(body, "Signed in as <strong>alice</strong>") {
		t.Fatalf("not signed in:\n%s", body)
	}
	return csrf
}

func (e *testEnv) sessionCookie(t *testing.T) string {
	t.Helper()
	u, _ := url.Parse(e.server.URL)
	for _, c := range e.client.Jar.Cookies(u) {
		if c.Name == "nds_session" {
			return c.Value
		}
	}
	return ""
}

// startLink posts the link form and returns the state sent to Discord.
func (e *testEnv) startLink(t *testing.T, csrf string) string {
	t.Helper()
	resp := e.post(t, "/link", url.Values{"csrf": {csrf}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("link status = %d", resp.StatusCode)
	}
	location, _ := url.Parse(resp.Header.Get("Location"))
	if location.Host != "discord.com" {
		t.Fatalf("link redirects to %s", location)
	}
	return location.Query().Get("state")
}

func (e *testEnv) callback(t *testing.T, state, code string) {
	t.Helper()
	resp, err := e.client.Get(e.server.URL + "/callback?state=" + url.QueryEscape(state) + "&code=" + code)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestSecurityHeaders(t *testing.T) {
	env := newTestEnv(t)
	resp, err := env.client.Get(env.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if resp.Header.Get(header) == "" {
			t.Errorf("missing %s", header)
		}
	}
	for _, c := range resp.Cookies() {
		if c.Name == "nds_session" && (!c.HttpOnly || c.SameSite != http.SameSiteLaxMode) {
			t.Errorf("session cookie lacks HttpOnly/SameSite: %+v", c)
		}
	}
}

func TestLoginRequiresCSRF(t *testing.T) {
	env := newTestEnv(t)
	env.page(t)
	resp := env.post(t, "/login", url.Values{"csrf": {"forged"}, "username": {"alice"}, "password": {"right"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestLoginRotatesSession(t *testing.T) {
	env := newTestEnv(t)
	env.page(t)
	before := env.sessionCookie(t)
	env.login(t)
	if after := env.sessionCookie(t); after == "" || after == before {
		t.Error("the session ID must change on login")
	}
}

func TestLoginRateLimit(t *testing.T) {
	env := newTestEnv(t)
	_, csrf := env.page(t)
	for range loginAttemptsPerUser {
		env.post(t, "/login", url.Values{"csrf": {csrf}, "username": {"alice"}, "password": {"wrong"}})
	}
	env.post(t, "/login", url.Values{"csrf": {csrf}, "username": {"alice"}, "password": {"right"}})
	if body, _ := env.page(t); !strings.Contains(body, "Too many sign-in attempts") {
		t.Errorf("a correct password must still be refused while rate limited:\n%s", body)
	}
}

func TestLinkFlow(t *testing.T) {
	env := newTestEnv(t)
	csrf := env.login(t)
	state := env.startLink(t, csrf)
	env.callback(t, state, "code1")

	link, err := env.links.Get("alice")
	if err != nil || link.DiscordID != "42" || link.RefreshToken != "rt-code1" {
		t.Fatalf("link = %+v, %v", link, err)
	}
	if body, _ := env.page(t); !strings.Contains(body, "alice_discord") {
		t.Errorf("page doesn't show the linked account:\n%s", body)
	}
}

func TestCallbackStateIsSingleUseAndChecked(t *testing.T) {
	env := newTestEnv(t)
	csrf := env.login(t)

	env.startLink(t, csrf)
	env.callback(t, "forged-state", "evil")
	if len(env.oauth.exchanged) != 0 {
		t.Fatal("a callback with a foreign state must not exchange the code")
	}

	state := env.startLink(t, csrf)
	env.callback(t, state, "code1")
	env.callback(t, state, "code2")
	if len(env.oauth.exchanged) != 1 {
		t.Errorf("state reused: %d exchanges", len(env.oauth.exchanged))
	}
}

func TestCallbackRequiresLogin(t *testing.T) {
	env := newTestEnv(t)
	env.callback(t, "anything", "code")
	if len(env.oauth.exchanged) != 0 {
		t.Error("a callback without a signed-in session must not exchange the code")
	}
}

func TestRelinkRevokesPreviousToken(t *testing.T) {
	env := newTestEnv(t)
	csrf := env.login(t)
	env.callback(t, env.startLink(t, csrf), "code1")
	env.callback(t, env.startLink(t, csrf), "code2")

	if len(env.oauth.revoked) != 1 || env.oauth.revoked[0] != "rt-code1" {
		t.Errorf("revoked = %v, want the previous token", env.oauth.revoked)
	}
}

func TestUnlinkRevokesAndForgets(t *testing.T) {
	env := newTestEnv(t)
	csrf := env.login(t)
	env.callback(t, env.startLink(t, csrf), "code1")

	env.post(t, "/unlink", url.Values{"csrf": {csrf}})
	if _, err := env.links.Get("alice"); !errors.Is(err, store.ErrNotLinked) {
		t.Error("link not removed")
	}
	if len(env.oauth.revoked) != 1 || env.oauth.revoked[0] != "rt-code1" {
		t.Errorf("revoked = %v", env.oauth.revoked)
	}
	if len(env.presence.forgotten) != 1 {
		t.Error("the status session must be stopped")
	}
}

func TestLinkRequiresLogin(t *testing.T) {
	env := newTestEnv(t)
	_, csrf := env.page(t)
	resp := env.post(t, "/link", url.Values{"csrf": {csrf}})
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("anonymous link redirected to %q, want the start page", loc)
	}
}

func TestClientIP(t *testing.T) {
	proxy := net.ParseIP("10.0.0.16")
	s := &Server{opts: Options{TrustedProxies: []net.IP{proxy}}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.16:5000"
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 192.168.1.5")
	if got := s.clientIP(req); got != "192.168.1.5" {
		t.Errorf("via proxy: %s, want the address the proxy added", got)
	}

	req.RemoteAddr = "192.168.1.99:5000"
	if got := s.clientIP(req); got != "192.168.1.99" {
		t.Errorf("direct: %s, X-Forwarded-For from untrusted clients must be ignored", got)
	}
}

func TestSessionStoreStaysBounded(t *testing.T) {
	st := newSessionStore(false)
	now := time.Unix(1_700_000_000, 0)
	st.now = func() time.Time { return now }

	signedIn, err := st.create(httptest.NewRecorder(), now)
	if err != nil {
		t.Fatal(err)
	}
	signedIn.signIn("alice")

	for i := 0; i < maxSessions+50; i++ {
		now = now.Add(time.Millisecond)
		if _, err := st.create(httptest.NewRecorder(), now); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(st.sessions); n > maxSessions {
		t.Errorf("%d sessions stored, limit is %d", n, maxSessions)
	}
	if _, ok := st.sessions[signedIn.id]; !ok {
		t.Error("anonymous sessions must be evicted before signed-in ones")
	}
}

func TestAnonymousSessionsExpireSooner(t *testing.T) {
	st := newSessionStore(false)
	now := time.Unix(1_700_000_000, 0)
	anonymous, _ := st.create(httptest.NewRecorder(), now)
	signedIn, _ := st.create(httptest.NewRecorder(), now)
	signedIn.signIn("alice")

	later := now.Add(anonymousIdleTimeout + time.Second)
	if st.valid(anonymous, later) {
		t.Error("an idle anonymous session must expire after the anonymous timeout")
	}
	if !st.valid(signedIn, later) {
		t.Error("a signed-in session keeps the longer idle timeout")
	}
}

func TestPrivacyNotice(t *testing.T) {
	env := newTestEnv(t)
	resp, err := env.client.Get(env.server.URL + "/privacy")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)

	for _, want := range []string{
		"<title>Privacy notice",
		"admin@example.com",
		"365 days after it was last used",
		"14 days",
		"uguu.se",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("privacy notice is missing %q", want)
		}
	}
}

func TestLinkButtonPointsToPrivacyNotice(t *testing.T) {
	env := newTestEnv(t)
	env.login(t)
	body, _ := env.page(t)
	if !strings.Contains(body, `href="/privacy"`) || !strings.Contains(body, "I agree, link my Discord account") {
		t.Errorf("the link form must refer to the privacy notice:\n%s", body)
	}
}

func TestHSTSOnlyOverHTTPS(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, secure := range []bool{false, true} {
		s := NewServer(fakeCredentials{}, &fakeOAuth{}, &memoryLinks{links: map[string]store.Link{}}, &fakeSessions{},
			Options{SecureCookies: secure}, log)
		rr := httptest.NewRecorder()
		s.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if got := rr.Header().Get("Strict-Transport-Security") != ""; got != secure {
			t.Errorf("secure=%v: HSTS header present = %v", secure, got)
		}
	}
}

func TestCSRFTokenChangesOnSignIn(t *testing.T) {
	env := newTestEnv(t)
	_, before := env.page(t)
	after := env.login(t)
	if before == after {
		t.Fatal("the CSRF token must change when signing in")
	}
	resp := env.post(t, "/link", url.Values{"csrf": {before}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("the old token was accepted: status %d", resp.StatusCode)
	}
}

func TestCallbackErrorFromDiscord(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"access_denied", "Linking was cancelled on Discord."},
		{"invalid_scope", "Discord refused the request (invalid_scope)."},
		{"<script>", "Discord refused the request (unknown error)."},
	} {
		env := newTestEnv(t)
		csrf := env.login(t)
		state := env.startLink(t, csrf)
		resp, err := env.client.Get(env.server.URL + "/callback?state=" + url.QueryEscape(state) + "&error=" + url.QueryEscape(tc.code))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		body, _ := env.page(t)
		if !strings.Contains(body, tc.want) {
			t.Errorf("error %q: page doesn't say %q:\n%s", tc.code, tc.want, body)
		}
		if _, err := env.links.Get("alice"); err == nil {
			t.Errorf("error %q: a link was saved", tc.code)
		}
	}
}
