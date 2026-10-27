// Package ratelimit limits attempts per key (for example per IP address or
// username) within a sliding time window.
package ratelimit

import (
	"sync"
	"time"
)

// defaultMaxKeys bounds memory use. When it is reached, keys without recent
// attempts are dropped; if none can be dropped, new keys are refused.
const defaultMaxKeys = 10_000

// Limiter allows at most max attempts per key within window.
type Limiter struct {
	max     int
	maxKeys int
	window  time.Duration
	now     func() time.Time

	mu       sync.Mutex
	attempts map[string][]time.Time
}

func New(max int, window time.Duration) *Limiter {
	return &Limiter{
		max:      max,
		maxKeys:  defaultMaxKeys,
		window:   window,
		now:      time.Now,
		attempts: make(map[string][]time.Time),
	}
}

// Allow records an attempt for key and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	recent := l.recent(key, now)
	if len(recent) >= l.max {
		l.attempts[key] = recent
		return false
	}
	if _, known := l.attempts[key]; !known && len(l.attempts) >= l.maxKeys {
		l.prune(now)
		if len(l.attempts) >= l.maxKeys {
			// Refuse rather than grow without limit: fail closed.
			return false
		}
	}
	l.attempts[key] = append(recent, now)
	return true
}

// Reset forgets the attempts of key, for example after a successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

func (l *Limiter) recent(key string, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	kept := l.attempts[key][:0]
	for _, t := range l.attempts[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

func (l *Limiter) prune(now time.Time) {
	for key := range l.attempts {
		if recent := l.recent(key, now); len(recent) == 0 {
			delete(l.attempts, key)
		} else {
			l.attempts[key] = recent
		}
	}
}
