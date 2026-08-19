package auth

import (
	"testing"
	"time"
)

func TestGenerateIncludesSessionIDClaim(t *testing.T) {
	t.Parallel()

	mgr := &Manager{
		Secret:   "test-secret",
		TTL:      time.Minute,
		Issuer:   "issuer",
		Audience: "audience",
	}

	token, err := mgr.Generate("admin", "session-123")
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	claims, err := mgr.Verify(token)
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if claims.Username != "admin" {
		t.Fatalf("expected username admin, got %q", claims.Username)
	}
	if claims.SessionID != "session-123" {
		t.Fatalf("expected session id session-123, got %q", claims.SessionID)
	}
	if claims.ID != "session-123" {
		t.Fatalf("expected jti to match session id, got %q", claims.ID)
	}
}
