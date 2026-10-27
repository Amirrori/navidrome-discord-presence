package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

const (
	eventsPath = "/v1/events"
	linkedPath = "/v1/linked"

	// Request signing headers. The signature covers timestamp, nonce and body,
	// so the bridge can reject forged and replayed requests.
	headerTimestamp = "X-NDS-Timestamp"
	headerNonce     = "X-NDS-Nonce"
	headerSignature = "X-NDS-Signature"

	signatureScheme = "v1="
	minSecretLength = 32
	nonceBytes      = 16

	bridgeTimeoutMs = 3000
)

type bridgeConfig struct {
	baseURL string
	secret  []byte
}

type linkStatus struct {
	Linked bool `json:"linked"`
}

// loadBridgeConfig reads and validates the bridge settings. It returns false
// (and logs why) when the plugin is not configured yet.
func loadBridgeConfig() (bridgeConfig, bool) {
	rawURL, _ := pdk.GetConfig(bridgeURLKey)
	secret, _ := pdk.GetConfig(bridgeSecretKey)

	base, err := bridgeBaseURL(rawURL)
	if err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("Bridge URL is not usable: %v", err))
		return bridgeConfig{}, false
	}
	if len(secret) < minSecretLength {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("Bridge secret must be at least %d characters", minSecretLength))
		return bridgeConfig{}, false
	}
	return bridgeConfig{baseURL: base, secret: []byte(secret)}, true
}

// bridgeBaseURL validates the configured bridge URL and returns it without a
// trailing slash.
func bridgeBaseURL(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("not configured")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("must not contain credentials, query or fragment")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// sendEvent posts a playback event and returns whether the user is linked.
func sendEvent(cfg bridgeConfig, event playbackEvent) (bool, error) {
	return post(cfg, eventsPath, event)
}

func queryLinked(cfg bridgeConfig, username string) (bool, error) {
	return post(cfg, linkedPath, struct {
		Version  int    `json:"v"`
		Username string `json:"user"`
	}{eventVersion, username})
}

func post(cfg bridgeConfig, path string, payload any) (bool, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("encoding request: %w", err)
	}
	nonce, err := newNonce()
	if err != nil {
		return false, fmt.Errorf("generating nonce: %w", err)
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	resp, err := host.HTTPSend(host.HTTPRequest{
		Method: "POST",
		URL:    cfg.baseURL + path,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			headerTimestamp: timestamp,
			headerNonce:     nonce,
			headerSignature: signatureScheme + sign(cfg.secret, timestamp, nonce, body),
		},
		Body:              body,
		TimeoutMs:         bridgeTimeoutMs,
		NoFollowRedirects: true,
	})
	if err != nil {
		return false, err
	}
	if resp.StatusCode != 200 {
		return false, fmt.Errorf("bridge answered HTTP %d", resp.StatusCode)
	}
	var status linkStatus
	if err := json.Unmarshal(resp.Body, &status); err != nil {
		return false, fmt.Errorf("reading bridge answer: %w", err)
	}
	return status.Linked, nil
}

// sign returns the hex-encoded HMAC-SHA256 of "timestamp.nonce.body".
func sign(secret []byte, timestamp, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write([]byte(nonce))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func newNonce() (string, error) {
	b := make([]byte, nonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
