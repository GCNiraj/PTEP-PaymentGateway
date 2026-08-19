package middleware

import (
	"crypto/hmac"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v2"
)

const (
	DefaultCSRFCookieName = "csrf_token"
	DefaultCSRFHeaderName = "X-CSRF-Token"
)

// CSRFProtect implements the Double-Submit Cookie CSRF defence.
//
// How it works:
//   - On login, the server sets a non-HttpOnly "csrf_token" cookie containing a
//     random secret that the admin JavaScript can read.
//   - Every state-mutating request (POST / PUT / DELETE / PATCH) must echo that
//     value in the "X-CSRF-Token" request header.
//   - This middleware validates that both values are present and identical.
//
// Safe methods (GET, HEAD, OPTIONS) are passed through without validation.
//
// Why Double-Submit Cookie is sufficient here:
//   - Admin session cookie is SameSite=Strict → already blocks most CSRF in
//     modern browsers.  This middleware adds defence-in-depth for older browsers
//     and satisfies explicit penetration-test requirements.
//   - The csrf_token cookie is also SameSite=Strict, so an attacker on a
//     different origin cannot read or predict it.
//
// Called from: routes.Register for adminWrite group and logout endpoint.
func CSRFProtect(cookieName, headerName string) fiber.Handler {
	cookieName = strings.TrimSpace(cookieName)
	if cookieName == "" {
		cookieName = DefaultCSRFCookieName
	}
	headerName = strings.TrimSpace(headerName)
	if headerName == "" {
		headerName = DefaultCSRFHeaderName
	}

	return func(c *fiber.Ctx) error {
		method := strings.ToUpper(strings.TrimSpace(c.Method()))

		// Safe methods do not need CSRF protection.
		switch method {
		case "GET", "HEAD", "OPTIONS":
			return c.Next()
		}

		tokenFromHeader := strings.TrimSpace(c.Get(headerName))
		tokenFromCookie := strings.TrimSpace(c.Cookies(cookieName))

		if tokenFromHeader == "" {
			return c.Status(http.StatusForbidden).JSON(fiber.Map{
				"error": "csrf token missing",
				"hint":  "Include the " + headerName + " header with your request",
			})
		}

		if tokenFromCookie == "" {
			return c.Status(http.StatusForbidden).JSON(fiber.Map{
				"error": "csrf token missing",
				"hint":  "No CSRF cookie present; please re-authenticate",
			})
		}

		// Constant-time comparison prevents timing-based token inference.
		if !hmac.Equal([]byte(tokenFromHeader), []byte(tokenFromCookie)) {
			return c.Status(http.StatusForbidden).JSON(fiber.Map{
				"error": "csrf token mismatch",
			})
		}

		return c.Next()
	}
}
