package controllers

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// Cookies are Secure everywhere except plain-http local development, where a
// Secure cookie is dropped by Safari and the admin cannot stay signed in.
func TestIsSecureRequestIgnoresPort(t *testing.T) {
	app := fiber.New()
	app.Get("/", func(c *fiber.Ctx) error {
		if isSecureRequest(c) {
			return c.SendString("secure")
		}
		return c.SendString("plain")
	})
	for host, want := range map[string]string{
		"localhost":                     "plain",
		"localhost:5001":                "plain",
		"127.0.0.1:5001":                "plain",
		"[::1]:5001":                    "plain",
		"pay.phuentsholing.travel":      "secure",
		"pay.phuentsholing.travel:8443": "secure",
		"192.168.1.20:5001":             "secure",
	} {
		resp, err := app.Test(httptest.NewRequest("GET", "http://"+host+"/", nil))
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 16)
		n, _ := resp.Body.Read(b)
		if got := string(b[:n]); got != want {
			t.Errorf("%s: got %s, want %s", host, got, want)
		}
	}
}
