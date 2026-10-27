package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sessionIdleTimeout     = 30 * time.Minute
	anonymousIdleTimeout   = 10 * time.Minute
	sessionAbsoluteTimeout = 12 * time.Hour
	oauthStateTimeout      = 10 * time.Minute
	pruneInterval          = time.Minute
	maxSessions            = 10_000
)

// session is server-side state for one browser. Only a random ID is stored in
// the cookie. Handlers hold mu while they use the session; id, created and
// lastSeen belong to the sessionStore and are guarded by its lock.
type session struct {
	mu sync.Mutex

	id        string
	csrfToken string
	username  string // set after a successful Navidrome login
	flash     string

	oauthState    string
	oauthVerifier string
	oauthExpires  time.Time

	created  time.Time
	lastSeen time.Time
	// authenticated mirrors username != "" for the store, which reads it
	// without holding mu.
	authenticated atomic.Bool
}

func (s *session) loggedIn() bool { return s.username != "" }

func (s *session) signIn(username string) {
	s.username = username
	s.authenticated.Store(true)
}

// renewCSRFToken replaces the form token, so a token seen before signing in
// is useless afterwards.
func (s *session) renewCSRFToken() error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	s.csrfToken = token
	return nil
}

// consumeOAuthState checks the state returned by Discord and invalidates it,
// so every authorization can be completed only once.
func (s *session) consumeOAuthState(state string, now time.Time) (verifier string, ok bool) {
	expected, verifier, expires := s.oauthState, s.oauthVerifier, s.oauthExpires
	s.oauthState, s.oauthVerifier = "", ""
	if expected == "" || now.After(expires) || !equal(state, expected) {
		return "", false
	}
	return verifier, true
}

type sessionStore struct {
	mu         sync.Mutex
	sessions   map[string]*session
	cookieName string
	secure     bool
	now        func() time.Time
	lastPrune  time.Time
}

func newSessionStore(secure bool) *sessionStore {
	name := "nds_session"
	if secure {
		// The __Host- prefix makes browsers insist on Secure, Path=/ and no Domain.
		name = "__Host-nds_session"
	}
	return &sessionStore{sessions: make(map[string]*session), cookieName: name, secure: secure, now: time.Now}
}

// get returns the request's session, creating a new one when there is none
// or it expired. Callers must hold the returned session only for the request.
func (st *sessionStore) get(w http.ResponseWriter, r *http.Request) (*session, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	now := st.now()
	if cookie, err := r.Cookie(st.cookieName); err == nil {
		if s, ok := st.sessions[cookie.Value]; ok && st.valid(s, now) {
			s.lastSeen = now
			return s, nil
		}
	}
	return st.create(w, now)
}

// rotate replaces the session ID, keeping its contents. Called on login to
// prevent session fixation.
func (st *sessionStore) rotate(w http.ResponseWriter, s *session) error {
	st.mu.Lock()
	defer st.mu.Unlock()

	id, err := randomToken()
	if err != nil {
		return err
	}
	delete(st.sessions, s.id)
	s.id = id
	st.sessions[id] = s
	st.setCookie(w, id, int(sessionAbsoluteTimeout.Seconds()))
	return nil
}

func (st *sessionStore) destroy(w http.ResponseWriter, s *session) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, s.id)
	st.setCookie(w, "", -1)
}

// create starts a new session. Expired sessions are pruned regularly, and
// when the store is full the oldest session is evicted (anonymous ones
// first), so a flood of new visitors can't grow memory without limit.
func (st *sessionStore) create(w http.ResponseWriter, now time.Time) (*session, error) {
	if now.Sub(st.lastPrune) >= pruneInterval || len(st.sessions) >= maxSessions {
		st.prune(now)
		st.lastPrune = now
	}
	if len(st.sessions) >= maxSessions {
		st.evictOldest()
	}
	id, err := randomToken()
	if err != nil {
		return nil, err
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, err
	}
	s := &session{id: id, csrfToken: csrf, created: now, lastSeen: now}
	st.sessions[id] = s
	st.setCookie(w, id, int(sessionAbsoluteTimeout.Seconds()))
	return s, nil
}

func (st *sessionStore) valid(s *session, now time.Time) bool {
	idleTimeout := anonymousIdleTimeout
	if s.authenticated.Load() {
		idleTimeout = sessionIdleTimeout
	}
	return now.Sub(s.lastSeen) < idleTimeout && now.Sub(s.created) < sessionAbsoluteTimeout
}

func (st *sessionStore) prune(now time.Time) {
	for id, s := range st.sessions {
		if !st.valid(s, now) {
			delete(st.sessions, id)
		}
	}
}

// evictOldest removes the least recently used anonymous session, or the least
// recently used session of all when every session is signed in.
func (st *sessionStore) evictOldest() {
	var oldestAnonymous, oldest *session
	for _, s := range st.sessions {
		if oldest == nil || s.lastSeen.Before(oldest.lastSeen) {
			oldest = s
		}
		if !s.authenticated.Load() && (oldestAnonymous == nil || s.lastSeen.Before(oldestAnonymous.lastSeen)) {
			oldestAnonymous = s
		}
	}
	if oldestAnonymous != nil {
		oldest = oldestAnonymous
	}
	if oldest != nil {
		delete(st.sessions, oldest.id)
	}
}

func (st *sessionStore) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     st.cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   st.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
