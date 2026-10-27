// Command nds-test-event sends one signed playback event to a bridge, the
// same way the Navidrome plugin does. Use it to check a bridge setup without
// Navidrome.
//
//	nds-test-event -url http://127.0.0.1:8090 -secret-file /path/to/secret -user alice -state playing
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"navidrome-discord-status/bridge/internal/events"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	bridgeURL := flag.String("url", "http://127.0.0.1:8090", "base URL of the bridge's events listener")
	secretFile := flag.String("secret-file", "", "file containing the shared event secret")
	user := flag.String("user", "", "Navidrome username")
	state := flag.String("state", events.StatePlaying, "playing, paused or stopped")
	title := flag.String("title", "Test Track", "track title")
	artist := flag.String("artist", "Test Artist", "track artist")
	album := flag.String("album", "Test Album", "track album")
	cover := flag.String("cover", "", "public cover image URL")
	length := flag.Duration("length", 3*time.Minute, "track length")
	flag.Parse()

	if *secretFile == "" || *user == "" {
		flag.Usage()
		return fmt.Errorf("-secret-file and -user are required")
	}
	secret, err := os.ReadFile(*secretFile)
	if err != nil {
		return err
	}

	event := events.Event{Version: 1, User: *user, State: *state}
	if *state != events.StateStopped {
		now := time.Now()
		event.Track = &events.Track{
			Title:    *title,
			Artist:   *artist,
			Album:    *album,
			StartMs:  now.UnixMilli(),
			CoverURL: *cover,
		}
		if *state == events.StatePlaying {
			event.Track.EndMs = now.Add(*length).UnixMilli()
		}
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}

	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	nonce := hex.EncodeToString(nonceBytes)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signature := events.Sign([]byte(strings.TrimSpace(string(secret))), timestamp, nonce, body)

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(*bridgeURL, "/")+events.Path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(events.HeaderTimestamp, timestamp)
	req.Header.Set(events.HeaderNonce, nonce)
	req.Header.Set(events.HeaderSignature, "v1="+hex.EncodeToString(signature))

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bridge answered HTTP %d", resp.StatusCode)
	}
	var status struct {
		Linked bool `json:"linked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return fmt.Errorf("reading the answer: %w", err)
	}
	if !status.Linked {
		fmt.Printf("sent %s event, but %s has not linked a Discord account\n", *state, *user)
		return nil
	}
	fmt.Printf("sent %s event for %s\n", *state, *user)
	return nil
}
