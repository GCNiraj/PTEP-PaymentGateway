package auth

import (
	"sync"
	"time"
)

type LoginLimiter struct {
	mu           sync.Mutex
	maxAttempts  int
	window       time.Duration
	lockDuration time.Duration
	attempts     map[string]*loginAttempt
}

type loginAttempt struct {
	failures    int
	firstFailed time.Time
	lockUntil   time.Time
	lastSeen    time.Time
}

// NewLoginLimiter creates an in-memory limiter for admin login attempts.
func NewLoginLimiter(maxAttempts int, window, lockDuration time.Duration) *LoginLimiter {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if window <= 0 {
		window = 15 * time.Minute
	}
	if lockDuration <= 0 {
		lockDuration = 15 * time.Minute
	}
	return &LoginLimiter{
		maxAttempts:  maxAttempts,
		window:       window,
		lockDuration: lockDuration,
		attempts:     map[string]*loginAttempt{},
	}
}

// Allow checks whether the key can attempt login right now.
func (l *LoginLimiter) Allow(key string) (bool, time.Duration) {
	if l == nil || key == "" {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UTC()
	l.cleanup(now)
	state, ok := l.attempts[key]
	if !ok || state == nil || state.lockUntil.IsZero() {
		return true, 0
	}
	if now.Before(state.lockUntil) {
		return false, state.lockUntil.Sub(now)
	}
	return true, 0
}

// RegisterFailure records a failed login and locks key when threshold is reached.
func (l *LoginLimiter) RegisterFailure(key string) (bool, time.Duration) {
	if l == nil || key == "" {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UTC()
	l.cleanup(now)
	state, ok := l.attempts[key]
	if !ok || state == nil {
		state = &loginAttempt{}
		l.attempts[key] = state
	}
	state.lastSeen = now

	if !state.lockUntil.IsZero() && now.Before(state.lockUntil) {
		return true, state.lockUntil.Sub(now)
	}

	if state.firstFailed.IsZero() || now.Sub(state.firstFailed) > l.window {
		state.firstFailed = now
		state.failures = 1
		state.lockUntil = time.Time{}
		return false, 0
	}

	state.failures++
	if state.failures >= l.maxAttempts {
		state.failures = 0
		state.firstFailed = time.Time{}
		state.lockUntil = now.Add(l.lockDuration)
		return true, l.lockDuration
	}
	return false, 0
}

// RegisterSuccess clears tracked failed attempts for key.
func (l *LoginLimiter) RegisterSuccess(key string) {
	if l == nil || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

// FailureCount returns failed attempts tracked in the active window for key.
func (l *LoginLimiter) FailureCount(key string) int {
	if l == nil || key == "" {
		return 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UTC()
	l.cleanup(now)
	state, ok := l.attempts[key]
	if !ok || state == nil || state.firstFailed.IsZero() {
		return 0
	}
	if now.Sub(state.firstFailed) > l.window {
		return 0
	}
	if state.failures < 0 {
		return 0
	}
	return state.failures
}

func (l *LoginLimiter) cleanup(now time.Time) {
	staleBefore := now.Add(-(l.window + l.lockDuration + time.Hour))
	for key, state := range l.attempts {
		if state == nil {
			delete(l.attempts, key)
			continue
		}
		if !state.lockUntil.IsZero() && now.Before(state.lockUntil) {
			continue
		}
		if state.lastSeen.Before(staleBefore) {
			delete(l.attempts, key)
		}
	}
}
