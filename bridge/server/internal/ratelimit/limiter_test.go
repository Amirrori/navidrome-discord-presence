package ratelimit

import (
	"testing"
	"time"
)

func newTestLimiter(max int, now *time.Time) *Limiter {
	l := New(max, time.Minute)
	l.now = func() time.Time { return *now }
	return l
}

func allowTimes(l *Limiter, key string, n int) bool {
	for range n {
		if !l.Allow(key) {
			return false
		}
	}
	return true
}

func TestLimiterAllowsUpToMax(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newTestLimiter(2, &now)

	if !allowTimes(l, "ip", 2) {
		t.Fatal("attempts within the limit must be allowed")
	}
	if l.Allow("ip") {
		t.Error("the third attempt within the window must be refused")
	}
	if !l.Allow("other") {
		t.Error("keys are limited independently")
	}
}

func TestLimiterWindowAndReset(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newTestLimiter(2, &now)
	allowTimes(l, "ip", 2)

	now = now.Add(61 * time.Second)
	if !l.Allow("ip") {
		t.Error("attempts must be allowed again after the window")
	}

	l.Reset("ip")
	if !allowTimes(l, "ip", 2) {
		t.Error("Reset must clear the key's attempts")
	}
}

func TestLimiterRefusesNewKeysWhenFull(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newTestLimiter(5, &now)
	l.maxKeys = 2
	l.Allow("a")
	l.Allow("b")

	if l.Allow("c") {
		t.Error("a full limiter must refuse new keys instead of growing")
	}
	if !l.Allow("a") {
		t.Error("known keys keep working while the limiter is full")
	}

	now = now.Add(2 * time.Minute)
	if !l.Allow("c") {
		t.Error("keys without recent attempts must be dropped to make room")
	}
}
