package middleware_test

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/middleware"
)

// Wires the app exactly as main.go does, and checks the things that only show
// up once BodyLimit, ErrorHandler and SecurityHeaders are present together.
func newAppLikeMain() *fiber.App {
	app := fiber.New(fiber.Config{
		ProxyHeader:  fiber.HeaderXForwardedProto,
		BodyLimit:    64 * 1024,
		ErrorHandler: middleware.ErrorHandler,
	})
	app.Use(middleware.SecurityHeaders(middleware.SecurityHeadersOptions{Enabled: true, CSPEnabled: true}))
	app.Post("/api/admin/login", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"ok": true}) })
	app.Get("/robots.txt", func(c *fiber.Ctx) error {
		c.Type("txt")
		return c.SendString("User-agent: *\nDisallow: /\n")
	})
	app.Static("/vendor", "../views/vendor", fiber.Static{
		Browse: false, CacheDuration: 24 * time.Hour, MaxAge: int((365 * 24 * time.Hour).Seconds()),
	})
	return app
}

func TestBodyLimitAcceptsRealBodiesAndRefusesLargeOnes(t *testing.T) {
	app := newAppLikeMain()

	small := httptest.NewRequest("POST", "/api/admin/login", strings.NewReader(`{"u":"a"}`))
	small.Header.Set("Content-Type", "application/json")
	res, _ := app.Test(small, -1)
	if res.StatusCode != 200 {
		t.Errorf("a normal login body was refused: %d", res.StatusCode)
	}

	big := httptest.NewRequest("POST", "/api/admin/login", strings.NewReader(strings.Repeat("a", 100*1024)))
	big.Header.Set("Content-Type", "application/json")
	res, err := app.Test(big, -1)
	if err != nil {
		t.Logf("100KB body -> transport error (connection refused/closed): %v", err)
	} else {
		raw, _ := io.ReadAll(res.Body)
		t.Logf("100KB body -> %d %s", res.StatusCode, strings.TrimSpace(string(raw)))
		if res.StatusCode != 413 {
			t.Errorf("a 100KB body got %d, want 413", res.StatusCode)
		}
	}

	// 63KB must still pass: the limit has to sit above anything legitimate.
	ok := httptest.NewRequest("POST", "/api/admin/login", strings.NewReader(`{"x":"`+strings.Repeat("a", 63*1024)+`"}`))
	ok.Header.Set("Content-Type", "application/json")
	res, _ = app.Test(ok, -1)
	if res.StatusCode != 200 {
		t.Errorf("a 63KB body got %d, want 200", res.StatusCode)
	}
}

func TestCSPDoesNotAllowJsdelivr(t *testing.T) {
	const want = "default-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'; " +
		"object-src 'none'; script-src 'self' https://challenges.cloudflare.com; " +
		"style-src 'self' https://fonts.googleapis.com; img-src 'self' data:; " +
		"font-src 'self' https://fonts.gstatic.com data:; " +
		"connect-src 'self' https://challenges.cloudflare.com; " +
		"frame-src 'self' https://challenges.cloudflare.com"

	app := newAppLikeMain()
	for _, path := range []string{"/robots.txt", "/vendor/bootstrap.min.css"} {
		res, _ := app.Test(httptest.NewRequest("GET", path, nil))
		got := res.Header.Get("Content-Security-Policy")
		if got != want {
			t.Errorf("%s CSP mismatch:\n got %q\nwant %q", path, got, want)
		}
		if strings.Contains(got, "jsdelivr") {
			t.Errorf("%s still allows jsdelivr", path)
		}
	}
	t.Logf("CSP confirmed identical on page and static routes")
}

// Every origin the two pages actually reach must be permitted by the policy.
func TestCSPAllowsEveryOriginThePagesUse(t *testing.T) {
	app := newAppLikeMain()
	res, _ := app.Test(httptest.NewRequest("GET", "/robots.txt", nil))
	csp := res.Header.Get("Content-Security-Policy")

	// styles.css does @import from fonts.googleapis.com, which then pulls font
	// files from fonts.gstatic.com. login.js injects the Turnstile script,
	// which connects to and frames challenges.cloudflare.com.
	need := map[string]string{
		"style-src":   "https://fonts.googleapis.com",
		"font-src":    "https://fonts.gstatic.com",
		"script-src":  "https://challenges.cloudflare.com",
		"connect-src": "https://challenges.cloudflare.com",
		"frame-src":   "https://challenges.cloudflare.com",
	}
	for directive, origin := range need {
		var found bool
		for _, part := range strings.Split(csp, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, directive+" ") && strings.Contains(part, origin) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not permit %s", directive, origin)
		}
	}
}
