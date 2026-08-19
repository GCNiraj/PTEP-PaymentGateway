package middleware_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/middleware"
)

func newSecurityHeadersTestApp(opts middleware.SecurityHeadersOptions) *fiber.App {
	app := fiber.New(fiber.Config{
		ProxyHeader: fiber.HeaderXForwardedProto,
	})
	app.Use(middleware.SecurityHeaders(opts))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
	return app
}

func TestSecurityHeaders_EnforcedCSPAndHSTSOnHTTPS(t *testing.T) {
	app := newSecurityHeadersTestApp(middleware.SecurityHeadersOptions{
		Enabled:    true,
		CSPEnabled: true,
	})

	req := httptest.NewRequest("GET", "https://pay.dashbhutan.com/test", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Host = "pay.dashbhutan.com"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Security-Policy"); got == "" {
		t.Fatal("expected Content-Security-Policy header")
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Fatalf("expected default-src in CSP, got %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy-Report-Only"); got != "" {
		t.Fatalf("expected no CSP report-only header in enforce mode, got %q", got)
	}
	if got := resp.Header.Get("Strict-Transport-Security"); got == "" {
		t.Fatal("expected HSTS on HTTPS responses")
	}
	if got := resp.Header.Get("Strict-Transport-Security"); got != "max-age=31536000; includeSubDomains; preload" {
		t.Fatalf("unexpected HSTS value: %q", got)
	}
}

func TestSecurityHeaders_ReportOnlyWithReportURI(t *testing.T) {
	app := newSecurityHeadersTestApp(middleware.SecurityHeadersOptions{
		Enabled:       true,
		CSPEnabled:    true,
		CSPReportOnly: true,
		CSPReportURI:  "/api/csp/report",
	})

	req := httptest.NewRequest("GET", "https://pay.dashbhutan.com/test", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Host = "pay.dashbhutan.com"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Security-Policy"); got != "" {
		t.Fatalf("expected no enforce CSP header in report-only mode, got %q", got)
	}
	got := resp.Header.Get("Content-Security-Policy-Report-Only")
	if got == "" {
		t.Fatal("expected Content-Security-Policy-Report-Only header")
	}
	if !strings.Contains(got, "report-uri /api/csp/report") {
		t.Fatalf("expected report-uri in CSP report-only header, got %q", got)
	}
}

func TestSecurityHeaders_NoHSTSOnHTTP(t *testing.T) {
	app := newSecurityHeadersTestApp(middleware.SecurityHeadersOptions{
		Enabled:    true,
		CSPEnabled: true,
	})

	req := httptest.NewRequest("GET", "http://pay.dashbhutan.com/test", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Host = "pay.dashbhutan.com"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("expected no HSTS on HTTP response, got %q", got)
	}
}

func TestSecurityHeaders_DisabledSkipsAllHeaders(t *testing.T) {
	app := newSecurityHeadersTestApp(middleware.SecurityHeadersOptions{
		Enabled:    false,
		CSPEnabled: true,
	})

	req := httptest.NewRequest("GET", "https://pay.dashbhutan.com/test", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Host = "pay.dashbhutan.com"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("X-Content-Type-Options"); got != "" {
		t.Fatalf("expected no X-Content-Type-Options when disabled, got %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got != "" {
		t.Fatalf("expected no CSP when disabled, got %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy-Report-Only"); got != "" {
		t.Fatalf("expected no CSP report-only when disabled, got %q", got)
	}
}

func TestSecurityHeaders_StripsTechnologyDisclosureHeaders(t *testing.T) {
	app := fiber.New(fiber.Config{
		ProxyHeader: fiber.HeaderXForwardedProto,
	})
	app.Use(middleware.SecurityHeaders(middleware.SecurityHeadersOptions{
		Enabled:    true,
		CSPEnabled: true,
	}))
	app.Get("/test", func(c *fiber.Ctx) error {
		c.Set("Server", "nginx/1.18.0")
		c.Set("X-Powered-By", "Express")
		c.Set("X-AspNet-Version", "4.0.30319")
		c.Set("X-AspNetMvc-Version", "5.2")
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest("GET", "https://pay.dashbhutan.com/test", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Host = "pay.dashbhutan.com"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Server"); got != "" {
		t.Fatalf("expected Server header to be stripped, got %q", got)
	}
	if got := resp.Header.Get("X-Powered-By"); got != "" {
		t.Fatalf("expected X-Powered-By header to be stripped, got %q", got)
	}
	if got := resp.Header.Get("X-AspNet-Version"); got != "" {
		t.Fatalf("expected X-AspNet-Version header to be stripped, got %q", got)
	}
	if got := resp.Header.Get("X-AspNetMvc-Version"); got != "" {
		t.Fatalf("expected X-AspNetMvc-Version header to be stripped, got %q", got)
	}
}
