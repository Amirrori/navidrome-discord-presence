// Package presence turns playback events into Discord statuses. Each linked
// user gets a session goroutine that owns the user's access token and worker
// process, rate-limits updates and shuts the worker down when idle.
package presence

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"navidrome-discord-status/bridge/internal/discord"
	"navidrome-discord-status/bridge/internal/events"
	"navidrome-discord-status/bridge/internal/store"
)

type Worker interface {
	SetActivity(Activity) error
	Clear() error
	UpdateToken(accessToken string) error
	Close() error
	Done() <-chan struct{}
}

type Spawner func(username, accessToken string) (Worker, error)

type LinkStore interface {
	Get(username string) (store.Link, error)
	// RecordRefresh stores a (possibly rotated) refresh token and marks the link used.
	RecordRefresh(username, refreshToken string) error
	Delete(username string) error
}

// TokenRefresher obtains access tokens from refresh tokens.
type TokenRefresher interface {
	Refresh(ctx context.Context, refreshToken string) (discord.Token, error)
}

// Timing controls rate limiting and idle shutdown.
type Timing struct {
	// MinUpdateInterval is the minimum time between two status updates of a
	// user; skipping through tracks quickly only sends the last one.
	MinUpdateInterval time.Duration
	// IdleTimeout is how long a worker stays connected after playback stopped.
	IdleTimeout time.Duration
	// RefreshMargin renews access tokens this long before they expire.
	RefreshMargin time.Duration
	// StaleGrace clears a playing status this long after the track should
	// have ended without a new event, so a lost "stopped" can't leave a
	// status behind forever.
	StaleGrace time.Duration
	// PausedTimeout clears a paused status that gets no further events.
	PausedTimeout time.Duration
	// UnknownEndTimeout clears a playing status without a known end time.
	UnknownEndTimeout time.Duration
}

// DefaultTiming stays well within Discord's presence rate limits.
var DefaultTiming = Timing{
	MinUpdateInterval: 5 * time.Second,
	IdleTimeout:       10 * time.Minute,
	RefreshMargin:     24 * time.Hour,
	StaleGrace:        time.Minute,
	PausedTimeout:     time.Hour,
	UnknownEndTimeout: 30 * time.Minute,
}

// Manager routes events to per-user sessions.
type Manager struct {
	ctx     context.Context
	links   LinkStore
	tokens  TokenRefresher
	spawn   Spawner
	display DisplayOptions
	timing  Timing
	log     *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session
	wg       sync.WaitGroup
}

// NewManager returns a manager whose sessions stop when ctx is cancelled.
func NewManager(ctx context.Context, links LinkStore, tokens TokenRefresher, spawn Spawner,
	display DisplayOptions, timing Timing, log *slog.Logger) *Manager {
	return &Manager{
		ctx:      ctx,
		links:    links,
		tokens:   tokens,
		spawn:    spawn,
		display:  display,
		timing:   timing,
		log:      log,
		sessions: make(map[string]*session),
	}
}

// Handle queues an event and reports whether the user is linked. Events of
// users without a linked account are dropped. Handle never blocks on Discord
// or the worker.
func (m *Manager) Handle(event events.Event) bool {
	if !m.IsLinked(event.User) {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[event.User]
	if !ok || s.ctx.Err() != nil {
		s = m.startSession(event.User)
	}
	s.queue(event)
	return true
}

func (m *Manager) IsLinked(username string) bool {
	_, err := m.links.Get(username)
	if err != nil && !errors.Is(err, store.ErrNotLinked) {
		m.log.Warn("reading link failed", "user", username, "error", err)
	}
	return err == nil
}

// Forget stops the user's session, for example after unlinking.
func (m *Manager) Forget(username string) {
	m.mu.Lock()
	s, ok := m.sessions[username]
	m.mu.Unlock()
	if ok {
		s.stop()
	}
}

// Wait blocks until all sessions have stopped after the context was cancelled.
func (m *Manager) Wait() {
	m.wg.Wait()
}

func (m *Manager) startSession(username string) *session {
	ctx, cancel := context.WithCancel(m.ctx)
	s := &session{
		user:    username,
		manager: m,
		log:     m.log.With("user", username),
		ctx:     ctx,
		cancel:  cancel,
		events:  make(chan events.Event, 1),
	}
	m.sessions[username] = s
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		s.run()
		m.endSession(s)
	}()
	return s
}

// endSession removes a finished session. An event that was queued while the
// session was ending is handed to a new session instead of being lost.
func (m *Manager) endSession(s *session) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s.cancel()
	if m.sessions[s.user] == s {
		delete(m.sessions, s.user)
	}
	select {
	case event := <-s.events:
		if m.ctx.Err() == nil {
			m.startSession(s.user).queue(event)
		}
	default:
	}
}

// session owns one user's token and worker. Only its goroutine touches them.
type session struct {
	user    string
	manager *Manager
	log     *slog.Logger
	ctx     context.Context
	cancel  context.CancelFunc
	events  chan events.Event

	worker      Worker
	accessToken string
	expiresAt   time.Time
}

// queue replaces any not yet processed event: only the latest state matters.
func (s *session) queue(event events.Event) {
	for {
		select {
		case s.events <- event:
			return
		default:
			select {
			case <-s.events:
			default:
			}
		}
	}
}

func (s *session) stop() {
	s.cancel()
}

func (s *session) run() {
	timing := s.manager.timing
	var (
		pending    *events.Event
		lastUpdate time.Time
		applyTimer = newStoppedTimer()
		idleTimer  = newStoppedTimer()
		staleTimer = newStoppedTimer()
	)
	defer s.closeWorker()

	for {
		var workerDone <-chan struct{}
		if s.worker != nil {
			workerDone = s.worker.Done()
		}

		select {
		case <-s.ctx.Done():
			return

		case event := <-s.events:
			pending = &event
			wait := time.Until(lastUpdate.Add(timing.MinUpdateInterval))
			applyTimer.Reset(max(wait, 0))

		case <-applyTimer.C:
			if pending == nil {
				continue
			}
			lastUpdate = time.Now()
			if !s.apply(*pending) {
				return
			}
			if pending.State == events.StateStopped {
				staleTimer.Stop()
				idleTimer.Reset(timing.IdleTimeout)
			} else {
				idleTimer.Stop()
				staleTimer.Reset(s.staleAfter(*pending))
			}
			pending = nil

		case <-staleTimer.C:
			s.log.Info("no playback update since the track should have ended, clearing status")
			s.apply(events.Event{Version: 1, User: s.user, State: events.StateStopped})
			idleTimer.Reset(timing.IdleTimeout)

		case <-idleTimer.C:
			s.log.Info("idle, disconnecting from Discord")
			return

		case <-workerDone:
			s.log.Warn("worker stopped unexpectedly, it will be restarted with the next event")
			s.worker = nil
		}
	}
}

func (s *session) staleAfter(event events.Event) time.Duration {
	timing := s.manager.timing
	switch {
	case event.State == events.StatePaused:
		return timing.PausedTimeout
	case event.Track.EndMs > 0:
		return max(time.Until(time.UnixMilli(event.Track.EndMs)), 0) + timing.StaleGrace
	default:
		return timing.UnknownEndTimeout
	}
}

// apply shows or clears the status. It returns false when the session must end.
func (s *session) apply(event events.Event) bool {
	if event.State == events.StateStopped {
		if s.worker != nil {
			if err := s.worker.Clear(); err != nil {
				s.log.Warn("clearing status failed", "error", err)
			}
		}
		return true
	}

	if err := s.ensureToken(); err != nil {
		if errors.Is(err, discord.ErrInvalidGrant) || errors.Is(err, store.ErrNotLinked) {
			return false
		}
		s.log.Warn("no access token, skipping update", "error", err)
		return true
	}
	if s.worker == nil {
		worker, err := s.manager.spawn(s.user, s.accessToken)
		if err != nil {
			s.log.Warn("starting worker failed", "error", err)
			return true
		}
		s.worker = worker
	}

	activity := BuildActivity(*event.Track, event.State == events.StatePaused, s.manager.display)
	if err := s.worker.SetActivity(activity); err != nil {
		s.log.Warn("setting status failed", "error", err)
	}
	return true
}

// ensureToken refreshes the access token when it is missing or about to
// expire, stores the (possibly rotated) refresh token and passes the new
// access token to a running worker. A rejected grant removes the link.
func (s *session) ensureToken() error {
	if s.accessToken != "" && time.Until(s.expiresAt) > s.manager.timing.RefreshMargin {
		return nil
	}

	link, err := s.manager.links.Get(s.user)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	token, err := s.manager.tokens.Refresh(ctx, link.RefreshToken)
	if errors.Is(err, discord.ErrInvalidGrant) {
		s.log.Warn("Discord access was revoked, removing the link")
		if err := s.manager.links.Delete(s.user); err != nil {
			s.log.Warn("removing link failed", "error", err)
		}
		return err
	}
	if err != nil {
		return err
	}

	if err := s.manager.links.RecordRefresh(s.user, token.RefreshToken); err != nil {
		s.log.Warn("saving refreshed token failed", "error", err)
	}
	s.accessToken, s.expiresAt = token.AccessToken, token.ExpiresAt
	if s.worker != nil {
		if err := s.worker.UpdateToken(token.AccessToken); err != nil {
			s.log.Warn("passing new token to worker failed", "error", err)
		}
	}
	return nil
}

func (s *session) closeWorker() {
	if s.worker == nil {
		return
	}
	if err := s.worker.Close(); err != nil {
		s.log.Warn("closing worker failed", "error", err)
	}
	s.worker = nil
}

func newStoppedTimer() *time.Timer {
	t := time.NewTimer(time.Hour)
	t.Stop()
	return t
}
