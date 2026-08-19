package auth

import (
	"testing"
	"time"
)

func TestSessionManagerLifecycle(t *testing.T) {
	t.Parallel()

	mgr := NewSessionManager(time.Minute)
	sessionID, err := mgr.Create("admin")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session id")
	}
	if !mgr.Validate(sessionID, "admin") {
		t.Fatal("expected session to be valid")
	}

	mgr.Revoke(sessionID)
	if mgr.Validate(sessionID, "admin") {
		t.Fatal("expected session to be revoked")
	}
}

func TestSessionManagerRevokeUser(t *testing.T) {
	t.Parallel()

	mgr := NewSessionManager(time.Minute)
	sessionA, err := mgr.Create("admin")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	sessionB, err := mgr.Create("admin")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	mgr.RevokeUser("admin")
	if mgr.Validate(sessionA, "admin") {
		t.Fatal("expected first session to be revoked")
	}
	if mgr.Validate(sessionB, "admin") {
		t.Fatal("expected second session to be revoked")
	}
}

func TestSessionManagerExpiry(t *testing.T) {
	t.Parallel()

	mgr := NewSessionManager(25 * time.Millisecond)
	sessionID, err := mgr.Create("admin")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if !mgr.Validate(sessionID, "admin") {
		t.Fatal("expected session to be initially valid")
	}

	time.Sleep(40 * time.Millisecond)
	if mgr.Validate(sessionID, "admin") {
		t.Fatal("expected session to expire")
	}
}
