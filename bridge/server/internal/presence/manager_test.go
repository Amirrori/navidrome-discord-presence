package presence

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"navidrome-discord-status/bridge/internal/discord"
	"navidrome-discord-status/bridge/internal/events"
	"navidrome-discord-status/bridge/internal/store"
)

var testTiming = Timing{
	MinUpdateInterval: 30 * time.Millisecond,
	IdleTimeout:       100 * time.Millisecond,
	RefreshMargin:     time.Hour,
	StaleGrace:        time.Hour,
	PausedTimeout:     time.Hour,
	UnknownEndTimeout: time.Hour,
}

type fakeLinks struct {
	mu    sync.Mutex
	links map[string]store.Link
}

func (f *fakeLinks) Get(username string) (store.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	link, ok := f.links[username]
	if !ok {
		return store.Link{}, store.ErrNotLinked
	}
	return link, nil
}

func (f *fakeLinks) RecordRefresh(username, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	link := f.links[username]
	link.RefreshToken = token
	f.links[username] = link
	return nil
}

func (f *fakeLinks) Delete(username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.links, username)
	return nil
}

type fakeRefresher struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeRefresher) Refresh(_ context.Context, _ string) (discord.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return discord.Token{}, f.err
	}
	return discord.Token{AccessToken: "access", RefreshToken: "rotated", ExpiresAt: time.Now().Add(7 * 24 * time.Hour)}, nil
}

type fakeWorker struct {
	mu         sync.Mutex
	activities []Activity
	clears     int
	closed     bool
	done       chan struct{}
	once       sync.Once
}

func newFakeWorker() *fakeWorker { return &fakeWorker{done: make(chan struct{})} }

func (w *fakeWorker) SetActivity(a Activity) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.activities = append(w.activities, a)
	return nil
}

func (w *fakeWorker) Clear() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.clears++
	return nil
}

func (w *fakeWorker) UpdateToken(string) error { return nil }

func (w *fakeWorker) Close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.crash()
	return nil
}

func (w *fakeWorker) Done() <-chan struct{} { return w.done }

func (w *fakeWorker) crash() { w.once.Do(func() { close(w.done) }) }

func (w *fakeWorker) snapshot() (activities []Activity, clears int, closed bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Activity(nil), w.activities...), w.clears, w.closed
}

type harness struct {
	manager   *Manager
	links     *fakeLinks
	refresher *fakeRefresher
	cancel    context.CancelFunc

	mu      sync.Mutex
	workers []*fakeWorker
	tokens  []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWithTiming(t, testTiming)
}

func newHarnessWithTiming(t *testing.T, timing Timing) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{
		links:     &fakeLinks{links: map[string]store.Link{"alice": {DiscordID: "1", RefreshToken: "initial"}}},
		refresher: &fakeRefresher{},
		cancel:    cancel,
	}
	spawn := func(_ string, accessToken string) (Worker, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		w := newFakeWorker()
		h.workers = append(h.workers, w)
		h.tokens = append(h.tokens, accessToken)
		return w, nil
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h.manager = NewManager(ctx, h.links, h.refresher, spawn, testOptions, timing, log)
	t.Cleanup(func() {
		cancel()
		h.manager.Wait()
	})
	return h
}

func (h *harness) worker(i int) *fakeWorker {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.workers) {
		return nil
	}
	return h.workers[i]
}

func (h *harness) spawned() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.workers)
}

func playing(title string) events.Event {
	return events.Event{Version: 1, User: "alice", State: events.StatePlaying,
		Track: &events.Track{Title: title, Artist: "Weezer", StartMs: 1, EndMs: 2}}
}

func stopped() events.Event {
	return events.Event{Version: 1, User: "alice", State: events.StateStopped}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestPlayingStartsWorkerWithAccessToken(t *testing.T) {
	h := newHarness(t)
	h.manager.Handle(playing("Undone"))

	eventually(t, "status set", func() bool {
		w := h.worker(0)
		if w == nil {
			return false
		}
		activities, _, _ := w.snapshot()
		return len(activities) == 1 && activities[0].Details == "Undone"
	})
	if h.tokens[0] != "access" {
		t.Errorf("worker got token %q", h.tokens[0])
	}
	if link, _ := h.links.Get("alice"); link.RefreshToken != "rotated" {
		t.Errorf("rotated refresh token not stored: %q", link.RefreshToken)
	}
}

func TestUnlinkedUsersAreIgnored(t *testing.T) {
	h := newHarness(t)
	event := playing("Undone")
	event.User = "bob"
	h.manager.Handle(event)

	time.Sleep(3 * testTiming.MinUpdateInterval)
	if h.spawned() != 0 {
		t.Error("no worker may start for an unlinked user")
	}
}

func TestRapidEventsAreCoalesced(t *testing.T) {
	h := newHarness(t)
	h.manager.Handle(playing("first"))
	eventually(t, "first status", func() bool { return h.worker(0) != nil })

	for _, title := range []string{"skip 1", "skip 2", "skip 3", "last"} {
		h.manager.Handle(playing(title))
	}

	eventually(t, "last status", func() bool {
		activities, _, _ := h.worker(0).snapshot()
		return activities[len(activities)-1].Details == "last"
	})
	activities, _, _ := h.worker(0).snapshot()
	if len(activities) > 2 {
		t.Errorf("%d updates sent, skipped tracks must be coalesced", len(activities))
	}
}

func TestStopClearsThenDisconnectsWhenIdle(t *testing.T) {
	h := newHarness(t)
	h.manager.Handle(playing("Undone"))
	eventually(t, "worker started", func() bool { return h.worker(0) != nil })

	h.manager.Handle(stopped())
	eventually(t, "status cleared and worker closed after idle", func() bool {
		_, clears, closed := h.worker(0).snapshot()
		return clears == 1 && closed
	})
}

func TestRevokedGrantRemovesLink(t *testing.T) {
	h := newHarness(t)
	h.refresher.err = discord.ErrInvalidGrant
	h.manager.Handle(playing("Undone"))

	eventually(t, "link removed", func() bool {
		_, err := h.links.Get("alice")
		return err != nil
	})
	if h.spawned() != 0 {
		t.Error("no worker may start without a valid grant")
	}
}

func TestCrashedWorkerIsRestarted(t *testing.T) {
	h := newHarness(t)
	h.manager.Handle(playing("one"))
	eventually(t, "first worker", func() bool { return h.worker(0) != nil })

	h.worker(0).crash()
	time.Sleep(10 * time.Millisecond)
	h.manager.Handle(playing("two"))

	eventually(t, "second worker with the new status", func() bool {
		w := h.worker(1)
		if w == nil {
			return false
		}
		activities, _, _ := w.snapshot()
		return len(activities) == 1 && activities[0].Details == "two"
	})
	h.refresher.mu.Lock()
	defer h.refresher.mu.Unlock()
	if h.refresher.calls != 1 {
		t.Errorf("token refreshed %d times, the cached token must be reused", h.refresher.calls)
	}
}

func TestForgetClosesWorker(t *testing.T) {
	h := newHarness(t)
	h.manager.Handle(playing("Undone"))
	eventually(t, "worker started", func() bool { return h.worker(0) != nil })

	h.manager.Forget("alice")
	eventually(t, "worker closed", func() bool {
		_, _, closed := h.worker(0).snapshot()
		return closed
	})
}

func TestHandleReportsLinkStatus(t *testing.T) {
	h := newHarness(t)
	if !h.manager.Handle(playing("Undone")) {
		t.Error("alice is linked")
	}
	event := playing("Undone")
	event.User = "bob"
	if h.manager.Handle(event) {
		t.Error("bob is not linked")
	}
}

func TestStaleStatusIsCleared(t *testing.T) {
	timing := testTiming
	timing.StaleGrace = 20 * time.Millisecond
	h := newHarnessWithTiming(t, timing)

	// The track "ended" long ago (EndMs: 2) and no stopped event follows.
	h.manager.Handle(playing("Undone"))
	eventually(t, "stale status cleared and worker closed after idle", func() bool {
		w := h.worker(0)
		if w == nil {
			return false
		}
		_, clears, closed := w.snapshot()
		return clears == 1 && closed
	})
}

func TestPausedStatusTimesOut(t *testing.T) {
	timing := testTiming
	timing.PausedTimeout = 20 * time.Millisecond
	h := newHarnessWithTiming(t, timing)

	paused := playing("Undone")
	paused.State = events.StatePaused
	h.manager.Handle(paused)
	eventually(t, "paused status cleared", func() bool {
		w := h.worker(0)
		if w == nil {
			return false
		}
		_, clears, _ := w.snapshot()
		return clears == 1
	})
}
