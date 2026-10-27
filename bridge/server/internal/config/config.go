// Package config loads the bridge configuration from environment variables.
// Secrets are never passed as values: the environment only names the files
// that hold them, so they don't show up in process listings or unit files.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	minEventSecretLength = 32

	defaultEventsAddr = "127.0.0.1:8090"
	defaultWebAddr    = "127.0.0.1:8091"
	defaultStateDir   = "/var/lib/navidrome-discord-status"

	defaultImageURL = "https://raw.githubusercontent.com/navidrome/website/refs/heads/master/assets/icons/logo.webp"

	defaultLinkRetentionDays = 365
	defaultLogRetentionDays  = 14

	// Names of the systemd credentials used when no secret file is configured.
	clientSecretCredential = "discord-client-secret"
	eventSecretCredential  = "event-secret"
)

// defaultImageHosts are the hosts artwork may be loaded from. A leading "*."
// also matches all subdomains.
var defaultImageHosts = []string{
	"coverartarchive.org",
	"archive.org",
	"*.archive.org",
	"*.dzcdn.net",
	"*.mzstatic.com",
	"uguu.se",
	"*.uguu.se",
	"raw.githubusercontent.com",
}

// defaultLinkHosts are the hosts that clickable status links may point to.
var defaultLinkHosts = []string{
	"open.spotify.com",
}

type Config struct {
	EventsAddr string
	WebAddr    string
	PublicURL  *url.URL
	// TrustedProxies may set X-Forwarded-For; it is ignored from anyone else.
	TrustedProxies []net.IP

	NavidromeURL *url.URL

	DiscordAppID        string
	DiscordClientSecret string
	EventSecret         []byte

	StateDir   string
	WorkerPath string

	DefaultImageURL string
	PauseImageURL   string
	ImageHosts      []string
	LinkHosts       []string

	// LinkRetention removes links that were neither created nor used for
	// this long; 0 keeps them until the user unlinks.
	LinkRetention time.Duration
	// LogRetentionDays is shown in the privacy notice. The logs themselves are
	// kept by journald or the container runtime, which must be configured to match.
	LogRetentionDays int
	// PrivacyContact tells users how to reach the operator (shown in the notice).
	PrivacyContact string
	LogLevel       slog.Level
}

// Warnings returns configuration choices that work but weaken security.
func (c *Config) Warnings() []string {
	var warnings []string
	if c.NavidromeURL.Scheme == "http" && !isLoopback(c.NavidromeURL.Hostname()) {
		warnings = append(warnings, "NDS_NAVIDROME_URL uses plain http: passwords typed on the link page "+
			"are sent to Navidrome unencrypted")
	}
	if c.PublicURL.Scheme == "http" && !isLoopback(c.PublicURL.Hostname()) {
		warnings = append(warnings, "NDS_PUBLIC_URL uses plain http: sign-ins and sessions on the link page are not encrypted")
	}
	if c.PrivacyContact == "" {
		warnings = append(warnings, "NDS_PRIVACY_CONTACT is not set: the privacy notice has no contact")
	}
	return warnings
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RedirectURL is the OAuth callback URL registered with Discord.
func (c *Config) RedirectURL() string {
	return strings.TrimRight(c.PublicURL.String(), "/") + "/callback"
}

func (c *Config) SecureCookies() bool {
	return c.PublicURL.Scheme == "https"
}

func Load() (*Config, error) {
	return load(os.Getenv, os.ReadFile)
}

func load(getenv func(string) string, readFile func(string) ([]byte, error)) (*Config, error) {
	var errs []error
	fail := func(err error) { errs = append(errs, err) }

	cfg := &Config{
		EventsAddr:      withDefault(getenv("NDS_EVENTS_ADDR"), defaultEventsAddr),
		WebAddr:         withDefault(getenv("NDS_WEB_ADDR"), defaultWebAddr),
		StateDir:        withDefault(getenv("NDS_STATE_DIR"), defaultStateDir),
		WorkerPath:      getenv("NDS_WORKER_PATH"),
		DiscordAppID:    getenv("NDS_DISCORD_APP_ID"),
		DefaultImageURL: withDefault(getenv("NDS_DEFAULT_IMAGE_URL"), defaultImageURL),
		PauseImageURL:   getenv("NDS_PAUSE_IMAGE_URL"),
		ImageHosts:      listWithDefault(getenv("NDS_IMAGE_HOSTS"), defaultImageHosts),
		LinkHosts:       listWithDefault(getenv("NDS_LINK_HOSTS"), defaultLinkHosts),
	}

	cfg.PrivacyContact = getenv("NDS_PRIVACY_CONTACT")

	linkDays, err := daysWithDefault(getenv("NDS_LINK_RETENTION_DAYS"), defaultLinkRetentionDays)
	if err != nil {
		fail(fmt.Errorf("NDS_LINK_RETENTION_DAYS: %w", err))
	}
	cfg.LinkRetention = time.Duration(linkDays) * 24 * time.Hour
	if cfg.LogRetentionDays, err = daysWithDefault(getenv("NDS_LOG_RETENTION_DAYS"), defaultLogRetentionDays); err != nil {
		fail(fmt.Errorf("NDS_LOG_RETENTION_DAYS: %w", err))
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(withDefault(getenv("NDS_LOG_LEVEL"), "info"))); err != nil {
		fail(fmt.Errorf("NDS_LOG_LEVEL: %w", err))
	}

	if cfg.PublicURL, err = requireURL(getenv, "NDS_PUBLIC_URL"); err != nil {
		fail(err)
	}
	if cfg.NavidromeURL, err = requireURL(getenv, "NDS_NAVIDROME_URL"); err != nil {
		fail(err)
	}
	if cfg.TrustedProxies, err = parseIPs(getenv("NDS_TRUSTED_PROXIES")); err != nil {
		fail(fmt.Errorf("NDS_TRUSTED_PROXIES: %w", err))
	}
	if _, err := strconv.ParseUint(cfg.DiscordAppID, 10, 64); err != nil {
		fail(errors.New("NDS_DISCORD_APP_ID must be the numeric application ID"))
	}
	if cfg.WorkerPath == "" {
		fail(errors.New("NDS_WORKER_PATH is required"))
	}

	if secret, err := readSecret(getenv, readFile, "NDS_DISCORD_CLIENT_SECRET_FILE", clientSecretCredential); err != nil {
		fail(err)
	} else {
		cfg.DiscordClientSecret = secret
	}
	if secret, err := readSecret(getenv, readFile, "NDS_EVENT_SECRET_FILE", eventSecretCredential); err != nil {
		fail(err)
	} else if len(secret) < minEventSecretLength {
		fail(fmt.Errorf("the event secret must be at least %d characters", minEventSecretLength))
	} else {
		cfg.EventSecret = []byte(secret)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

func daysWithDefault(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	days, err := strconv.Atoi(value)
	if err != nil || days < 0 {
		return 0, errors.New("must be a whole number of days, 0 or more")
	}
	return days, nil
}

func withDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func listWithDefault(value string, fallback []string) []string {
	if value == "" {
		return fallback
	}
	var items []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func requireURL(getenv func(string) string, name string) (*url.URL, error) {
	raw := getenv(name)
	if raw == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%s must be an http or https URL", name)
	}
	return u, nil
}

func parseIPs(value string) ([]net.IP, error) {
	var ips []net.IP
	for _, item := range listWithDefault(value, nil) {
		ip := net.ParseIP(item)
		if ip == nil {
			return nil, fmt.Errorf("%q is not an IP address", item)
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

// readSecret reads a secret from the file named by the environment variable,
// trimming surrounding whitespace. Without the variable it falls back to the
// systemd credential of the given name ($CREDENTIALS_DIRECTORY/<credential>).
func readSecret(getenv func(string) string, readFile func(string) ([]byte, error), name, credential string) (string, error) {
	path := getenv(name)
	if path == "" {
		if dir := getenv("CREDENTIALS_DIRECTORY"); dir != "" {
			path = filepath.Join(dir, credential)
		}
	}
	if path == "" {
		return "", fmt.Errorf("%s is required (or a systemd credential named %q)", name, credential)
	}
	data, err := readFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("%s: file is empty", name)
	}
	return secret, nil
}
