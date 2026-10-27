package events

import (
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	testSecret = []byte("0123456789abcdef0123456789abcdef")
	testNow    = time.Unix(1_700_000_000, 0)
)

const playingBody = `{"v":1,"user":"alice","state":"playing","track":{"title":"Undone","artist":"Weezer","album":"Weezer","startMs":1,"endMs":2}}`

type recorder struct{ events []Event }

func newTestHandler() (*Handler, *recorder) {
	rec := &recorder{}
	sink := func(e Event) bool {
		rec.events = append(rec.events, e)
		return e.User == "alice"
	}
	linked := func(user string) bool { return user == "alice" }
	h := NewHandler(testSecret, sink, linked, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.now = func() time.Time { return testNow }
	return h, rec
}

func signedRequest(body, nonce string, sentAt time.Time, secret []byte) *http.Request {
	return signedRequestTo(Path, body, nonce, sentAt, secret)
}

func signedRequestTo(path, body, nonce string, sentAt time.Time, secret []byte) *http.Request {
	timestamp := strconv.FormatInt(sentAt.Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, signatureScheme+hex.EncodeToString(Sign(secret, timestamp, nonce, []byte(body))))
	return req
}

func serve(h *Handler, req *http.Request) int {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code
}

const nonceA = "00112233445566778899aabbccddeeff"
const nonceB = "ffeeddccbbaa99887766554433221100"

func TestAcceptsValidEvent(t *testing.T) {
	h, rec := newTestHandler()
	if code := serve(h, signedRequest(playingBody, nonceA, testNow, testSecret)); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(rec.events) != 1 || rec.events[0].Track.Title != "Undone" {
		t.Errorf("events = %+v", rec.events)
	}
}

func TestMatchesPluginSignature(t *testing.T) {
	// Reference value shared with the plugin's tests (computed with openssl).
	got := hex.EncodeToString(Sign(testSecret, "1700000000", nonceA, []byte(`{"v":1}`)))
	if got != "cc5dea683ee6cc8c872dad6d45d8d07c02df0da728efa619ecd3eb72635d504a" {
		t.Errorf("signature = %s", got)
	}
}

func TestRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name string
		req  func() *http.Request
		want int
	}{
		{"wrong secret", func() *http.Request {
			return signedRequest(playingBody, nonceA, testNow, []byte("another-secret-another-secret-xx"))
		}, http.StatusUnauthorized},
		{"altered body", func() *http.Request {
			req := signedRequest(playingBody, nonceA, testNow, testSecret)
			req.Body = io.NopCloser(strings.NewReader(strings.Replace(playingBody, "alice", "bob", 1)))
			return req
		}, http.StatusUnauthorized},
		{"too old", func() *http.Request {
			return signedRequest(playingBody, nonceA, testNow.Add(-time.Minute), testSecret)
		}, http.StatusUnauthorized},
		{"from the future", func() *http.Request {
			return signedRequest(playingBody, nonceA, testNow.Add(time.Minute), testSecret)
		}, http.StatusUnauthorized},
		{"no signature", func() *http.Request {
			req := signedRequest(playingBody, nonceA, testNow, testSecret)
			req.Header.Del(HeaderSignature)
			return req
		}, http.StatusUnauthorized},
		{"short nonce", func() *http.Request {
			return signedRequest(playingBody, "abc", testNow, testSecret)
		}, http.StatusUnauthorized},
		{"wrong method", func() *http.Request {
			return httptest.NewRequest(http.MethodGet, Path, nil)
		}, http.StatusMethodNotAllowed},
		{"too large", func() *http.Request {
			return signedRequest(strings.Repeat(" ", maxBodySize+1), nonceA, testNow, testSecret)
		}, http.StatusRequestEntityTooLarge},
		{"unknown field", func() *http.Request {
			return signedRequest(`{"v":1,"user":"alice","state":"stopped","extra":1}`, nonceA, testNow, testSecret)
		}, http.StatusBadRequest},
		{"unknown state", func() *http.Request {
			return signedRequest(`{"v":1,"user":"alice","state":"rewinding"}`, nonceA, testNow, testSecret)
		}, http.StatusBadRequest},
		{"playing without track", func() *http.Request {
			return signedRequest(`{"v":1,"user":"alice","state":"playing"}`, nonceA, testNow, testSecret)
		}, http.StatusBadRequest},
		{"future version", func() *http.Request {
			return signedRequest(`{"v":2,"user":"alice","state":"stopped"}`, nonceA, testNow, testSecret)
		}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rec := newTestHandler()
			if code := serve(h, tt.req()); code != tt.want {
				t.Errorf("status = %d, want %d", code, tt.want)
			}
			if len(rec.events) != 0 {
				t.Error("a rejected request must not reach the sink")
			}
		})
	}
}

func TestRejectsReplay(t *testing.T) {
	h, rec := newTestHandler()
	if code := serve(h, signedRequest(playingBody, nonceA, testNow, testSecret)); code != http.StatusOK {
		t.Fatalf("first request: %d", code)
	}
	if code := serve(h, signedRequest(playingBody, nonceA, testNow, testSecret)); code != http.StatusUnauthorized {
		t.Errorf("replay: status = %d", code)
	}
	if code := serve(h, signedRequest(playingBody, nonceB, testNow, testSecret)); code != http.StatusOK {
		t.Errorf("fresh nonce: status = %d", code)
	}
	if len(rec.events) != 2 {
		t.Errorf("events = %d, want 2", len(rec.events))
	}
}

func TestStoppedEventDropsTrack(t *testing.T) {
	h, rec := newTestHandler()
	body := `{"v":1,"user":"alice","state":"stopped","track":{"title":"x","artist":"y","album":"z","startMs":1}}`
	if code := serve(h, signedRequest(body, nonceA, testNow, testSecret)); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if rec.events[0].Track != nil {
		t.Error("stopped events must not carry a track")
	}
}

func TestNonceCacheRefusesWhenFull(t *testing.T) {
	c := newNonceCache(2)
	expiry := testNow.Add(time.Minute)
	if !c.add("a", expiry, testNow) || !c.add("b", expiry, testNow) {
		t.Fatal("first nonces must be accepted")
	}
	if c.add("c", expiry, testNow) {
		t.Error("a full cache of live nonces must refuse new ones")
	}
	if !c.add("c", expiry, expiry.Add(time.Second)) {
		t.Error("expired nonces must be pruned to make room")
	}
}

func serveBody(h *Handler, req *http.Request) (int, string) {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code, strings.TrimSpace(rr.Body.String())
}

func TestEventResponseReportsLinkStatus(t *testing.T) {
	h, _ := newTestHandler()
	code, body := serveBody(h, signedRequest(playingBody, nonceA, testNow, testSecret))
	if code != http.StatusOK || body != `{"linked":true}` {
		t.Errorf("linked user: %d %s", code, body)
	}

	unlinked := strings.Replace(playingBody, "alice", "bob", 1)
	code, body = serveBody(h, signedRequest(unlinked, nonceB, testNow, testSecret))
	if code != http.StatusOK || body != `{"linked":false}` {
		t.Errorf("unlinked user: %d %s", code, body)
	}
}

func TestLinkedQuery(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		secret   []byte
		wantCode int
		wantBody string
	}{
		{"linked", `{"v":1,"user":"alice"}`, testSecret, http.StatusOK, `{"linked":true}`},
		{"not linked", `{"v":1,"user":"bob"}`, testSecret, http.StatusOK, `{"linked":false}`},
		{"unsigned", `{"v":1,"user":"alice"}`, []byte("another-secret-another-secret-xx"), http.StatusUnauthorized, ""},
		{"unknown field", `{"v":1,"user":"alice","x":1}`, testSecret, http.StatusBadRequest, ""},
		{"no user", `{"v":1}`, testSecret, http.StatusBadRequest, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandler()
			code, body := serveBody(h, signedRequestTo(LinkedPath, tt.body, nonceA, testNow, tt.secret))
			if code != tt.wantCode || (tt.wantBody != "" && body != tt.wantBody) {
				t.Errorf("got %d %q, want %d %q", code, body, tt.wantCode, tt.wantBody)
			}
		})
	}
}
