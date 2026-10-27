package navidrome

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func newTestAuthenticator(t *testing.T) *Authenticator {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != loginPath {
			http.NotFound(w, r)
			return
		}
		var creds map[string]string
		if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case creds["username"] == "Alice" && creds["password"] == "right":
			w.Write([]byte(`{"username":"alice","name":"Alice","token":"jwt"}`))
		case creds["username"] == "broken":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	return NewAuthenticator(base)
}

func TestVerifyReturnsCanonicalUsername(t *testing.T) {
	a := newTestAuthenticator(t)
	username, err := a.Verify(context.Background(), "Alice", "right")
	if err != nil {
		t.Fatal(err)
	}
	if username != "alice" {
		t.Errorf("username = %q, want the canonical %q", username, "alice")
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	a := newTestAuthenticator(t)
	if _, err := a.Verify(context.Background(), "Alice", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("want ErrInvalidCredentials, got %v", err)
	}
}

func TestVerifyReportsServerErrors(t *testing.T) {
	a := newTestAuthenticator(t)
	_, err := a.Verify(context.Background(), "broken", "x")
	if err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a server error must not look like bad credentials, got %v", err)
	}
}
