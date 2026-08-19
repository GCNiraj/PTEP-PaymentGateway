package common

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// GenerateAPIKey creates a secure random API key with the format: sk_live_<random>
// Why needed: provides unique authentication credentials for external applications.
// Called from: app creation endpoints.
func GenerateAPIKey() (string, error) {
	// Generate 32 random bytes
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Encode to base64 and create key with prefix
	randomPart := base64.URLEncoding.EncodeToString(b)
	apiKey := fmt.Sprintf("sk_live_%s", randomPart)

	return apiKey, nil
}

// GenerateAPISecret creates a secure random secret for webhook signature verification.
// Why needed: allows external apps to verify webhook authenticity.
// Called from: app creation endpoints.
func GenerateAPISecret() (string, error) {
	// Generate 32 random bytes
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Encode to base64
	secret := base64.URLEncoding.EncodeToString(b)

	return secret, nil
}

// HashAPIKey hashes an API key using bcrypt for secure storage.
// Why needed: API keys should never be stored in plain text.
// Called from: app creation/update endpoints before database storage.
func HashAPIKey(apiKey string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(apiKey), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash API key: %w", err)
	}
	return string(hash), nil
}

// VerifyAPIKey compares a plain API key with its hashed version.
// Why needed: authentication middleware needs to verify incoming API keys.
// Called from: API key authentication middleware.
func VerifyAPIKey(plainKey, hashedKey string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashedKey), []byte(plainKey))
	return err == nil
}

// GenerateAppID creates a unique app identifier with format: APP-<timestamp>-<random>
// Why needed: provides human-readable unique identifiers for apps.
// Called from: app creation endpoints.
func GenerateAppID() (string, error) {
	// Generate 4 random bytes for uniqueness
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Create short random suffix
	randomPart := base64.URLEncoding.EncodeToString(b)[:6]
	appID := fmt.Sprintf("APP-%s", randomPart)

	return appID, nil
}
