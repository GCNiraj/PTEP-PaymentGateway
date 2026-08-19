package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// okHandler is a simple 200 OK handler used to verify that the middleware
// allows the request through.
func okHandlerCSRF(c *fiber.Ctx) error { return c.SendStatus(http.StatusOK) }

func newCSRFTestApp(cookieName, headerName string) *fiber.App {
	app := fiber.New()
	app.Use(CSRFProtect(cookieName, headerName))
	app.Post("/mutate", okHandlerCSRF)
	app.Put("/mutate", okHandlerCSRF)
	app.Delete("/mutate", okHandlerCSRF)
	app.Patch("/mutate", okHandlerCSRF)
	app.Get("/safe", okHandlerCSRF)
	return app
}

const (
	testCSRFCookie = "csrf_token"
	testCSRFHeader = "X-CSRF-Token"
	testCSRFToken  = "test-csrf-token-value-abc123"
)

// ── Safe methods bypass ───────────────────────────────────────────────────────

func TestCSRFAllowsGETWithoutToken(t *testing.T) {
	t.Parallel()
	app := newCSRFTestApp(testCSRFCookie, testCSRFHeader)
	req := httptest.NewRequest(http.MethodGet, "/safe", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for GET, got %d", res.StatusCode)
	}
}

func TestCSRFAllowsHEADWithoutToken(t *testing.T) {
	t.Parallel()
	app := newCSRFTestApp(testCSRFCookie, testCSRFHeader)
	req := httptest.NewRequest(http.MethodHead, "/safe", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	// Fiber returns 200 for HEAD on a route that has a GET handler, but 405
	// when there's no explicit HEAD route.  We only care it's not 403.
	if res.StatusCode == http.StatusForbidden {
		t.Fatalf("expected CSRF to skip HEAD, got 403")
	}
}

func TestCSRFAllowsOPTIONSWithoutToken(t *testing.T) {
	t.Parallel()
	app := newCSRFTestApp(testCSRFCookie, testCSRFHeader)
	req := httptest.NewRequest(http.MethodOptions, "/mutate", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode == http.StatusForbidden {
		t.Fatalf("expected CSRF to skip OPTIONS, got 403")
	}
}

// ── POST with valid matching token ────────────────────────────────────────────

func TestCSRFAllowsPostWithMatchingToken(t *testing.T) {
	t.Parallel()
	app := newCSRFTestApp(testCSRFCookie, testCSRFHeader)
	req := httptest.NewRequest(http.MethodPost, "/mutate", nil)
	req.AddCookie(&http.Cookie{Name: testCSRFCookie, Value: testCSRFToken})
	req.Header.Set(testCSRFHeader, testCSRFToken)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for valid CSRF, got %d", res.StatusCode)
	}
}

func TestCSRFAllowsPutWithMatchingToken(t *testing.T) {
	t.Parallel()
	app := newCSRFTestApp(testCSRFCookie, testCSRFHeader)
	req := httptest.NewRequest(http.MethodPut, "/mutate", nil)
	req.AddCookie(&http.Cookie{Name: testCSRFCookie, Value: testCSRFToken})
	req.Header.Set(testCSRFHeader, testCSRFToken)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for valid CSRF on PUT, got %d", res.StatusCode)
	}
}

func TestCSRFAllowsDeleteWithMatchingToken(t *testing.T) {
	t.Parallel()
	app := newCSRFTestApp(testCSRFCookie, testCSRFHeader)
	req := httptest.NewRequest(http.MethodDelete, "/mutate", nil)
	req.AddCookie(&http.Cookie{Name: testCSRFCookie, Value: testCSRFToken})
	req.Header.Set(testCSRFHeader, testCSRFToken)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for valid CSRF on DELETE, got %d", res.StatusCode)
	}
}

// ── POST with missing header ──────────────────────────────────────────────────

func TestCSRFRejectsPostMissingHeader(t *testing.T) {
	t.Parallel()
	app := newCSRFTestApp(testCSRFCookie, testCSRFHeader)
	req := httptest.NewRequest(http.MethodPost, "/mutate", nil)
	req.AddCookie(&http.Cookie{Name: testCSRFCookie, Value: testCSRFToken})
	// No X-CSRF-Token header
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when header missing, got %d", res.StatusCode)
	}
}

// ── POST with missing cookie ──────────────────────────────────────────────────

func TestCSRFRejectsPostMissingCookie(t *testing.T) {
	t.Parallel()
	app := newCSRFTestApp(testCSRFCookie, testCSRFHeader)
	req := httptest.NewRequest(http.MethodPost, "/mutate", nil)
	// No csrf_token cookie
	req.Header.Set(testCSRFHeader, testCSRFToken)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when cookie missing, got %d", res.StatusCode)
	}
}

// ── POST with mismatched token ────────────────────────────────────────────────

func TestCSRFRejectsPostMismatchedToken(t *testing.T) {
	t.Parallel()
	app := newCSRFTestApp(testCSRFCookie, testCSRFHeader)
	req := httptest.NewRequest(http.MethodPost, "/mutate", nil)
	req.AddCookie(&http.Cookie{Name: testCSRFCookie, Value: testCSRFToken})
	req.Header.Set(testCSRFHeader, "wrong-token-value")
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for token mismatch, got %d", res.StatusCode)
	}
}

// ── Default names are applied when empty strings are passed ───────────────────

func TestCSRFUsesDefaultNamesWhenEmpty(t *testing.T) {
	t.Parallel()
	// Pass empty strings — middleware should fall back to "csrf_token" / "X-CSRF-Token"
	app := fiber.New()
	app.Use(CSRFProtect("", ""))
	app.Post("/mutate", okHandlerCSRF)

	req := httptest.NewRequest(http.MethodPost, "/mutate", nil)
	req.AddCookie(&http.Cookie{Name: DefaultCSRFCookieName, Value: testCSRFToken})
	req.Header.Set(DefaultCSRFHeaderName, testCSRFToken)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with default names, got %d", res.StatusCode)
	}
}

// ── Burp Suite Pentest Reproduction ──────────────────────────────────────────

func TestBurpSuiteReplayAttack(t *testing.T) {
	t.Parallel()
	
	// Replicate the exact scenario: Admin session is valid, but CSRF token is missing in the request
	// (e.g. attacker sends raw request via Burp without knowing the CSRF token)
	
	app := fiber.New()
	adminWrite := app.Group("/admin", CSRFProtect(testCSRFCookie, testCSRFHeader))
	adminWrite.Post("/apps/create", func(c *fiber.Ctx) error {
		return c.Status(http.StatusCreated).JSON(fiber.Map{"success": "app created"})
	})

	// Scenario: Attacker intercepts request and replays it, or tricks user browser into sending it.
	// The browser automatically attaches the admin_session cookie (and maybe even the csrf_token
	// cookie depending on SameSite/subdomain contexts during the attack window),
	// BUT the browser WILL NOT automatically add the X-CSRF-Token header in a cross-site request.
	
	req := httptest.NewRequest(http.MethodPost, "/admin/apps/create", strings.NewReader(`{"name":"Malicious App"}`))
	req.Header.Set("Content-Type", "application/json")
	
	// Simulate what the browser attaches automatically for the targeted admin
	req.AddCookie(&http.Cookie{Name: "admin_session", Value: "valid-jwt-token"})
	req.AddCookie(&http.Cookie{Name: testCSRFCookie, Value: testCSRFToken})
	
	// CRITICAL: The X-CSRF-Token header is NOT present in the request because the attacker 
	// cannot force the victim's browser to send custom headers cross-origin.
	
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	
	// The server MUST block this request as a CSRF attack.
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("CRITICAL: Burp suite replay attack succeeded! Expected 403 Forbidden, got %d", res.StatusCode)
	}
}
