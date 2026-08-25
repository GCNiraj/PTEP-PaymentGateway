package routes

import (
	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/controllers"
	"example.com/fiber-mvc/middleware"
)

// Register wires all HTTP routes and connects each path to its controller.
// Why needed: centralizes endpoint mapping so request flow is predictable.
// Called from: main() after middleware/controller construction.
// Next flow: Fiber dispatches matching requests to mapped handlers.
func Register(app *fiber.App, businessController *controllers.BusinessController, txListController *controllers.TransactionsListController, authController *controllers.AuthController, logsController *controllers.LogsController, appsController *controllers.AppsController, appsManagementController *controllers.AppsManagementController, internationalController *controllers.InternationalController, merchantRoutingAdminController *controllers.MerchantRoutingAdminController, dbLogger fiber.Handler, stdoutLogger fiber.Handler, adminAuth fiber.Handler, apiKeyAuth fiber.Handler, adminDebugController *controllers.AdminDebugController, seedController *controllers.SeedController, dbDebugController *controllers.DBDebugController, bizRateLimiter fiber.Handler, otpRateLimiter fiber.Handler, adminWriteRateLimiter fiber.Handler, appCreateRateLimiter fiber.Handler) {
	_ = adminDebugController
	_ = seedController
	_ = dbDebugController
	api := app.Group("/api")

	api.Get("/health", controllers.Health)
	api.Get("/hello", controllers.Hello)
	api.Get("/admin/login/options", dbLogger, authController.LoginOptions)
	api.Post("/admin/login", dbLogger, authController.Login) // Login is intentionally exempt — no session exists pre-login.
	api.Post("/admin/logout", dbLogger, adminAuth, middleware.CSRFProtect(authController.CSRFCookieName, ""), authController.Logout)

	adminRead := api.Group("/admin", stdoutLogger, adminAuth)
	adminRead.Get("/session", authController.Session)
	adminRead.Get("/transactions", txListController.List)
	adminRead.Post("/transactions/detail", middleware.CSRFProtect(authController.CSRFCookieName, ""), txListController.GetDetailByBody)
	adminRead.Get("/transactions/:id", txListController.Get)
	adminRead.Post("/transactions/search", middleware.CSRFProtect(authController.CSRFCookieName, ""), txListController.ListByBody)
	adminRead.Get("/stats", txListController.Stats)
	adminRead.Get("/stats/daily", txListController.DailyStats)
	adminRead.Get("/apps", appsController.List)
	adminRead.Get("/logs", logsController.List)
	adminRead.Post("/logs/search", middleware.CSRFProtect(authController.CSRFCookieName, ""), logsController.ListByBody)
	adminRead.Get("/international/transactions", internationalController.List)
	adminRead.Post("/international/transactions/search", middleware.CSRFProtect(authController.CSRFCookieName, ""), internationalController.ListByBody)

	// App management endpoints (admin only) — rate limited per IP to prevent automated abuse
	// of Create/Update/Delete/RegenerateKey even behind session auth.
	adminWrite := api.Group("/admin", adminWriteRateLimiter, dbLogger, adminAuth, middleware.CSRFProtect(authController.CSRFCookieName, ""))
	adminWrite.Post("/apps/create", appCreateRateLimiter, appsManagementController.Create)
	adminRead.Get("/apps/:id", appsManagementController.Get)
	adminWrite.Put("/apps/:id", appsManagementController.Update)
	adminWrite.Delete("/apps/:id", appCreateRateLimiter, appsManagementController.Delete)
	adminWrite.Post("/apps/:id/regenerate-key", appCreateRateLimiter, appsManagementController.RegenerateKey)
	adminRead.Get("/apps/:id/stats", appsManagementController.GetStats)
	adminRead.Get("/recipients", merchantRoutingAdminController.ListRecipients)
	adminRead.Get("/recipient-mappings", merchantRoutingAdminController.ListMappings)
	adminRead.Get("/gateway-credentials", merchantRoutingAdminController.ListCredentials)
	adminWrite.Post("/recipients", merchantRoutingAdminController.CreateRecipient)
	adminWrite.Put("/recipients/:id", merchantRoutingAdminController.SetRecipientActive)
	adminWrite.Post("/recipient-mappings", merchantRoutingAdminController.CreateMapping)
	adminWrite.Put("/recipient-mappings/:id", merchantRoutingAdminController.SetMappingActive)
	adminWrite.Post("/gateway-credentials", merchantRoutingAdminController.CreateCredential)
	adminWrite.Post("/gateway-credentials/:id/rotate", merchantRoutingAdminController.RotateCredential)
	adminWrite.Post("/gateway-credentials/:id/activate", merchantRoutingAdminController.ActivateCredential)
	adminWrite.Post("/gateway-credentials/:id/disable", merchantRoutingAdminController.DisableCredential)

	// Business endpoints - rate limited per IP before API-key auth.
	// The OTP confirm endpoint carries an additional tighter per-IP limit to prevent OTP guessing.
	biz := api.Group("/business", bizRateLimiter, dbLogger, apiKeyAuth)
	biz.Post("/pull/initiate", businessController.PullPaymentInitiate)
	biz.Post("/pull/confirm", otpRateLimiter, businessController.PullPaymentConfirm)
	biz.Post("/intra/inquiry", businessController.IntraInquiry)
	// Disabled for now; keep handler wired in code for future re-enable.
	// biz.Post("/intra/transfer", businessController.IntraTransfer)
	biz.Post("/status/same-day", businessController.StatusSameDay)
	biz.Post("/status/later", businessController.StatusLater)
	// Disabled for now; keep handler wired in code for future re-enable.
	// biz.Post("/status/intra", businessController.StatusIntra)

	// International payment endpoints (v1 API) - rate limited per IP before API-key auth.
	v1 := api.Group("/v1", bizRateLimiter, dbLogger, apiKeyAuth)
	v1.Post("/payments", internationalController.CreatePayment)
	v1.Get("/payments/:reference_id/status", internationalController.CheckPaymentStatus)

}
