// Package retention removes links that haven't been used for a long time, so
// data of people who stopped using the bridge isn't kept forever.
package retention

import (
	"context"
	"log/slog"
	"time"

	"navidrome-discord-status/bridge/internal/store"
)

type Links interface {
	Unused(cutoff time.Time) []string
	Get(username string) (store.Link, error)
	Delete(username string) error
}

// Revoker invalidates a refresh token at Discord.
type Revoker interface {
	Revoke(ctx context.Context, refreshToken string) error
}

// Forgetter stops any running status session of a user.
type Forgetter interface {
	Forget(username string)
}

// Sweeper deletes links unused for longer than the retention period.
type Sweeper struct {
	Links     Links
	Revoker   Revoker
	Forgetter Forgetter
	Retention time.Duration
	Log       *slog.Logger
}

// Run sweeps once now and then every interval until ctx is cancelled.
// It does nothing when Retention is 0.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	if s.Retention <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.Sweep(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep revokes and deletes every link unused since now - Retention and
// returns how many were removed.
func (s *Sweeper) Sweep(ctx context.Context, now time.Time) int {
	removed := 0
	for _, username := range s.Links.Unused(now.Add(-s.Retention)) {
		link, err := s.Links.Get(username)
		if err == nil {
			if err := s.Revoker.Revoke(ctx, link.RefreshToken); err != nil {
				s.Log.Warn("revoking unused link failed", "user", username, "error", err)
			}
		}
		if err := s.Links.Delete(username); err != nil {
			s.Log.Error("removing unused link failed", "user", username, "error", err)
			continue
		}
		s.Forgetter.Forget(username)
		s.Log.Info("removed unused link", "user", username, "unused_for_more_than", s.Retention.String())
		removed++
	}
	return removed
}
