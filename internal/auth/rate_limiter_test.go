package auth

import (
	"testing"
	"time"
)

func TestIPRateLimiter_AllowsUnderLimit(t *testing.T) {
	t.Parallel()
	l := NewIPRateLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		ok, retry := l.Allow("1.2.3.4")
		if !ok {
			t.Fatalf("request %d should be allowed, got denied (retryAfter=%v)", i+1, retry)
		}
		if retry != 0 {
			t.Fatalf("request %d: expected retryAfter=0, got %v", i+1, retry)
		}
	}
}

func TestIPRateLimiter_BlocksOverLimit(t *testing.T) {
	t.Parallel()
	const max = 3
	l := NewIPRateLimiter(max, time.Minute)

	for i := 0; i < max; i++ {
		ok, _ := l.Allow("10.0.0.1")
		if !ok {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	ok, retryAfter := l.Allow("10.0.0.1")
	if ok {
		t.Fatal("request beyond limit should be denied")
	}
	if retryAfter <= 0 {
		t.Fatalf("expected positive retryAfter, got %v", retryAfter)
	}
}

func TestIPRateLimiter_ResetsAfterWindow(t *testing.T) {
	t.Parallel()
	const max = 2
	window := 50 * time.Millisecond
	l := NewIPRateLimiter(max, window)

	// Exhaust the limit.
	for i := 0; i < max; i++ {
		ok, _ := l.Allow("172.16.0.1")
		if !ok {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	ok, _ := l.Allow("172.16.0.1")
	if ok {
		t.Fatal("should be denied after exhausting limit")
	}

	// Wait for the window to expire.
	time.Sleep(window + 10*time.Millisecond)

	// Should be allowed again after the window resets.
	ok, _ = l.Allow("172.16.0.1")
	if !ok {
		t.Fatal("should be allowed after window expired")
	}
}

func TestIPRateLimiter_IndependentPerIP(t *testing.T) {
	t.Parallel()
	const max = 2
	l := NewIPRateLimiter(max, time.Minute)

	// Exhaust quota for IP A.
	for i := 0; i < max; i++ {
		l.Allow("192.168.1.1") //nolint:errcheck
	}
	okA, _ := l.Allow("192.168.1.1")
	if okA {
		t.Fatal("IP A should be rate limited")
	}

	// IP B must still be allowed.
	okB, _ := l.Allow("192.168.1.2")
	if !okB {
		t.Fatal("IP B should NOT be rate limited when a different IP is blocked")
	}
}

func TestIPRateLimiter_NilSafe(t *testing.T) {
	t.Parallel()
	var l *IPRateLimiter
	ok, retry := l.Allow("1.1.1.1")
	if !ok {
		t.Fatal("nil limiter must always allow")
	}
	if retry != 0 {
		t.Fatalf("nil limiter must return retryAfter=0, got %v", retry)
	}
}

func TestIPRateLimiter_EmptyIPAllowed(t *testing.T) {
	t.Parallel()
	l := NewIPRateLimiter(1, time.Minute)
	// Empty key must always be allowed (limiter skips tracking).
	ok, _ := l.Allow("")
	if !ok {
		t.Fatal("empty IP must always be allowed")
	}
}
