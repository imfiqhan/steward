package steward

import (
	"testing"
	"time"
)

func TestRateLimiterWindow(t *testing.T) {
	rl := newRateLimiter(time.Minute)
	t0 := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	// The first `limit` attempts pass, the next does not.
	for i := 1; i <= 3; i++ {
		if ok, _ := rl.allow("k", 3, t0); !ok {
			t.Fatalf("attempt %d was refused, want allowed", i)
		}
	}
	ok, retry := rl.allow("k", 3, t0)
	if ok {
		t.Error("fourth attempt allowed, want refused")
	}
	if retry <= 0 || retry > time.Minute {
		t.Errorf("retry-after = %v, want within (0, 1m]", retry)
	}

	// Keys are independent.
	if ok, _ := rl.allow("other", 3, t0); !ok {
		t.Error("a different key was refused")
	}

	// The window resets.
	if ok, _ := rl.allow("k", 3, t0.Add(time.Minute+time.Second)); !ok {
		t.Error("attempt after the window elapsed was refused")
	}
}

func TestRateLimiterDisabledByNonPositiveLimit(t *testing.T) {
	rl := newRateLimiter(time.Minute)
	t0 := time.Now()
	for range 50 {
		if ok, _ := rl.allow("k", 0, t0); !ok {
			t.Fatal("limit 0 should not throttle; the caller decides")
		}
	}
}

func TestRateLimiterPrunesElapsedWindows(t *testing.T) {
	rl := newRateLimiter(time.Minute)
	t0 := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	for i := range 100 {
		rl.allow(string(rune('a'+i%26))+string(rune('a'+i/26)), 1, t0)
	}
	before := len(rl.entries)
	rl.mu.Lock()
	rl.prune(t0.Add(2 * time.Minute))
	rl.mu.Unlock()
	if before == 0 {
		t.Fatal("no entries were recorded")
	}
	if len(rl.entries) != 0 {
		t.Errorf("prune left %d of %d entries, want 0", len(rl.entries), before)
	}
}
