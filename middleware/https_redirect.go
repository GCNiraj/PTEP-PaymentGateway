package middleware

import (
	"net/netip"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// HTTPSRedirect returns a middleware that issues a 301 Moved Permanently redirect
// from HTTP to HTTPS for every request that did not arrive over a secure channel.
//
// Why needed: prevents credentials (and all data) from being transmitted in plain
// text when users or integrations access the service over HTTP.
//
// Behaviour:
//   - When enabled=false the middleware is a no-op (useful for local dev).
//   - Loopback addresses (127.x, ::1) and the "localhost" hostname are always
//     exempted so local development continues to work even when enabled=true.
//   - The scheme check honours the X-Forwarded-Proto header (set by Fiber when
//     fiber.Config.ProxyHeader = fiber.HeaderXForwardedProto) so the middleware
//     works correctly behind TLS-terminating reverse proxies.
//
// Registration: must be the first global middleware in main.go so that every
// request — including the login page and all API routes — is covered.
func HTTPSRedirect(enabled bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !enabled {
			return c.Next()
		}

		// Skip redirect for loopback / localhost origins.
		if isLoopbackHost(c.Hostname()) {
			return c.Next()
		}

		// c.Protocol() returns the value of fiber.Config.ProxyHeader when set,
		// falling back to the transport-level scheme. On a TLS-terminating proxy
		// this will be "https" for secure requests and "http" for plain requests.
		if strings.EqualFold(c.Protocol(), "https") {
			return c.Next()
		}

		// Build the HTTPS target URL preserving path, query, and fragment.
		target := "https://" + c.Hostname() + string(c.Request().RequestURI())
		return c.Redirect(target, fiber.StatusMovedPermanently)
	}
}

// isLoopbackHost returns true for "localhost" and any loopback IP address,
// ensuring HTTPS redirect is never applied to local development traffic.
func isLoopbackHost(hostname string) bool {
	hostname = strings.TrimSpace(hostname)
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	if addr, err := netip.ParseAddr(hostname); err == nil {
		return addr.IsLoopback()
	}
	return false
}
