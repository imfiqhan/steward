// Package ratelimit is fixed-window rate limiting, bounding attempts on the
// token endpoint and the two-factor challenge.
//
// This is deliberately in-process rather than backed by Steward's Cache: that
// interface offers only Get/Set/Delete, so counting through it would be a racy
// read-modify-write. The consequence is that limits are per process — N
// replicas admit N times the configured rate. Put a limiter at the edge if you
// need a strict global bound.
package ratelimit

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// maxRateEntries bounds memory: a caller cycling usernames would otherwise grow
// the map without limit. Expired entries are pruned first; if everything is
// still live at the cap, new keys are refused rather than admitted, so the
// limiter fails closed.
const maxRateEntries = 20000

type rateEntry struct {
	count   int
	resetAt time.Time
}

type Limiter struct {
	mu      sync.Mutex
	window  time.Duration
	entries map[string]rateEntry
}

func New(window time.Duration) *Limiter {
	return &Limiter{window: window, entries: map[string]rateEntry{}}
}

// Allow records an attempt against key and reports whether it fits inside
// limit for the current window. When it does not, the second return value is
// how long until the window resets. now is a parameter so tests need no sleep.
func (rl *Limiter) Allow(key string, limit int, now time.Time) (bool, time.Duration) {
	if limit <= 0 {
		return true, 0
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	e, ok := rl.entries[key]
	if !ok || now.After(e.resetAt) {
		if len(rl.entries) >= maxRateEntries {
			rl.prune(now)
			if len(rl.entries) >= maxRateEntries {
				return false, rl.window
			}
		}
		rl.entries[key] = rateEntry{count: 1, resetAt: now.Add(rl.window)}
		return true, 0
	}
	if e.count >= limit {
		return false, e.resetAt.Sub(now)
	}
	e.count++
	rl.entries[key] = e
	return true, 0
}

// prune drops windows that have already elapsed. Callers hold rl.mu.
func (rl *Limiter) prune(now time.Time) {
	for k, e := range rl.entries {
		if now.After(e.resetAt) {
			delete(rl.entries, k)
		}
	}
}

// ClientIP is the peer address, port stripped.
//
// X-Forwarded-For is deliberately ignored: it is caller-controlled, so
// honouring it would let one attacker mint a fresh bucket per request. Behind a
// proxy this means every request shares one bucket, which is why the per-IP
// allowance is loose and the per-username allowance carries the real
// protection.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
