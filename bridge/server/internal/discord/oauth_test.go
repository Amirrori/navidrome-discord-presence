package discord

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := NewClient("123", "secret", "https://presence.example.com/callback")
	c.BaseURL = server.URL
	return c
}

func TestAuthorizeURL(t *testing.T) {
	c := NewClient("123", "secret", "https://presence.example.com/callback")
	u, err := url.Parse(c.AuthorizeURL("state-1", "challenge-1"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	want := map[string]string{
		"response_type":         "code",
		"client_id":             "123",
		"scope":                 Scopes,
		"redirect_uri":          "https://presence.example.com/callback",
		"state":                 "state-1",
		"code_challenge":        "challenge-1",
		"code_challenge_method": "S256",
	}
	for key, value := range want {
		if q.Get(key) != value {
			t.Errorf("%s = %q, want %q", key, q.Get(key), value)
		}
	}
}

func TestExchangeSendsConfidentialRequest(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "123" || pass != "secret" {
			t.Errorf("missing client credentials")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("code") != "the-code" || r.PostForm.Get("code_verifier") != "the-verifier" {
			t.Errorf("unexpected form %v", r.PostForm)
		}
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":604800,"scope":"openid sdk.social_layer_presence"}`))
	})

	token, err := c.Exchange(context.Background(), "the-code", "the-verifier")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "at" || token.RefreshToken != "rt" || token.ExpiresAt.IsZero() {
		t.Errorf("got %+v", token)
	}
}

func TestTokenWithoutPresenceScopeIsRejected(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":60,"scope":"openid"}`))
	})
	if _, err := c.Exchange(context.Background(), "code", "verifier"); err == nil {
		t.Error("a token without the presence scope must be rejected")
	}
}

func TestRefreshInvalidGrant(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	if _, err := c.Refresh(context.Background(), "old"); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("want ErrInvalidGrant, got %v", err)
	}
}

func TestRefreshKeepsTokenWhenNotRotated(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at2","expires_in":60,"scope":"openid sdk.social_layer_presence"}`))
	})
	token, err := c.Refresh(context.Background(), "keep-me")
	if err != nil {
		t.Fatal(err)
	}
	if token.RefreshToken != "keep-me" {
		t.Errorf("refresh token = %q", token.RefreshToken)
	}
}

func TestRevoke(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != revokePath {
			t.Errorf("path = %s", r.URL.Path)
		}
		r.ParseForm()
		if r.PostForm.Get("token") != "rt" || r.PostForm.Get("token_type_hint") != "refresh_token" {
			t.Errorf("unexpected form %v", r.PostForm)
		}
	})
	if err := c.Revoke(context.Background(), "rt"); err != nil {
		t.Fatal(err)
	}
}

func TestUserInfo(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at" {
			t.Errorf("missing bearer token")
		}
		w.Write([]byte(`{"sub":"42","preferred_username":"alice"}`))
	})
	user, err := c.UserInfo(context.Background(), "at")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != "42" || user.Name != "alice" {
		t.Errorf("got %+v", user)
	}
}

func TestNewPKCE(t *testing.T) {
	verifier, challenge, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Error("challenge is not the S256 of the verifier")
	}
	if len(verifier) < 43 || strings.ContainsAny(verifier, "+/=") {
		t.Errorf("verifier %q is not a valid PKCE verifier", verifier)
	}
}

func TestRefreshRetriesAfterShortRateLimit(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0.05")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":60,"scope":"openid sdk.social_layer_presence"}`))
	})
	token, err := c.Refresh(context.Background(), "old")
	if err != nil || token.AccessToken != "at" || calls != 2 {
		t.Errorf("token=%+v err=%v calls=%d, want a retry that succeeds", token, err, calls)
	}
}

func TestRefreshGivesUpOnLongRateLimit(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err := c.Refresh(context.Background(), "old"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("want ErrRateLimited, got %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{"2": 2 * time.Second, "0.5": 500 * time.Millisecond, "": time.Second, "soon": time.Second}
	for in, want := range cases {
		if got := retryAfter(in); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
