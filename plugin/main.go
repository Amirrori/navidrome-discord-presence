// Navidrome Discord Status plugin
//
// Forwards each user's playback state to a navidrome-discord-status bridge,
// which shows it as a "Listening to" status on the user's linked Discord
// account through Discord's official Social SDK. The plugin never handles
// Discord credentials: users link their accounts on the bridge's own page.
//
// Capabilities: Scrobbler
//
// Based on the Discord Rich Presence plugin by the Navidrome Team
// (https://github.com/navidrome/discord-rich-presence-plugin), GPL-3.0.
package main

import (
	"fmt"

	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

// Configuration keys
const (
	bridgeURLKey     = "bridgeurl"
	bridgeSecretKey  = "bridgesecret"
	spotifyLinksKey  = "spotifylinks"
	caaEnabledKey    = "caaenabled"
	catalogLookupKey = "cataloglookup"
	uguuEnabledKey   = "uguuenabled"
)

// Playback states from PlaybackReportRequest.State
const (
	stateStarting = "starting"
	statePlaying  = "playing"
	statePaused   = "paused"
	stateStopped  = "stopped"
	stateExpired  = "expired"
)

type statusPlugin struct{}

func init() {
	scrobbler.Register(&statusPlugin{})
}

// IsAuthorized accepts only users who linked a Discord account on the bridge,
// so Navidrome doesn't even report the playback of anyone else.
func (p *statusPlugin) IsAuthorized(input scrobbler.IsAuthorizedRequest) (bool, error) {
	cfg, ok := loadBridgeConfig()
	if !ok {
		return false, nil
	}
	return isLinked(cfg, input.Username), nil
}

// NowPlaying is a no-op: playback state is handled by PlaybackReport.
func (p *statusPlugin) NowPlaying(_ scrobbler.NowPlayingRequest) error {
	return nil
}

// Scrobble is a no-op: a status needs no scrobbles.
func (p *statusPlugin) Scrobble(_ scrobbler.ScrobbleRequest) error {
	return nil
}

// PlaybackReport forwards playback state changes to the bridge.
func (p *statusPlugin) PlaybackReport(input scrobbler.PlaybackReportRequest) error {
	pdk.Log(pdk.LogDebug, fmt.Sprintf("PlaybackReport: state=%s user=%s track=%q pos=%dms rate=%.1f player=%s",
		input.State, input.Username, input.Track.Title, input.PositionMs, input.PlaybackRate, input.PlayerName))

	cfg, ok := loadBridgeConfig()
	if !ok || !isLinked(cfg, input.Username) {
		return nil
	}

	var event playbackEvent
	switch input.State {
	case stateStarting, statePlaying, statePaused:
		event = buildPlaybackEvent(input)
	case stateStopped, stateExpired:
		event = buildStoppedEvent(input)
	default:
		return nil
	}

	linked, err := sendEvent(cfg, event)
	if err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("Failed to send %s event for user %s: %v", event.State, input.Username, err))
	} else {
		rememberLinked(input.Username, linked)
	}
	// Never report an error to Navidrome: a missing status must not affect playback or retries.
	return nil
}

func main() {}
