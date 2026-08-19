package middleware_test

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/middleware"
)

// newTestApp creates a minimal Fiber app with ProxyHeader set (mirroring main.go)
// and an always-200 handler to detect when the redirect was NOT issued.
func newTestApp(enabled bool) *fiber.App {
	app := fiber.New(fiber.Config{
		ProxyHeader: fiber.HeaderXForwardedProto,
	})
	app.Use(middleware.HTTPSRedirect(enabled))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
	return app
}

// TestHTTPSRedirect_HTTP_RedirectsToHTTPS verifies that a plain-HTTP request
// (signalled by X-Forwarded-Proto: http) is redirected to https with status 301.
func TestHTTPSRedirect_HTTP_RedirectsToHTTPS(t *testing.T) {
	app := newTestApp(true)

	req := httptest.NewRequest("GET", "http://pay.dashbhutan.com/test", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Host = "pay.dashbhutan.com"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusMovedPermanently {
		t.Errorf("expected 301, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	expected := "https://pay.dashbhutan.com/test"
	if loc != expected {
		t.Errorf("expected Location %q, got %q", expected, loc)
	}
}

// TestHTTPSRedirect_HTTPS_PassesThrough verifies that a request already over
// HTTPS (X-Forwarded-Proto: https) is NOT redirected.
func TestHTTPSRedirect_HTTPS_PassesThrough(t *testing.T) {
	app := newTestApp(true)

	req := httptest.NewRequest("GET", "https://pay.dashbhutan.com/test", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Host = "pay.dashbhutan.com"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestHTTPSRedirect_Disabled_NoRedirect verifies that when enabled=false the
// middleware is a no-op — HTTP requests pass through without a redirect.
func TestHTTPSRedirect_Disabled_NoRedirect(t *testing.T) {
	app := newTestApp(false)

	req := httptest.NewRequest("GET", "http://pay.dashbhutan.com/test", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Host = "pay.dashbhutan.com"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("expected 200 when disabled, got %d", resp.StatusCode)
	}
}

// TestHTTPSRedirect_Localhost_NoRedirect verifies that requests from a localhost
// host are never redirected, even when HTTPS redirect is enabled.
func TestHTTPSRedirect_Localhost_NoRedirect(t *testing.T) {
	app := newTestApp(true)

	req := httptest.NewRequest("GET", "http://localhost/test", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Host = "localhost"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("expected 200 for localhost, got %d", resp.StatusCode)
	}
}

// TestHTTPSRedirect_Loopback_NoRedirect verifies that loopback IP requests
// (e.g., 127.0.0.1) are also exempted from the redirect.
func TestHTTPSRedirect_Loopback_NoRedirect(t *testing.T) {
	app := newTestApp(true)

	req := httptest.NewRequest("GET", "http://127.0.0.1/test", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Host = "127.0.0.1"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("expected 200 for loopback IP, got %d", resp.StatusCode)
	}
}

// TestHTTPSRedirect_PreservesPathAndQuery verifies that the redirect target URL
// includes the original path and query string intact.
func TestHTTPSRedirect_PreservesPathAndQuery(t *testing.T) {
	app := newTestApp(true)

	req := httptest.NewRequest("GET", "http://pay.dashbhutan.com/login?next=%2Fdashboard", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Host = "pay.dashbhutan.com"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusMovedPermanently {
		t.Errorf("expected 301, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	expected := "https://pay.dashbhutan.com/login?next=%2Fdashboard"
	if loc != expected {
		t.Errorf("expected Location %q, got %q", expected, loc)
	}
}
