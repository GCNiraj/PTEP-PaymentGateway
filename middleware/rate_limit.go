package middleware

import (
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/internal/auth"
)

// RateLimit returns a Fiber middleware that enforces a per-IP sliding-window
// rate limit using the provided *auth.IPRateLimiter.
// Why needed: prevents brute-force, OTP guessing, automated payment abuse, and
// DoS attacks on critical payment API endpoints.
// Response on limit exceeded: HTTP 429 with Retry-After header and JSON body.
// A nil limiter is a no-op (always passes through) — safe to use in tests.
// Called from: routes.Register for /api/business/*, /api/dkpg/*, /api/v1/*.
func RateLimit(limiter *auth.IPRateLimiter) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if limiter == nil {
			return c.Next()
		}

		ok, retryAfter := limiter.Allow(c.IP())
		if ok {
			return c.Next()
		}

		secs := retryAfterSeconds(retryAfter)
		c.Set("Retry-After", fmt.Sprintf("%d", secs))
		return c.Status(http.StatusTooManyRequests).JSON(fiber.Map{
			"error":               "too many requests",
			"retry_after_seconds": secs,
		})
	}
}

// retryAfterSeconds converts a duration to a whole-number of seconds,
// rounding up and returning at least 1.
func retryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	return int(math.Ceil(d.Seconds()))
}
