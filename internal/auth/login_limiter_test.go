package auth

import (
	"testing"
	"time"
)

func TestLoginLimiterFailureCountTracksWindow(t *testing.T) {
	t.Parallel()

	l := NewLoginLimiter(5, 100*time.Millisecond, time.Minute)
	key := "admin|127.0.0.1"

	if got := l.FailureCount(key); got != 0 {
		t.Fatalf("expected initial failure count 0, got %d", got)
	}

	locked, _ := l.RegisterFailure(key)
	if locked {
		t.Fatal("first failure must not lock")
	}
	if got := l.FailureCount(key); got != 1 {
		t.Fatalf("expected failure count 1, got %d", got)
	}

	locked, _ = l.RegisterFailure(key)
	if locked {
		t.Fatal("second failure must not lock")
	}
	if got := l.FailureCount(key); got != 2 {
		t.Fatalf("expected failure count 2, got %d", got)
	}

	time.Sleep(130 * time.Millisecond)
	if got := l.FailureCount(key); got != 0 {
		t.Fatalf("expected expired-window failure count 0, got %d", got)
	}
}

func TestLoginLimiterFailureCountResetsOnSuccessAndLock(t *testing.T) {
	t.Parallel()

	key := "admin|127.0.0.1"

	l := NewLoginLimiter(4, time.Minute, time.Minute)
	l.RegisterFailure(key) //nolint:errcheck
	l.RegisterFailure(key) //nolint:errcheck
	if got := l.FailureCount(key); got != 2 {
		t.Fatalf("expected count 2 before success, got %d", got)
	}
	l.RegisterSuccess(key)
	if got := l.FailureCount(key); got != 0 {
		t.Fatalf("expected count 0 after success, got %d", got)
	}

	locking := NewLoginLimiter(2, time.Minute, time.Minute)
	locking.RegisterFailure(key) //nolint:errcheck
	locked, _ := locking.RegisterFailure(key)
	if !locked {
		t.Fatal("expected lock on second failure")
	}
	if got := locking.FailureCount(key); got != 0 {
		t.Fatalf("expected count reset to 0 once locked, got %d", got)
	}
}
