package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/internal/auth"
)

// newRateLimitApp is a test helper that sets up a minimal Fiber app with
// RateLimit middleware applied to a single GET /test handler.
func newRateLimitApp(limiter *auth.IPRateLimiter) *fiber.App {
	app := fiber.New()
	app.Get("/test", RateLimit(limiter), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})
	return app
}

func TestRateLimitMiddleware_AllowsUnderLimit(t *testing.T) {
	t.Parallel()
	limiter := auth.NewIPRateLimiter(3, time.Minute)
	app := newRateLimitApp(limiter)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		res, err := app.Test(req)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, res.StatusCode)
		}
	}
}

func TestRateLimitMiddleware_BlocksOverLimit(t *testing.T) {
	t.Parallel()
	const max = 2
	limiter := auth.NewIPRateLimiter(max, time.Minute)
	app := newRateLimitApp(limiter)

	for i := 0; i < max; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		res, err := app.Test(req)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, res.StatusCode)
		}
	}

	// (max+1)th request must be blocked.
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("over-limit request: %v", err)
	}
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", res.StatusCode)
	}

	// Retry-After header must be present and non-empty.
	if got := res.Header.Get("Retry-After"); got == "" {
		t.Fatal("expected non-empty Retry-After header on 429 response")
	}

	// Response body must include error and retry_after_seconds fields.
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode 429 body: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Error("expected \"error\" field in 429 body")
	}
	if _, ok := body["retry_after_seconds"]; !ok {
		t.Error("expected \"retry_after_seconds\" field in 429 body")
	}
}

func TestRateLimitMiddleware_NilLimiter(t *testing.T) {
	t.Parallel()
	// A nil limiter must be a pure no-op — all requests pass through.
	app := newRateLimitApp(nil)

	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		res, err := app.Test(req)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("request %d: nil limiter must allow all, got %d", i+1, res.StatusCode)
		}
	}
}
