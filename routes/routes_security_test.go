package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/controllers"
)

func TestRemovedDKPGDisclosureAndProxyRoutesReturnNotFound(t *testing.T) {
	app := fiber.New()
	next := func(c *fiber.Ctx) error { return c.Next() }
	Register(app,
		nil, nil, &controllers.AuthController{CSRFCookieName: "csrf_token"}, nil, nil, nil, nil, nil,
		next, next, next, next,
		nil, nil, nil,
		next, next, next, next,
	)

	for _, path := range []string{
		"/api/dkpg/auth/token",
		"/api/dkpg/sign/key",
		"/api/dkpg/account-auth/pull-payment",
		"/api/dkpg/debit-request/pull-payment",
		"/api/dkpg/beneficiary/account-inquiry",
		"/api/dkpg/transaction/status",
		"/api/dkpg/transactions/status",
	} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		response, err := app.Test(request)
		if err != nil {
			t.Fatalf("%s: app.Test() error = %v", path, err)
		}
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want %d", path, response.StatusCode, http.StatusNotFound)
		}
	}
}
