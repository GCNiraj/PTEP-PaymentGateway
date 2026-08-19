package auth

import (
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Manager struct {
	Secret   string // #nosec G117 -- field name is intentional and internal to JWT manager semantics.
	TTL      time.Duration
	Issuer   string
	Audience string
}

type Claims struct {
	Username  string `json:"username"`
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// Generate issues a signed JWT for the given admin username.
// Why needed: admin endpoints are protected by bearer authentication.
// Called from: AuthController.Login.
func (m *Manager) Generate(username, sessionID string) (string, error) {
	if m == nil {
		return "", errors.New("jwt manager is nil")
	}
	if m.Secret == "" {
		return "", errors.New("jwt secret is empty")
	}
	username = strings.TrimSpace(username)
	sessionID = strings.TrimSpace(sessionID)
	if username == "" {
		return "", errors.New("username is required")
	}
	if sessionID == "" {
		return "", errors.New("session id is required")
	}
	ttl := m.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	now := time.Now().UTC()
	claims := Claims{
		Username:  username,
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Subject:   username,
			Issuer:    m.Issuer,
			ID:        sessionID,
		},
	}
	if m.Audience != "" {
		claims.Audience = []string{m.Audience}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(m.Secret))
}

// Verify parses and validates a JWT token string using configured secret.
// Called from: middleware.AdminAuth on protected admin requests.
// Returns parsed claims on success or error on invalid/expired tokens.
func (m *Manager) Verify(tokenString string) (*Claims, error) {
	if m == nil {
		return nil, errors.New("jwt manager is nil")
	}
	if m.Secret == "" {
		return nil, errors.New("jwt secret is empty")
	}
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithLeeway(5 * time.Second),
	}
	if m.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(m.Issuer))
	}
	if m.Audience != "" {
		opts = append(opts, jwt.WithAudience(m.Audience))
	}
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		return []byte(m.Secret), nil
	}, opts...)
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, jwt.ErrTokenInvalidClaims
	}
	return claims, nil
}
