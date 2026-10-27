// Package events receives signed playback events from the Navidrome plugin.
//
// Every request carries a timestamp, a random nonce and an HMAC-SHA256
// signature over "timestamp.nonce.body". Requests that are unsigned, altered,
// older than the allowed clock skew or replayed are rejected before the body
// is even parsed as JSON.
package events

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Path receives playback events.
	Path = "/v1/events"
	// LinkedPath answers whether a user has linked a Discord account, so the
	// plugin can skip all work (and third-party lookups) for everyone else.
	LinkedPath = "/v1/linked"

	HeaderTimestamp = "X-NDS-Timestamp"
	HeaderNonce     = "X-NDS-Nonce"
	HeaderSignature = "X-NDS-Signature"

	signatureScheme = "v1="
	maxBodySize     = 8 << 10
	maxClockSkew    = 30 * time.Second
	maxNonces       = 10_000

	supportedVersion = 1
	maxUsernameLen   = 255
)

// Playback states sent by the plugin.
const (
	StatePlaying = "playing"
	StatePaused  = "paused"
	StateStopped = "stopped"
)

type Event struct {
	Version int    `json:"v"`
	User    string `json:"user"`
	State   string `json:"state"`
	Track   *Track `json:"track,omitempty"`
}

// Track describes what is playing. Text is untrusted and must be sanitized
// before display.
type Track struct {
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Album     string `json:"album"`
	StartMs   int64  `json:"startMs"`
	EndMs     int64  `json:"endMs,omitempty"`
	CoverURL  string `json:"coverUrl,omitempty"`
	TrackURL  string `json:"trackUrl,omitempty"`
	ArtistURL string `json:"artistUrl,omitempty"`
}

type linkQuery struct {
	Version int    `json:"v"`
	User    string `json:"user"`
}

type linkStatus struct {
	Linked bool `json:"linked"`
}

// Sink receives a valid event and reports whether the user is linked.
type Sink func(Event) (linked bool)

type LinkChecker func(username string) bool

// Handler authenticates requests from the plugin and serves both endpoints.
type Handler struct {
	secret []byte
	sink   Sink
	linked LinkChecker
	nonces *nonceCache
	now    func() time.Time
	log    *slog.Logger
	mux    *http.ServeMux
}

func NewHandler(secret []byte, sink Sink, linked LinkChecker, log *slog.Logger) *Handler {
	h := &Handler{
		secret: secret,
		sink:   sink,
		linked: linked,
		nonces: newNonceCache(maxNonces),
		now:    time.Now,
		log:    log,
		mux:    http.NewServeMux(),
	}
	h.mux.HandleFunc("POST "+Path, h.authenticated(h.handleEvent))
	h.mux.HandleFunc("POST "+LinkedPath, h.authenticated(h.handleLinked))
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// authenticated reads the body, checks the signature and only then calls next.
func (h *Handler) authenticated(next func(http.ResponseWriter, []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		if err := h.authenticate(r.Header, body); err != nil {
			h.log.Warn("rejected request", "remote", r.RemoteAddr, "path", r.URL.Path, "reason", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, body)
	}
}

func (h *Handler) handleEvent(w http.ResponseWriter, body []byte) {
	event, err := decodeEvent(body)
	if err != nil {
		h.log.Warn("invalid event", "reason", err)
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	writeStatus(w, h.sink(event))
}

func (h *Handler) handleLinked(w http.ResponseWriter, body []byte) {
	var query linkQuery
	if err := decodeStrict(body, &query); err != nil || query.Version != supportedVersion || !validUser(query.User) {
		http.Error(w, "invalid query", http.StatusBadRequest)
		return
	}
	writeStatus(w, h.linked(query.User))
}

func writeStatus(w http.ResponseWriter, linked bool) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(linkStatus{Linked: linked})
}

// authenticate checks freshness, signature and uniqueness, in that order, so
// that unauthenticated requests can never fill the nonce cache.
func (h *Handler) authenticate(header http.Header, body []byte) error {
	timestamp := header.Get(HeaderTimestamp)
	nonce := header.Get(HeaderNonce)
	signature := header.Get(HeaderSignature)

	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("missing or malformed timestamp")
	}
	sentAt := time.Unix(seconds, 0)
	now := h.now()
	if sentAt.Before(now.Add(-maxClockSkew)) || sentAt.After(now.Add(maxClockSkew)) {
		return errors.New("timestamp outside the allowed window")
	}
	if len(nonce) != 32 || !isHex(nonce) {
		return errors.New("missing or malformed nonce")
	}

	given, err := hex.DecodeString(strings.TrimPrefix(signature, signatureScheme))
	if !strings.HasPrefix(signature, signatureScheme) || err != nil {
		return errors.New("missing or malformed signature")
	}
	if !hmac.Equal(given, Sign(h.secret, timestamp, nonce, body)) {
		return errors.New("signature mismatch")
	}

	if !h.nonces.add(nonce, now.Add(2*maxClockSkew), now) {
		return errors.New("replayed nonce")
	}
	return nil
}

// Sign returns the HMAC-SHA256 of "timestamp.nonce.body".
func Sign(secret []byte, timestamp, nonce string, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write([]byte(nonce))
	mac.Write([]byte("."))
	mac.Write(body)
	return mac.Sum(nil)
}

// decodeStrict decodes exactly one JSON object, rejecting unknown fields.
func decodeStrict(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing data after the object")
	}
	return nil
}

func validUser(user string) bool {
	return user != "" && len(user) <= maxUsernameLen
}

func decodeEvent(body []byte) (Event, error) {
	var event Event
	if err := decodeStrict(body, &event); err != nil {
		return Event{}, err
	}
	if event.Version != supportedVersion {
		return Event{}, errors.New("unsupported event version")
	}
	if !validUser(event.User) {
		return Event{}, errors.New("missing or overlong user")
	}

	switch event.State {
	case StatePlaying, StatePaused:
		if event.Track == nil {
			return Event{}, errors.New("missing track")
		}
	case StateStopped:
		event.Track = nil
	default:
		return Event{}, errors.New("unknown state")
	}
	return event, nil
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// nonceCache remembers recently seen nonces until they expire.
type nonceCache struct {
	mu      sync.Mutex
	limit   int
	expires map[string]time.Time
}

func newNonceCache(limit int) *nonceCache {
	return &nonceCache{limit: limit, expires: make(map[string]time.Time)}
}

// add records the nonce and reports whether it was new. When the cache is
// full of unexpired nonces, new ones are refused rather than forgetting old
// ones, which would reopen a replay window.
func (c *nonceCache) add(nonce string, expiresAt, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if expiry, seen := c.expires[nonce]; seen && now.Before(expiry) {
		return false
	}
	if len(c.expires) >= c.limit {
		for n, expiry := range c.expires {
			if !now.Before(expiry) {
				delete(c.expires, n)
			}
		}
		if len(c.expires) >= c.limit {
			return false
		}
	}
	c.expires[nonce] = expiresAt
	return true
}
