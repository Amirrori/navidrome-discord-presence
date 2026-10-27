package retention

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"navidrome-discord-status/bridge/internal/store"
)

type recorder struct {
	revoked   []string
	forgotten []string
}

func (r *recorder) Revoke(_ context.Context, token string) error {
	r.revoked = append(r.revoked, token)
	return nil
}

func (r *recorder) Forget(username string) {
	r.forgotten = append(r.forgotten, username)
}

func TestSweepRemovesOnlyUnusedLinks(t *testing.T) {
	links, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer links.Close()

	now := time.Unix(1_800_000_000, 0).UTC()
	old := now.Add(-400 * 24 * time.Hour)
	if err := links.Put("stale", store.Link{DiscordID: "1", RefreshToken: "stale-token", LinkedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := links.Put("active", store.Link{DiscordID: "2", RefreshToken: "active-token", LinkedAt: old, LastUsed: now}); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	sweeper := &Sweeper{
		Links:     links,
		Revoker:   rec,
		Forgetter: rec,
		Retention: 365 * 24 * time.Hour,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if removed := sweeper.Sweep(context.Background(), now); removed != 1 {
		t.Fatalf("removed %d links, want 1", removed)
	}
	if len(rec.revoked) != 1 || rec.revoked[0] != "stale-token" {
		t.Errorf("revoked = %v", rec.revoked)
	}
	if len(rec.forgotten) != 1 || rec.forgotten[0] != "stale" {
		t.Errorf("forgotten = %v", rec.forgotten)
	}
	if _, err := links.Get("active"); err != nil {
		t.Error("an active link must be kept")
	}
}

func TestRunIsDisabledWithoutRetention(t *testing.T) {
	sweeper := &Sweeper{Retention: 0}
	done := make(chan struct{})
	go func() {
		sweeper.Run(context.Background(), time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run must return at once when retention is disabled")
	}
}
