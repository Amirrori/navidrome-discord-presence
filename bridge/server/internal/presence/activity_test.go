package presence

import (
	"strings"
	"testing"
	"unicode/utf8"

	"navidrome-discord-status/bridge/internal/events"
)

var testOptions = DisplayOptions{
	DefaultImageURL: "https://img.example/default.png",
	PauseImageURL:   "https://img.example/pause.png",
	ImageHosts:      []string{"coverartarchive.org", "*.dzcdn.net"},
	LinkHosts:       []string{"open.spotify.com"},
}

func TestBuildActivityPlaying(t *testing.T) {
	track := events.Track{
		Title: "Undone", Artist: "Weezer", Album: "Weezer",
		StartMs: 1000, EndMs: 5000,
		CoverURL: "https://cdn-images.dzcdn.net/cover.jpg",
		TrackURL: "https://open.spotify.com/track/abc",
	}
	a := BuildActivity(track, false, testOptions)

	if a.Details != "Undone" || a.State != "Weezer" || a.LargeText != "Weezer" {
		t.Errorf("text: %+v", a)
	}
	if a.LargeImage != track.CoverURL || a.LargeURL != track.TrackURL || a.DetailsURL != track.TrackURL {
		t.Errorf("urls: %+v", a)
	}
	if a.StartMs != 1000 || a.EndMs != 5000 || a.SmallImage != "" {
		t.Errorf("timing/pause: %+v", a)
	}
}

func TestBuildActivityPaused(t *testing.T) {
	a := BuildActivity(events.Track{Title: "Undone", Artist: "Weezer", StartMs: 1000, EndMs: 5000}, true, testOptions)
	if a.EndMs != 0 || a.SmallImage != testOptions.PauseImageURL || a.SmallText != pausedText {
		t.Errorf("paused activity: %+v", a)
	}
}

func TestBuildActivityRejectsForeignURLs(t *testing.T) {
	track := events.Track{
		Title: "x", Artist: "y",
		CoverURL:  "https://evil.example/cover.jpg",
		TrackURL:  "https://open.spotify.com.evil.example/track",
		ArtistURL: "javascript:alert(1)",
	}
	a := BuildActivity(track, false, testOptions)
	if a.LargeImage != testOptions.DefaultImageURL {
		t.Errorf("foreign cover kept: %q", a.LargeImage)
	}
	if a.LargeURL != "" || a.DetailsURL != "" || a.StateURL != "" {
		t.Errorf("foreign links kept: %+v", a)
	}
}

func TestAllowedURL(t *testing.T) {
	hosts := []string{"coverartarchive.org", "*.dzcdn.net"}
	tests := map[string]bool{
		"https://coverartarchive.org/release/x":  true,
		"https://cdn-images.dzcdn.net/a.jpg":     true,
		"https://dzcdn.net/a.jpg":                false, // wildcard needs a subdomain
		"http://coverartarchive.org/x":           false,
		"https://user@coverartarchive.org/x":     false,
		"https://coverartarchive.org:8443/x":     false,
		"https://coverartarchive.org.evil.com/x": false,
		"https://" + strings.Repeat("a", 300):    false,
		"":                                       false,
		"https://COVERARTARCHIVE.org/upper-case": true,
	}
	for raw, want := range tests {
		if got := allowedURL(raw, hosts) != ""; got != want {
			t.Errorf("allowedURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestCleanText(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  Hello \n\t World  ", "Hello World"},
		{"evil\u202Etxt.exe", "eviltxt.exe"}, // right-to-left override removed
		{"zero\u200Bwidth", "zerowidth"},     // zero-width space removed
		{"bell\x07", "bell"},                 // control character removed
		{"ペルソナ3", "ペルソナ3"},                   // non-Latin text kept
		{"A", "A\u2800"},                     // padded to Discord's minimum
	}
	for _, tt := range tests {
		if got := cleanText(tt.in, "fallback"); got != tt.want {
			t.Errorf("cleanText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := cleanText("\u200B", "Unknown title"); got != "Unknown title" {
		t.Errorf("invisible-only text must fall back, got %q", got)
	}
	if got := cleanText("", ""); got != "" {
		t.Errorf("empty without fallback = %q", got)
	}
}

func TestCleanTextTruncates(t *testing.T) {
	got := cleanText(strings.Repeat("é", 300), "")
	if n := utf8.RuneCountInString(got); n != maxTextRunes {
		t.Errorf("length = %d runes, want %d", n, maxTextRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("truncated text must end with an ellipsis")
	}
}
