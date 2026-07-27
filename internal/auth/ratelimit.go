package auth

import (
	"sync"
	"time"
)

// Login throttling defaults: five failed attempts per quarter hour per client.
const (
	LoginAttemptLimit  = 5
	LoginAttemptWindow = 15 * time.Minute
)

// Limiter is a sliding-window counter keyed by an arbitrary string (the login
// handler keys on client IP). It is safe for concurrent use.
type Limiter struct {
	mu        sync.Mutex
	max       int
	window    time.Duration
	now       func() time.Time
	hits      map[string][]time.Time
	lastSweep time.Time
}

// NewLimiter returns a Limiter using the wall clock.
func NewLimiter(max int, window time.Duration) *Limiter {
	return NewLimiterWithClock(max, window, time.Now)
}

// NewLimiterWithClock returns a Limiter with an injected clock, so tests can
// advance time instead of sleeping.
func NewLimiterWithClock(max int, window time.Duration, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		max:    max,
		window: window,
		now:    now,
		hits:   make(map[string][]time.Time),
	}
}

// Allow records an attempt for key and reports whether it is permitted.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)

	kept := l.hits[key][:0]
	for _, ts := range l.hits[key] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = kept
	}

	// Periodically sweep the entire map to evict idle keys,
	// at most once per window.
	if now.After(l.lastSweep.Add(l.window)) {
		l.lastSweep = now
		for k, attempts := range l.hits {
			// Recompute for each key; if all expired, delete it.
			keptAttempts := attempts[:0]
			for _, ts := range attempts {
				if ts.After(cutoff) {
					keptAttempts = append(keptAttempts, ts)
				}
			}
			if len(keptAttempts) == 0 {
				delete(l.hits, k)
			} else {
				l.hits[k] = keptAttempts
			}
		}
	}

	// Check limit and record attempt.
	if len(l.hits[key]) >= l.max {
		return false
	}
	l.hits[key] = append(l.hits[key], now)
	return true
}

// Reset clears the history for key. Call it after a successful login so one
// forgotten password does not lock out the rest of the window.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
