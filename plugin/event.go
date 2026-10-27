package main

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

const eventVersion = 1

type playbackEvent struct {
	Version  int         `json:"v"`
	Username string      `json:"user"`
	State    string      `json:"state"`
	Track    *eventTrack `json:"track,omitempty"`
}

type eventTrack struct {
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Album     string `json:"album"`
	StartMs   int64  `json:"startMs"`
	EndMs     int64  `json:"endMs,omitempty"`
	CoverURL  string `json:"coverUrl,omitempty"`
	TrackURL  string `json:"trackUrl,omitempty"`
	ArtistURL string `json:"artistUrl,omitempty"`
}

// buildPlaybackEvent builds a playing or paused event, including the wall-clock
// start and end times of the track and its resolved artwork and links.
func buildPlaybackEvent(input scrobbler.PlaybackReportRequest) playbackEvent {
	state := statePlaying
	if input.State == statePaused {
		state = statePaused
	}

	trackURL, artistURL := resolveSpotifyLinks(input.Track)
	track := &eventTrack{
		Title:     input.Track.Title,
		Artist:    input.Track.Artist,
		Album:     input.Track.Album,
		CoverURL:  getImageURL(input.Username, input.Track),
		TrackURL:  trackURL,
		ArtistURL: artistURL,
	}
	track.StartMs, track.EndMs = playbackWindow(input, state == statePaused)

	return playbackEvent{
		Version:  eventVersion,
		Username: input.Username,
		State:    state,
		Track:    track,
	}
}

// buildStoppedEvent builds an event that clears the user's status.
func buildStoppedEvent(input scrobbler.PlaybackReportRequest) playbackEvent {
	return playbackEvent{
		Version:  eventVersion,
		Username: input.Username,
		State:    stateStopped,
	}
}

// playbackWindow converts the reported track position into wall-clock start and
// end times in milliseconds, accounting for the playback rate. While paused only
// the start (the moment of pausing) is set, so Discord shows "paused for".
func playbackWindow(input scrobbler.PlaybackReportRequest, paused bool) (startMs, endMs int64) {
	reportedAtMs := input.Timestamp * 1000
	if paused {
		return reportedAtMs, 0
	}

	rate := input.PlaybackRate
	if rate <= 0 {
		rate = 1.0
	}
	elapsedMs := int64(float64(input.PositionMs) / rate)
	durationMs := int64(float64(input.Track.Duration) * 1000 / rate)

	startMs = reportedAtMs - elapsedMs
	return startMs, startMs + durationMs
}

func resolveSpotifyLinks(track scrobbler.TrackInfo) (trackURL, artistURL string) {
	if enabled, _ := pdk.GetConfig(spotifyLinksKey); enabled != "true" {
		return "", ""
	}
	return resolveSpotifyURL(track), spotifySearchURL(track.Artist)
}
