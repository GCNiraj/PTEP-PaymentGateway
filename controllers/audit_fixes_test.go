package controllers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/middleware"
)

func body(t *testing.T, app *fiber.App, method, path, payload string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(payload))
	if payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

// FIX 1: the audited URL must be a 400 with a generic message.
func TestBadDateIsRejectedBeforeTheDatabase(t *testing.T) {
	_, _, err := parseDateRange("2026-08'-24", "")
	if err == nil {
		t.Fatal("the audited value was accepted and would have reached Postgres")
	}
	if err.Error() != "Invalid date range" {
		t.Errorf("message is %q", err.Error())
	}
	if _, _, err := parseDateRange("2026-09-01", "2026-08-01"); err == nil {
		t.Error("a range ending before it starts was accepted")
	}
	if _, _, err := parseDateRange("", ""); err != nil {
		t.Errorf("unbounded range rejected: %v", err)
	}
	if f, to, err := parseDateRange(" 2026-08-24 ", "2026-08-25"); err != nil || f != "2026-08-24" || to != "2026-08-25" {
		t.Errorf("a good range was mangled: %q %q %v", f, to, err)
	}
}

// FIX 1: the global handler must not echo a driver error.
func TestErrorHandlerHidesTheRealError(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: middleware.ErrorHandler})
	app.Get("/boom", func(c *fiber.Ctx) error {
		return errors.New(`ERROR: invalid input syntax for type date (SQLSTATE 22007)`)
	})
	app.Get("/nope", func(c *fiber.Ctx) error { return fiber.NewError(404, "app not found") })

	code, out := body(t, app, "GET", "/boom", "")
	if code != 500 {
		t.Errorf("status %d", code)
	}
	if out["error"] != "Something went wrong" {
		t.Errorf("client was told %q", out["error"])
	}
	if strings.Contains(out["error"].(string), "SQLSTATE") {
		t.Error("SQLSTATE leaked")
	}
	code, out = body(t, app, "GET", "/nope", "")
	if code != 404 || out["error"] != "app not found" {
		t.Errorf("a deliberate 4xx was swallowed: %d %v", code, out)
	}
}

// FIX 2: the exact payloads from the audit.
func TestAuditedInputsAreRejected(t *testing.T) {
	app := fiber.New()
	ctl := &AppsManagementController{}
	app.Post("/apps", ctl.Create)

	for name, payload := range map[string]string{
		"html injection in app name": `{"name":"<h1>HTML Injection</h1>"}`,
		"letters in the phone":       `{"name":"Good Name","contact_phone":"986543210saasa"}`,
		"not an email":               `{"name":"Good Name","contact_email":"notanemail"}`,
		"name of one character":      `{"name":"x"}`,
		"whitespace-only name":       `{"name":"   "}`,
	} {
		code, out := body(t, app, "POST", "/apps", payload)
		if code != 400 {
			t.Errorf("%s: status %d, want 400", name, code)
		}
		if msg, _ := out["error"].(string); strings.Contains(msg, "<h1>") || strings.Contains(msg, "saasa") {
			t.Errorf("%s: the response echoed the rejected input: %q", name, msg)
		}
	}
}

// FIX 2: payee credential format, including the 1060-only rule.
func TestCredentialFormat(t *testing.T) {
	ok := map[string]string{"beneficiary_account": "110158212197", "beneficiary_name": "Lhaki Hotels & Resorts (Pvt.) Ltd.", "beneficiary_bank": "1060"}
	if err := validateCredentialFormat("dkpg", ok); err != nil {
		t.Fatalf("a good payee was refused: %v", err)
	}
	for name, values := range map[string]map[string]string{
		"account with letters": {"beneficiary_account": "986543210saasa", "beneficiary_name": "Good", "beneficiary_bank": "1060"},
		"account too short":    {"beneficiary_account": "12345", "beneficiary_name": "Good", "beneficiary_bank": "1060"},
		"html in the name":     {"beneficiary_account": "110158212197", "beneficiary_name": "<h1>x</h1>", "beneficiary_bank": "1060"},
		"another bank":         {"beneficiary_account": "110158212197", "beneficiary_name": "Good", "beneficiary_bank": "1040"},
		"no bank at all":       {"beneficiary_account": "110158212197", "beneficiary_name": "Good", "beneficiary_bank": ""},
	} {
		if err := validateCredentialFormat("dkpg", values); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
