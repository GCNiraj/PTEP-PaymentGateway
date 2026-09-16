package controllers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/internal/auth"
	gormdb "example.com/fiber-mvc/internal/storage/gorm"
	"gorm.io/gorm"
)

type AuthController struct {
	JWT            *auth.Manager
	Sessions       *auth.SessionManager
	DB             *gorm.DB
	Limiter        *auth.LoginLimiter
	Captcha        *auth.CaptchaVerifier
	CookieName     string
	CSRFCookieName string // non-HttpOnly cookie read by admin JS; empty → "csrf_token"
}

// csrfCookieName returns the configured CSRF cookie name or "csrf_token".
func (ctl *AuthController) csrfCookieName() string {
	name := strings.TrimSpace(ctl.CSRFCookieName)
	if name == "" {
		return "csrf_token"
	}
	return name
}

// generateCSRFToken returns a 32-byte cryptographically random hex string.
func generateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type LoginRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"` // #nosec G117 -- field name is required by login request JSON contract.
	CaptchaToken string `json:"captcha_token"`
}

func (ctl *AuthController) sessionCookieName() string {
	if ctl == nil {
		return "admin_session"
	}
	name := strings.TrimSpace(ctl.CookieName)
	if name == "" {
		return "admin_session"
	}
	return name
}

func bearerTokenFromHeader(authHeader string) string {
	authHeader = strings.TrimSpace(authHeader)
	if authHeader == "" {
		return ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(parts[0]), "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func requestToken(c *fiber.Ctx, cookieName string) string {
	headerToken := bearerTokenFromHeader(c.Get("Authorization"))
	if headerToken != "" {
		return headerToken
	}
	return strings.TrimSpace(c.Cookies(cookieName))
}

// logIncomingSessionRequest prints a temporary debug line for incoming
// /api/admin/session requests without exposing sensitive token values.
func logIncomingSessionRequest(c *fiber.Ctx) {
	if c == nil {
		return
	}
	fmt.Printf(
		"[debug] incoming session request method=%s path=%s ip=%s request_id=%s has_auth_header=%t has_cookie_header=%t user_agent=%q\n",
		c.Method(),
		c.OriginalURL(),
		c.IP(),
		strings.TrimSpace(c.Get("X-Request-Id")),
		strings.TrimSpace(c.Get(fiber.HeaderAuthorization)) != "",
		strings.TrimSpace(c.Get(fiber.HeaderCookie)) != "",
		strings.TrimSpace(c.Get(fiber.HeaderUserAgent)),
	)
}

func isSecureRequest(c *fiber.Ctx) bool {
	if strings.EqualFold(c.Protocol(), "https") {
		return true
	}
	hostname := strings.TrimSpace(c.Hostname())
	if hostname == "" {
		return false
	}
	if hostname == "localhost" {
		return false
	}
	if addr, err := netip.ParseAddr(hostname); err == nil {
		return !addr.IsLoopback()
	}
	return true
}

func limiterKey(c *fiber.Ctx, username string) string {
	return strings.ToLower(strings.TrimSpace(username)) + "|" + strings.TrimSpace(c.IP())
}

func retryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	return int(math.Ceil(d.Seconds()))
}

func (ctl *AuthController) failedAttempts(key string) int {
	if ctl == nil || ctl.Limiter == nil {
		return 0
	}
	return ctl.Limiter.FailureCount(key)
}

func (ctl *AuthController) captchaRequired(key string) bool {
	if ctl == nil || ctl.Captcha == nil {
		return false
	}
	return ctl.Captcha.RequiresCaptcha(ctl.failedAttempts(key))
}

func (ctl *AuthController) loginErrorResponse(c *fiber.Ctx, status int, message, key string) error {
	return c.Status(status).JSON(fiber.Map{
		"error":            message,
		"failed_attempts":  ctl.failedAttempts(key),
		"captcha_required": ctl.captchaRequired(key),
	})
}

// LoginOptions exposes non-sensitive settings needed by the login UI.
func (ctl *AuthController) LoginOptions(c *fiber.Ctx) error {
	if ctl == nil || ctl.Captcha == nil {
		return c.JSON(fiber.Map{
			"captcha_enabled":        false,
			"captcha_site_key":       "",
			"captcha_after_failures": 0,
		})
	}
	enabled, siteKey, afterFailures := ctl.Captcha.PublicConfig()
	return c.JSON(fiber.Map{
		"captcha_enabled":        enabled,
		"captcha_site_key":       siteKey,
		"captcha_after_failures": afterFailures,
	})
}

// Login validates admin credentials and issues a JWT bearer token.
// Why needed: protects admin dashboards and data APIs.
// Request fields: username, password.
// Responses:
// - 400 invalid/missing JSON fields
// - 401 invalid credentials
// - 500 token generation failure
// - 200 token payload (access_token, token_type, expires_in)
// Called from: POST /api/admin/login.
// Next flow: token is verified by middleware.AdminAuth for /api/admin/* routes.
func (ctl *AuthController) Login(c *fiber.Ctx) error {
	if ctl == nil || ctl.JWT == nil || ctl.Sessions == nil || ctl.DB == nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "auth is not configured"})
	}

	// Reject login attempts over plain HTTP to prevent credentials from being
	// transmitted in readable form. The HTTPS redirect (301) alone is insufficient
	// because the POST body is already sent before the redirect is processed.
	// Loopback/localhost is exempted so local development continues to work.
	if !strings.EqualFold(c.Protocol(), "https") {
		isLocal := false
		hostname := strings.TrimSpace(c.Hostname())
		// Hostname() carries the port ("localhost:5001"), which matches neither
		// the literal below nor an address parse. Strip it, or the loopback
		// exemption never applies and local development cannot sign in at all.
		if host, _, err := net.SplitHostPort(hostname); err == nil {
			hostname = host
		}
		if strings.EqualFold(hostname, "localhost") {
			isLocal = true
		} else if addr, err := netip.ParseAddr(hostname); err == nil && addr.IsLoopback() {
			isLocal = true
		}
		if !isLocal {
			return c.Status(http.StatusForbidden).JSON(fiber.Map{
				"error": "login requires a secure connection (HTTPS)",
			})
		}
	}

	var req LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}

	// Try to decode base64 credentials sent by updated frontend.
	// Fallback to raw string if decoding fails (e.g. cached login.js).
	username := req.Username
	if dec, err := base64.StdEncoding.DecodeString(req.Username); err == nil && len(dec) > 0 {
		username = string(dec)
	}

	password := req.Password
	if dec, err := base64.StdEncoding.DecodeString(req.Password); err == nil && len(dec) > 0 {
		password = string(dec)
	}

	username = strings.TrimSpace(username)
	if username == "" || strings.TrimSpace(password) == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "username and password required"})
	}
	c.Locals("admin_username", username)
	if len(password) > 256 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "password is too long"})
	}

	key := limiterKey(c, username)
	if ctl.Limiter != nil {
		if ok, retry := ctl.Limiter.Allow(key); !ok {
			secs := retryAfterSeconds(retry)
			c.Set("Retry-After", fmt.Sprintf("%d", secs))
			c.Locals("admin_username", username)
			return c.Status(http.StatusTooManyRequests).JSON(fiber.Map{
				"error":               "too many login attempts",
				"retry_after_seconds": secs,
				"failed_attempts":     ctl.failedAttempts(key),
				"captcha_required":    ctl.captchaRequired(key),
			})
		}
	}

	if ctl.captchaRequired(key) {
		token := strings.TrimSpace(req.CaptchaToken)
		if token == "" {
			return ctl.loginErrorResponse(c, http.StatusBadRequest, "captcha required", key)
		}
		ok, err := ctl.Captcha.Verify(c.Context(), token, c.IP())
		if err != nil {
			return ctl.loginErrorResponse(c, http.StatusServiceUnavailable, "captcha verification unavailable", key)
		}
		if !ok {
			if ctl.Limiter != nil {
				locked, retry := ctl.Limiter.RegisterFailure(key)
				if locked {
					secs := retryAfterSeconds(retry)
					c.Set("Retry-After", fmt.Sprintf("%d", secs))
					c.Locals("admin_username", username)
					return c.Status(http.StatusTooManyRequests).JSON(fiber.Map{
						"error":               "too many login attempts",
						"retry_after_seconds": secs,
						"failed_attempts":     ctl.failedAttempts(key),
						"captcha_required":    ctl.captchaRequired(key),
					})
				}
			}
			return ctl.loginErrorResponse(c, http.StatusBadRequest, "captcha validation failed", key)
		}
	}

	ok, err := gormdb.VerifyAdminUser(ctl.DB, username, password)
	if err != nil || !ok {
		if ctl.Limiter != nil {
			locked, retry := ctl.Limiter.RegisterFailure(key)
			if locked {
				secs := retryAfterSeconds(retry)
				c.Set("Retry-After", fmt.Sprintf("%d", secs))
				c.Locals("admin_username", username)
				return c.Status(http.StatusTooManyRequests).JSON(fiber.Map{
					"error":               "too many login attempts",
					"retry_after_seconds": secs,
					"failed_attempts":     ctl.failedAttempts(key),
					"captcha_required":    ctl.captchaRequired(key),
				})
			}
		}
		return ctl.loginErrorResponse(c, http.StatusUnauthorized, "invalid credentials", key)
	}
	if ctl.Limiter != nil {
		ctl.Limiter.RegisterSuccess(key)
	}

	// Keep exactly one active admin session per user by revoking older sessions on new login.
	ctl.Sessions.RevokeUser(username)
	sessionID, err := ctl.Sessions.Create(username)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "session creation failed"})
	}

	token, err := ctl.JWT.Generate(username, sessionID)
	if err != nil {
		ctl.Sessions.Revoke(sessionID)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "token generation failed"})
	}
	ttl := ctl.JWT.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}

	c.Cookie(&fiber.Cookie{
		Name:     ctl.sessionCookieName(),
		Value:    token,
		Path:     "/",
		HTTPOnly: true,
		Secure:   isSecureRequest(c),
		SameSite: "Strict",
		MaxAge:   int(ttl.Seconds()),
		Expires:  time.Now().UTC().Add(ttl),
	})

	// Set the CSRF double-submit cookie.
	// HTTPOnly=false so the admin dashboard JS can read and echo it in the
	// X-CSRF-Token request header.  SameSite=Strict prevents a cross-site
	// attacker from reading or sending it.
	csrfToken, err := generateCSRFToken()
	if err != nil {
		ctl.Sessions.Revoke(sessionID)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "csrf token generation failed"})
	}
	c.Cookie(&fiber.Cookie{
		Name:     ctl.csrfCookieName(),
		Value:    csrfToken,
		Path:     "/",
		HTTPOnly: false,
		Secure:   isSecureRequest(c),
		SameSite: "Strict",
		MaxAge:   int(ttl.Seconds()),
		Expires:  time.Now().UTC().Add(ttl),
	})

	c.Set("Cache-Control", "no-store")

	// Do not return the session token in the response body; it is set in an HttpOnly
	// cookie only. Returning it in the body would expose it to logs, browser storage,
	// and any consumer of the API response (CWE-200 / sensitive information disclosure).
	return c.JSON(fiber.Map{
		"ok":         true,
		"expires_in": int(ttl.Seconds()),
	})
}

// Logout clears the admin session cookie.
func (ctl *AuthController) Logout(c *fiber.Ctx) error {
	if ctl != nil && ctl.JWT != nil && ctl.Sessions != nil {
		tokenValue := requestToken(c, ctl.sessionCookieName())
		if tokenValue != "" {
			if claims, err := ctl.JWT.Verify(tokenValue); err == nil && claims != nil && strings.TrimSpace(claims.SessionID) != "" {
				if username := strings.TrimSpace(claims.Username); username != "" {
					c.Locals("admin_username", username)
				}
				ctl.Sessions.Revoke(claims.SessionID)
			}
		}
	}

	c.Cookie(&fiber.Cookie{
		Name:     ctl.sessionCookieName(),
		Value:    "",
		Path:     "/",
		HTTPOnly: true,
		Secure:   isSecureRequest(c),
		SameSite: "Strict",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
	})
	// Clear CSRF cookie on logout.
	c.Cookie(&fiber.Cookie{
		Name:     ctl.csrfCookieName(),
		Value:    "",
		Path:     "/",
		HTTPOnly: false,
		Secure:   isSecureRequest(c),
		SameSite: "Strict",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
	})
	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"ok": true})
}

// Session returns the current authenticated admin session metadata.
// session_id is intentionally omitted from the response (internal opaque identifier
// not required by the admin UI and flagged as unnecessary exposure by security assessment).
func (ctl *AuthController) Session(c *fiber.Ctx) error {
	logIncomingSessionRequest(c)

	username, _ := c.Locals("admin_username").(string)
	username = strings.TrimSpace(username)
	if username == "" {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	resp := fiber.Map{
		"authenticated": true,
		"username":      username,
	}

	if claims, ok := c.Locals("admin_claims").(*auth.Claims); ok && claims != nil && claims.ExpiresAt != nil {
		resp["expires_at"] = claims.ExpiresAt.Time.UTC().Format(time.RFC3339)
	}

	return c.JSON(resp)
}
