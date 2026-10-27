package config

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func validEnv() map[string]string {
	return map[string]string{
		"NDS_PUBLIC_URL":                 "https://presence.example.com",
		"NDS_NAVIDROME_URL":              "http://192.168.1.10:4533",
		"NDS_DISCORD_APP_ID":             "1234567890123456789",
		"NDS_DISCORD_CLIENT_SECRET_FILE": "/run/secrets/client",
		"NDS_EVENT_SECRET_FILE":          "/run/secrets/events",
		"NDS_WORKER_PATH":                "/usr/lib/nds/nds-worker",
		"NDS_TRUSTED_PROXIES":            "192.168.1.20, ::1",
	}
}

func fakeFiles(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		content, ok := files[path]
		if !ok {
			return nil, errors.New("no such file")
		}
		return []byte(content), nil
	}
}

var validFiles = map[string]string{
	"/run/secrets/client": "client-secret\n",
	"/run/secrets/events": strings.Repeat("s", 40) + "\n",
}

func TestLoadValid(t *testing.T) {
	env := validEnv()
	cfg, err := load(func(k string) string { return env[k] }, fakeFiles(validFiles))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.DiscordClientSecret != "client-secret" {
		t.Errorf("client secret not trimmed: %q", cfg.DiscordClientSecret)
	}
	if got := cfg.RedirectURL(); got != "https://presence.example.com/callback" {
		t.Errorf("redirect URL = %q", got)
	}
	if !cfg.SecureCookies() {
		t.Error("https public URL must enable secure cookies")
	}
	if len(cfg.TrustedProxies) != 2 {
		t.Errorf("trusted proxies = %v", cfg.TrustedProxies)
	}
	if cfg.EventsAddr != defaultEventsAddr || cfg.WebAddr != defaultWebAddr {
		t.Errorf("listen defaults not applied: %q %q", cfg.EventsAddr, cfg.WebAddr)
	}
	if len(cfg.ImageHosts) != len(defaultImageHosts) {
		t.Errorf("image hosts default not applied: %v", cfg.ImageHosts)
	}
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		files   map[string]string
		wantErr string
	}{
		{name: "missing public URL", key: "NDS_PUBLIC_URL", value: "", wantErr: "NDS_PUBLIC_URL is required"},
		{name: "non-http navidrome URL", key: "NDS_NAVIDROME_URL", value: "ftp://x", wantErr: "NDS_NAVIDROME_URL must be"},
		{name: "non-numeric app ID", key: "NDS_DISCORD_APP_ID", value: "abc", wantErr: "numeric application ID"},
		{name: "bad proxy", key: "NDS_TRUSTED_PROXIES", value: "proxy.local", wantErr: "not an IP address"},
		{name: "missing worker", key: "NDS_WORKER_PATH", value: "", wantErr: "NDS_WORKER_PATH is required"},
		{
			name: "short event secret", key: "NDS_EVENT_SECRET_FILE", value: "/run/secrets/events",
			files:   map[string]string{"/run/secrets/client": "x", "/run/secrets/events": "short"},
			wantErr: "at least 32 characters",
		},
		{name: "unreadable secret", key: "NDS_DISCORD_CLIENT_SECRET_FILE", value: "/nope", wantErr: "no such file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value
			files := validFiles
			if tt.files != nil {
				files = tt.files
			}

			_, err := load(func(k string) string { return env[k] }, fakeFiles(files))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestListWithDefaultTrimsEntries(t *testing.T) {
	got := listWithDefault(" a.example , ,b.example", nil)
	if len(got) != 2 || got[0] != "a.example" || got[1] != "b.example" {
		t.Errorf("got %v", got)
	}
}

func TestRetentionAndLogLevel(t *testing.T) {
	env := validEnv()
	env["NDS_LINK_RETENTION_DAYS"] = "30"
	env["NDS_LOG_LEVEL"] = "debug"
	cfg, err := load(func(k string) string { return env[k] }, fakeFiles(validFiles))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LinkRetention != 30*24*time.Hour || cfg.LogRetentionDays != defaultLogRetentionDays {
		t.Errorf("retention = %v / %d days", cfg.LinkRetention, cfg.LogRetentionDays)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("log level = %v", cfg.LogLevel)
	}

	env["NDS_LINK_RETENTION_DAYS"] = "-1"
	if _, err := load(func(k string) string { return env[k] }, fakeFiles(validFiles)); err == nil {
		t.Error("negative retention must be rejected")
	}
}

func TestWarnings(t *testing.T) {
	env := validEnv() // navidrome over plain http on the LAN, no privacy contact
	cfg, err := load(func(k string) string { return env[k] }, fakeFiles(validFiles))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(cfg.Warnings()); got != 2 {
		t.Errorf("warnings = %q, want the plain-http and privacy-contact warnings", cfg.Warnings())
	}

	env["NDS_NAVIDROME_URL"] = "https://music.example.com"
	env["NDS_PRIVACY_CONTACT"] = "admin@example.com"
	cfg, _ = load(func(k string) string { return env[k] }, fakeFiles(validFiles))
	if w := cfg.Warnings(); len(w) != 0 {
		t.Errorf("unexpected warnings %q", w)
	}
}

func TestSecretsFromSystemdCredentials(t *testing.T) {
	env := validEnv()
	delete(env, "NDS_DISCORD_CLIENT_SECRET_FILE")
	delete(env, "NDS_EVENT_SECRET_FILE")
	env["CREDENTIALS_DIRECTORY"] = "/run/credentials/nds-bridge.service"
	files := map[string]string{
		"/run/credentials/nds-bridge.service/discord-client-secret": "from-credential",
		"/run/credentials/nds-bridge.service/event-secret":          strings.Repeat("e", 40),
	}

	cfg, err := load(func(k string) string { return env[k] }, fakeFiles(files))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DiscordClientSecret != "from-credential" || len(cfg.EventSecret) != 40 {
		t.Errorf("secrets not read from the credentials directory: %q", cfg.DiscordClientSecret)
	}
}
