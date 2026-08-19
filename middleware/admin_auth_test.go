package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/internal/auth"
)

const (
	testCookieName = "admin_session_test"
	testUsername   = "admin"
)

func newTestJWTManager() *auth.Manager {
	return &auth.Manager{
		Secret:   "unit-test-jwt-secret",
		TTL:      time.Minute,
		Issuer:   "unit-test",
		Audience: "unit-test",
	}
}

func issueToken(t *testing.T, mgr *auth.Manager, sessions *auth.SessionManager, username string) (string, string) {
	t.Helper()
	sessionID, err := sessions.Create(username)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	token, err := mgr.Generate(username, sessionID)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}
	return token, sessionID
}

func TestAdminAuthAllowsValidCookieSession(t *testing.T) {
	t.Parallel()

	mgr := newTestJWTManager()
	sessions := auth.NewSessionManager(time.Minute)
	token, _ := issueToken(t, mgr, sessions, testUsername)

	app := fiber.New()
	app.Get("/protected", AdminAuth(mgr, sessions, testCookieName), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: token, Path: "/"})

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}
}

func TestAdminAuthRejectsRevokedSession(t *testing.T) {
	t.Parallel()

	mgr := newTestJWTManager()
	sessions := auth.NewSessionManager(time.Minute)
	token, sessionID := issueToken(t, mgr, sessions, testUsername)
	sessions.Revoke(sessionID)

	app := fiber.New()
	app.Get("/protected", AdminAuth(mgr, sessions, testCookieName), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: token, Path: "/"})

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", res.StatusCode)
	}
}

func TestAdminAuthRejectsExpiredSession(t *testing.T) {
	t.Parallel()

	mgr := newTestJWTManager()
	sessions := auth.NewSessionManager(25 * time.Millisecond)
	token, _ := issueToken(t, mgr, sessions, testUsername)
	time.Sleep(40 * time.Millisecond)

	app := fiber.New()
	app.Get("/protected", AdminAuth(mgr, sessions, testCookieName), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: token, Path: "/"})

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", res.StatusCode)
	}
}

func TestAdminAuthRejectsConflictingHeaderAndCookieTokens(t *testing.T) {
	t.Parallel()

	mgr := newTestJWTManager()
	sessions := auth.NewSessionManager(time.Minute)
	cookieToken, _ := issueToken(t, mgr, sessions, testUsername)
	headerToken, _ := issueToken(t, mgr, sessions, testUsername)

	app := fiber.New()
	app.Get("/protected", AdminAuth(mgr, sessions, testCookieName), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+headerToken)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: cookieToken, Path: "/"})

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", res.StatusCode)
	}
}

func TestAdminPageAuthRedirectsWithoutCookie(t *testing.T) {
	t.Parallel()

	mgr := newTestJWTManager()
	sessions := auth.NewSessionManager(time.Minute)

	app := fiber.New()
	app.Get("/dashboard", AdminPageAuth(mgr, sessions, testCookieName, "/login"), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusFound {
		t.Fatalf("expected status 302, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != "/login" {
		t.Fatalf("expected redirect to /login, got %q", got)
	}
}

func TestAdminPageAuthClearsCookieOnInvalidSession(t *testing.T) {
	t.Parallel()

	mgr := newTestJWTManager()
	sessions := auth.NewSessionManager(time.Minute)
	token, sessionID := issueToken(t, mgr, sessions, testUsername)
	sessions.Revoke(sessionID)

	app := fiber.New()
	app.Get("/dashboard", AdminPageAuth(mgr, sessions, testCookieName, "/login"), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: token, Path: "/"})

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if res.StatusCode != http.StatusFound {
		t.Fatalf("expected status 302, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != "/login" {
		t.Fatalf("expected redirect to /login, got %q", got)
	}
	setCookie := res.Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, testCookieName+"=") {
		t.Fatalf("expected cookie clear header for %s, got %q", testCookieName, setCookie)
	}
}
