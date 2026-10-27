package presence

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"navidrome-discord-status/bridge/internal/events"
)

// Discord field limits for rich presence.
const (
	maxTextRunes = 128
	minTextRunes = 2
	maxURLLength = 256

	// padRune lengthens one-character text to Discord's two-character minimum
	// without showing anything (Braille pattern blank).
	padRune = '\u2800'

	unknownTitle  = "Unknown title"
	unknownArtist = "Unknown artist"
	pausedText    = "Paused"
)

type Activity struct {
	Details    string `json:"details"`
	DetailsURL string `json:"detailsUrl,omitempty"`
	State      string `json:"state"`
	StateURL   string `json:"stateUrl,omitempty"`
	LargeImage string `json:"largeImage,omitempty"`
	LargeText  string `json:"largeText,omitempty"`
	LargeURL   string `json:"largeUrl,omitempty"`
	SmallImage string `json:"smallImage,omitempty"`
	SmallText  string `json:"smallText,omitempty"`
	StartMs    int64  `json:"startMs,omitempty"`
	EndMs      int64  `json:"endMs,omitempty"`
}

// DisplayOptions controls images and which hosts links may point to.
type DisplayOptions struct {
	DefaultImageURL string
	PauseImageURL   string
	ImageHosts      []string
	LinkHosts       []string
}

// BuildActivity turns an untrusted track description into a safe activity:
// text is cleaned and shortened to Discord's limits, and images and links
// are only kept when they are https URLs on an allowed host.
func BuildActivity(track events.Track, paused bool, opts DisplayOptions) Activity {
	activity := Activity{
		Details:    cleanText(track.Title, unknownTitle),
		State:      cleanText(track.Artist, unknownArtist),
		LargeImage: opts.DefaultImageURL,
		DetailsURL: allowedURL(track.TrackURL, opts.LinkHosts),
		StateURL:   allowedURL(track.ArtistURL, opts.LinkHosts),
		LargeURL:   allowedURL(track.TrackURL, opts.LinkHosts),
		StartMs:    max(track.StartMs, 0),
	}
	if album := cleanText(track.Album, ""); album != "" {
		activity.LargeText = album
	}
	if cover := allowedURL(track.CoverURL, opts.ImageHosts); cover != "" {
		activity.LargeImage = cover
	}

	if paused {
		if opts.PauseImageURL != "" {
			activity.SmallImage = opts.PauseImageURL
			activity.SmallText = pausedText
		}
	} else if track.EndMs > activity.StartMs {
		activity.EndMs = track.EndMs
	}
	return activity
}

// cleanText removes control and invisible formatting characters (which could
// hide or reorder text), collapses whitespace and enforces Discord's length
// limits. Empty results become fallback.
func cleanText(s, fallback string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == utf8.RuneError:
			// dropped
		default:
			if space {
				b.WriteRune(' ')
				space = false
			}
			b.WriteRune(r)
		}
	}

	text := b.String()
	if text == "" {
		if fallback == "" {
			return ""
		}
		text = fallback
	}

	runes := []rune(text)
	if len(runes) > maxTextRunes {
		runes = append(runes[:maxTextRunes-1], '…')
	}
	for len(runes) < minTextRunes {
		runes = append(runes, padRune)
	}
	return string(runes)
}

// allowedURL returns raw if it is an https URL without credentials on one of
// the allowed hosts, and "" otherwise. "*.example.com" matches subdomains.
func allowedURL(raw string, hosts []string) string {
	if raw == "" || len(raw) > maxURLLength {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.Port() != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	for _, pattern := range hosts {
		pattern = strings.ToLower(pattern)
		if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return raw
			}
		} else if host == pattern {
			return raw
		}
	}
	return ""
}
