// Package navidrome verifies Navidrome credentials, so the link page knows
// which Navidrome user is linking a Discord account.
package navidrome

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	loginPath       = "/auth/login"
	maxResponseSize = 64 << 10
)

// ErrInvalidCredentials means Navidrome rejected the username or password.
var ErrInvalidCredentials = errors.New("invalid username or password")

type Authenticator struct {
	baseURL    *url.URL
	httpClient *http.Client
}

func NewAuthenticator(baseURL *url.URL) *Authenticator {
	return &Authenticator{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Verify checks the credentials and returns the canonical username as stored
// in Navidrome. The password is only forwarded to Navidrome, never kept.
func (a *Authenticator) Verify(ctx context.Context, username, password string) (string, error) {
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return "", err
	}
	endpoint := a.baseURL.JoinPath(loginPath).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("navidrome login: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", ErrInvalidCredentials
	default:
		return "", fmt.Errorf("navidrome login: HTTP %d", resp.StatusCode)
	}

	var account struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&account); err != nil {
		return "", fmt.Errorf("navidrome login response: %w", err)
	}
	if account.Username == "" {
		return "", errors.New("navidrome login response: missing username")
	}
	return account.Username, nil
}
