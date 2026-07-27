package auth

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestLimiter(max int, window time.Duration) (*Limiter, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)}
	return NewLimiterWithClock(max, window, clock.Now), clock
}

func TestLimiterAllowsUpToMax(t *testing.T) {
	lim, _ := newTestLimiter(3, time.Minute)
	for i := 1; i <= 3; i++ {
		if !lim.Allow("1.2.3.4") {
			t.Fatalf("attempt %d was blocked, want allowed", i)
		}
	}
	if lim.Allow("1.2.3.4") {
		t.Fatal("attempt 4 was allowed, want blocked")
	}
}

func TestLimiterIsPerKey(t *testing.T) {
	lim, _ := newTestLimiter(1, time.Minute)
	if !lim.Allow("1.2.3.4") {
		t.Fatal("first attempt from 1.2.3.4 blocked")
	}
	if lim.Allow("1.2.3.4") {
		t.Fatal("second attempt from 1.2.3.4 allowed, want blocked")
	}
	if !lim.Allow("5.6.7.8") {
		t.Fatal("a different key was blocked by another key's attempts")
	}
}

func TestLimiterWindowSlides(t *testing.T) {
	lim, clock := newTestLimiter(2, time.Minute)
	if !lim.Allow("k") || !lim.Allow("k") {
		t.Fatal("the first two attempts should be allowed")
	}
	if lim.Allow("k") {
		t.Fatal("the third attempt inside the window should be blocked")
	}
	clock.Advance(time.Minute + time.Second)
	if !lim.Allow("k") {
		t.Fatal("attempts should be allowed again once the window has passed")
	}
}

func TestLimiterResetClearsKey(t *testing.T) {
	lim, _ := newTestLimiter(1, time.Minute)
	if !lim.Allow("k") {
		t.Fatal("first attempt blocked")
	}
	if lim.Allow("k") {
		t.Fatal("second attempt allowed, want blocked")
	}
	lim.Reset("k")
	if !lim.Allow("k") {
		t.Fatal("attempt after Reset was blocked")
	}
}

func TestLimiterIsConcurrencySafe(t *testing.T) {
	lim, _ := newTestLimiter(1000, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				lim.Allow("shared")
			}
		}()
	}
	wg.Wait()
	// 50*20 == 1000 attempts consumed the whole budget; the next must fail.
	if lim.Allow("shared") {
		t.Fatal("limiter lost attempts under concurrency")
	}
}

func TestLoginDefaultsMatchSpec(t *testing.T) {
	if LoginAttemptLimit != 5 {
		t.Errorf("LoginAttemptLimit = %d, want 5", LoginAttemptLimit)
	}
	if LoginAttemptWindow != 15*time.Minute {
		t.Errorf("LoginAttemptWindow = %v, want 15m", LoginAttemptWindow)
	}
}

func TestLimiterEvictsIdleKeys(t *testing.T) {
	lim, clock := newTestLimiter(2, time.Minute)

	// Record attempts under several keys.
	if !lim.Allow("idle1") || !lim.Allow("idle2") {
		t.Fatal("initial attempts blocked")
	}
	// active will receive an attempt within the current window.
	if !lim.Allow("active") {
		t.Fatal("initial active attempt blocked")
	}

	// Advance past the window so idle1 and idle2 are expired.
	clock.Advance(time.Minute + time.Second)

	// Trigger Allow on active with a fresh attempt (still within new window).
	if !lim.Allow("active") {
		t.Fatal("active key's new attempt blocked")
	}

	// Idle keys should be evicted from the map during the sweep.
	// Access the internal map to verify eviction occurred.
	// The test is in the auth package, so it can read l.hits under l.mu.
	lim.mu.Lock()
	if _, exists := lim.hits["idle1"]; exists {
		t.Error("idle1 was not evicted from the map")
	}
	if _, exists := lim.hits["idle2"]; exists {
		t.Error("idle2 was not evicted from the map")
	}
	if _, exists := lim.hits["active"]; !exists {
		t.Error("active key was unexpectedly evicted")
	}
	if len(lim.hits["active"]) == 0 {
		t.Error("active key has no attempts recorded")
	}
	lim.mu.Unlock()
}
