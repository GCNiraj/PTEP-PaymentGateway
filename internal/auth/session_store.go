package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"
)

type Session struct {
	ID        string
	Username  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// SessionManager keeps active admin sessions in memory.
// Note: this is process-local; use a shared store (e.g. Redis) for multi-instance deployments.
type SessionManager struct {
	mu       sync.RWMutex
	ttl      time.Duration
	sessions map[string]Session
	byUser   map[string]map[string]struct{}
}

func NewSessionManager(ttl time.Duration) *SessionManager {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &SessionManager{
		ttl:      ttl,
		sessions: map[string]Session{},
		byUser:   map[string]map[string]struct{}{},
	}
}

func (s *SessionManager) Create(username string) (string, error) {
	if s == nil {
		return "", errors.New("session manager is nil")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return "", errors.New("username is required")
	}

	sessionID, err := newSessionID()
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	session := Session{
		ID:        sessionID,
		Username:  username,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	s.sessions[sessionID] = session
	if _, ok := s.byUser[username]; !ok {
		s.byUser[username] = map[string]struct{}{}
	}
	s.byUser[username][sessionID] = struct{}{}
	return sessionID, nil
}

func (s *SessionManager) Validate(sessionID, username string) bool {
	_, ok := s.Get(sessionID, username)
	return ok
}

func (s *SessionManager) Get(sessionID, username string) (Session, bool) {
	var empty Session
	if s == nil {
		return empty, false
	}
	sessionID = strings.TrimSpace(sessionID)
	username = strings.TrimSpace(username)
	if sessionID == "" || username == "" {
		return empty, false
	}

	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)

	session, ok := s.sessions[sessionID]
	if !ok {
		return empty, false
	}
	if session.Username != username {
		return empty, false
	}
	if !now.Before(session.ExpiresAt) {
		s.deleteSessionLocked(sessionID)
		return empty, false
	}
	return session, true
}

func (s *SessionManager) Revoke(sessionID string) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteSessionLocked(sessionID)
}

func (s *SessionManager) RevokeUser(username string) {
	if s == nil {
		return
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sessionIDs, ok := s.byUser[username]
	if !ok {
		return
	}
	for sessionID := range sessionIDs {
		delete(s.sessions, sessionID)
	}
	delete(s.byUser, username)
}

func (s *SessionManager) cleanupExpiredLocked(now time.Time) {
	for sessionID, session := range s.sessions {
		if !now.Before(session.ExpiresAt) {
			s.deleteSessionLocked(sessionID)
		}
	}
}

func (s *SessionManager) deleteSessionLocked(sessionID string) {
	session, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	delete(s.sessions, sessionID)

	userSessions, ok := s.byUser[session.Username]
	if !ok {
		return
	}
	delete(userSessions, sessionID)
	if len(userSessions) == 0 {
		delete(s.byUser, session.Username)
	}
}

func newSessionID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
