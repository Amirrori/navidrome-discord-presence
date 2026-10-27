// Package discord implements the OAuth2 calls the bridge needs, as a
// confidential client: the client secret never leaves the server.
package discord

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Scopes are the only OAuth2 scopes the bridge requests: enough to set a
// user's presence, without access to messages.
const Scopes = "openid sdk.social_layer_presence"

const (
	DefaultBaseURL = "https://discord.com"

	authorizePath = "/api/oauth2/authorize"
	tokenPath     = "/api/oauth2/token"
	revokePath    = "/api/oauth2/token/revoke"
	userInfoPath  = "/api/oauth2/userinfo"

	maxResponseSize = 64 << 10

	// maxRetryWait is the longest Retry-After we wait for before retrying once.
	maxRetryWait = 10 * time.Second
)

// ErrInvalidGrant means Discord no longer accepts the grant: the user revoked
// access or the refresh token expired. The link should be removed.
var ErrInvalidGrant = errors.New("discord rejected the grant")

// ErrRateLimited means Discord asked us to slow down for longer than we wait.
var ErrRateLimited = errors.New("discord rate limited the request")

type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// User identifies the Discord account behind a token.
type User struct {
	ID   string
	Name string
}

// Client talks to Discord's OAuth2 endpoints.
type Client struct {
	AppID        string
	ClientSecret string
	RedirectURL  string
	BaseURL      string
	HTTPClient   *http.Client
}

func NewClient(appID, clientSecret, redirectURL string) *Client {
	return &Client{
		AppID:        appID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		BaseURL:      DefaultBaseURL,
		HTTPClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) AuthorizeURL(state, codeChallenge string) string {
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.AppID},
		"scope":                 {Scopes},
		"redirect_uri":          {c.RedirectURL},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	return c.BaseURL + authorizePath + "?" + query.Encode()
}

// Exchange trades an authorization code for tokens.
func (c *Client) Exchange(ctx context.Context, code, codeVerifier string) (Token, error) {
	return c.requestToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.RedirectURL},
		"code_verifier": {codeVerifier},
	})
}

// Refresh obtains a new access token. Callers must store the returned
// refresh token, since Discord may rotate it.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	token, err := c.requestToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	})
	if err == nil && token.RefreshToken == "" {
		token.RefreshToken = refreshToken
	}
	return token, err
}

// Revoke invalidates a refresh token and every token of the same grant.
func (c *Client) Revoke(ctx context.Context, refreshToken string) error {
	resp, err := c.postForm(ctx, revokePath, url.Values{
		"token":           {refreshToken},
		"token_type_hint": {"refresh_token"},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("revoke: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) UserInfo(ctx context.Context, accessToken string) (User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+userInfoPath, nil)
	if err != nil {
		return User{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return User{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return User{}, fmt.Errorf("userinfo: HTTP %d", resp.StatusCode)
	}

	var info struct {
		Sub               string `json:"sub"`
		PreferredUsername string `json:"preferred_username"`
		Nickname          string `json:"nickname"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&info); err != nil {
		return User{}, fmt.Errorf("userinfo: %w", err)
	}
	if info.Sub == "" {
		return User{}, errors.New("userinfo: missing subject")
	}
	name := info.PreferredUsername
	if name == "" {
		name = info.Nickname
	}
	return User{ID: info.Sub, Name: name}, nil
}

// requestToken calls the token endpoint. When Discord rate limits the call and
// asks for a short wait, it waits and tries once more.
func (c *Client) requestToken(ctx context.Context, form url.Values) (Token, error) {
	resp, err := c.postForm(ctx, tokenPath, form)
	if err != nil {
		return Token{}, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		wait := retryAfter(resp.Header.Get("Retry-After"))
		resp.Body.Close()
		if wait > maxRetryWait {
			return Token{}, fmt.Errorf("%w (retry after %s)", ErrRateLimited, wait)
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return Token{}, ctx.Err()
		}
		if resp, err = c.postForm(ctx, tokenPath, form); err != nil {
			return Token{}, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			return Token{}, ErrRateLimited
		}
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, maxResponseSize)

	if resp.StatusCode != http.StatusOK {
		var oauthErr struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(body).Decode(&oauthErr)
		if oauthErr.Error == "invalid_grant" {
			return Token{}, ErrInvalidGrant
		}
		return Token{}, fmt.Errorf("token request: HTTP %d %s", resp.StatusCode, oauthErr.Error)
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return Token{}, fmt.Errorf("token response: %w", err)
	}
	if payload.AccessToken == "" {
		return Token{}, errors.New("token response: missing access token")
	}
	if !hasScope(payload.Scope, "sdk.social_layer_presence") {
		return Token{}, fmt.Errorf("token response: presence scope not granted (got %q)", payload.Scope)
	}
	return Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

func (c *Client) postForm(ctx context.Context, path string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.AppID, c.ClientSecret)
	return c.HTTPClient.Do(req)
}

func hasScope(scopes, want string) bool {
	for _, scope := range strings.Fields(scopes) {
		if scope == want {
			return true
		}
	}
	return false
}

// NewPKCE returns a random code verifier and its S256 challenge.
func NewPKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// retryAfter reads a Retry-After header in seconds (Discord may send a
// fraction). Missing or unreadable values mean one second.
func retryAfter(value string) time.Duration {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return time.Second
	}
	return time.Duration(seconds * float64(time.Second))
}
