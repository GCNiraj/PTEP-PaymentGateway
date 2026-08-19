package auth

import (
	"sync"
	"time"
)

// IPRateLimiter implements a sliding-window per-IP rate limiter.
// It is safe for concurrent use and nil-safe (a nil receiver always allows).
// Why needed: protects critical payment and OTP endpoints from automated abuse
// and denial-of-service without external dependencies.
type IPRateLimiter struct {
	mu          sync.Mutex
	maxRequests int
	window      time.Duration
	entries     map[string]*ipEntry
}

type ipEntry struct {
	// timestamps holds the time of each request within the current window.
	// Older entries are pruned lazily on each access.
	timestamps []time.Time
}

// NewIPRateLimiter creates a sliding-window rate limiter that allows at most
// maxRequests requests per window duration per IP address.
// Sensible defaults are applied when values are non-positive.
func NewIPRateLimiter(maxRequests int, window time.Duration) *IPRateLimiter {
	if maxRequests <= 0 {
		maxRequests = 60
	}
	if window <= 0 {
		window = time.Minute
	}
	return &IPRateLimiter{
		maxRequests: maxRequests,
		window:      window,
		entries:     make(map[string]*ipEntry),
	}
}

// Allow checks whether the given IP is permitted to make a request right now.
// Returns (true, 0) when the request is within quota.
// Returns (false, retryAfter) when the IP has exceeded its quota, where
// retryAfter is the duration until the oldest request in the window expires.
// A nil *IPRateLimiter always returns (true, 0) — safe to use as no-op.
func (l *IPRateLimiter) Allow(ip string) (bool, time.Duration) {
	if l == nil || ip == "" {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UTC()
	cutoff := now.Add(-l.window)

	entry, ok := l.entries[ip]
	if !ok {
		entry = &ipEntry{}
		l.entries[ip] = entry
	}

	// Prune timestamps outside the current window.
	entry.timestamps = pruneOld(entry.timestamps, cutoff)

	if len(entry.timestamps) >= l.maxRequests {
		// Retry after the earliest request falls out of the window.
		retryAfter := entry.timestamps[0].Add(l.window).Sub(now)
		if retryAfter < 0 {
			retryAfter = 0
		}
		return false, retryAfter
	}

	entry.timestamps = append(entry.timestamps, now)
	// Opportunistic global cleanup to prevent unbounded map growth.
	if len(l.entries) > 8192 {
		l.evictStale(now, cutoff)
	}
	return true, 0
}

// pruneOld removes timestamps older than cutoff from the slice and returns the
// trimmed slice. It avoids allocating a new backing array when nothing is pruned.
func pruneOld(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	return ts[i:]
}

// evictStale removes IP entries whose entire timestamp history is outside the
// current window. Must be called with l.mu held.
func (l *IPRateLimiter) evictStale(now time.Time, cutoff time.Time) {
	for ip, entry := range l.entries {
		entry.timestamps = pruneOld(entry.timestamps, cutoff)
		if len(entry.timestamps) == 0 {
			delete(l.entries, ip)
		}
	}
	_ = now // unused but kept for readability of caller
}
