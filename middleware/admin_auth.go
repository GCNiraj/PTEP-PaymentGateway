package middleware

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/internal/auth"
)

// AdminAuth enforces Bearer JWT authentication for protected admin routes.
// Why needed: prevents unauthorized access to admin-only APIs.
// Expected header: Authorization: Bearer <token>.
// Responses: 401 missing/invalid auth header or invalid JWT.
// Called from: routes.Register when creating /api/admin route group.
func AdminAuth(jwtMgr *auth.Manager, sessions *auth.SessionManager, cookieName string) fiber.Handler {
	cookieName = strings.TrimSpace(cookieName)
	if cookieName == "" {
		cookieName = "admin_session"
	}
	return func(c *fiber.Ctx) error {
		tokenValue, err := resolveToken(c, cookieName)
		if err != nil || tokenValue == "" {
			return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}

		claims, err := verifyAdminSession(jwtMgr, sessions, tokenValue)
		if err != nil {
			return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "invalid token"})
		}
		c.Locals("admin_username", claims.Username)
		c.Locals("admin_session_id", claims.SessionID)
		c.Locals("admin_claims", claims)

		return c.Next()
	}
}

// AdminPageAuth protects admin HTML pages and redirects unauthenticated users to login.
func AdminPageAuth(jwtMgr *auth.Manager, sessions *auth.SessionManager, cookieName, loginPath string) fiber.Handler {
	cookieName = strings.TrimSpace(cookieName)
	if cookieName == "" {
		cookieName = "admin_session"
	}
	loginPath = strings.TrimSpace(loginPath)
	if loginPath == "" {
		loginPath = "/login"
	}

	return func(c *fiber.Ctx) error {
		tokenValue := strings.TrimSpace(c.Cookies(cookieName))
		if tokenValue == "" {
			return c.Redirect(loginPath, http.StatusFound)
		}

		claims, err := verifyAdminSession(jwtMgr, sessions, tokenValue)
		if err != nil {
			clearAuthCookie(c, cookieName)
			return c.Redirect(loginPath, http.StatusFound)
		}
		c.Locals("admin_username", claims.Username)
		c.Locals("admin_session_id", claims.SessionID)
		c.Locals("admin_claims", claims)
		return c.Next()
	}
}

func resolveToken(c *fiber.Ctx, cookieName string) (string, error) {
	headerToken := bearerToken(strings.TrimSpace(c.Get("Authorization")))
	cookieToken := strings.TrimSpace(c.Cookies(cookieName))

	if headerToken != "" && cookieToken != "" && headerToken != cookieToken {
		return "", errors.New("conflicting auth tokens")
	}
	if headerToken != "" {
		return headerToken, nil
	}
	if cookieToken != "" {
		return cookieToken, nil
	}
	return "", errors.New("missing auth token")
}

func bearerToken(authHeader string) string {
	if authHeader == "" {
		return ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func verifyAdminSession(jwtMgr *auth.Manager, sessions *auth.SessionManager, tokenValue string) (*auth.Claims, error) {
	if jwtMgr == nil || sessions == nil {
		return nil, errors.New("auth not configured")
	}
	claims, err := jwtMgr.Verify(tokenValue)
	if err != nil {
		return nil, err
	}
	username := strings.TrimSpace(claims.Username)
	sessionID := strings.TrimSpace(claims.SessionID)
	if username == "" || sessionID == "" {
		return nil, errors.New("invalid session claims")
	}
	if !sessions.Validate(sessionID, username) {
		return nil, errors.New("session is not active")
	}
	return claims, nil
}

func clearAuthCookie(c *fiber.Ctx, cookieName string) {
	c.Cookie(&fiber.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HTTPOnly: true,
		Secure:   isSecureRequest(c),
		SameSite: "Strict",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
	})
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
