package controllers

import (
	"database/sql"
	"encoding/base64"
	"strings"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/internal/common"
	"example.com/fiber-mvc/internal/storage"
	"example.com/fiber-mvc/internal/validation"
)

// AppsManagementController handles CRUD operations for external applications.
// Why needed: allows admins to manage which external apps can access the payment gateway.
// Endpoints: Create, List, Get, Update, RegenerateKey, GetStats.
type AppsManagementController struct {
	repo *storage.AppsRepository
}

// NewAppsManagementController constructs the apps management controller.
// Called from: main() and injected into routes.
func NewAppsManagementController(repo *storage.AppsRepository) *AppsManagementController {
	return &AppsManagementController{repo: repo}
}

// NewAppsManagementControllerWithOptions is kept for backward compatibility.
// Webhook URL validation mode is no longer used because webhook_url has been removed from the admin API surface.
func NewAppsManagementControllerWithOptions(repo *storage.AppsRepository, webhookURLValidationMode string) *AppsManagementController {
	_ = webhookURLValidationMode
	return NewAppsManagementController(repo)
}

// CreateAppRequest represents the request body for creating a new app.
//
// The tags are the whole of the server-side check. Before them this endpoint
// stored "<h1>HTML Injection</h1>" as an app name and "986543210saasa" as a
// phone number (ASD Cyber Security, 23 September 2026, finding V2).
type CreateAppRequest struct {
	Name         string `json:"name"          validate:"required,min=2,max=100,safetext"`
	ContactName  string `json:"contact_name"  validate:"omitempty,min=2,max=100,safetext"`
	ContactEmail string `json:"contact_email" validate:"omitempty,email,max=254"`
	ContactPhone string `json:"contact_phone" validate:"omitempty,phonedigits"`
}

// trim removes surrounding whitespace before validation, so that " " fails
// `required` rather than passing it, and a pasted value with a trailing newline
// is accepted rather than rejected for a character nobody can see.
func (r *CreateAppRequest) trim() {
	r.Name = strings.TrimSpace(r.Name)
	r.ContactName = strings.TrimSpace(r.ContactName)
	r.ContactEmail = strings.TrimSpace(r.ContactEmail)
	r.ContactPhone = strings.TrimSpace(r.ContactPhone)
}

// CreateAppResponse represents the response after creating an app.
type CreateAppResponse struct {
	AppID     string `json:"app_id"`
	Name      string `json:"name"`
	APIKey    string `json:"api_key"`
	APISecret string `json:"api_secret"`
	CreatedAt string `json:"created_at"`
}

// Create creates a new external application with API credentials.
// POST /api/admin/apps
func (c *AppsManagementController) Create(ctx *fiber.Ctx) error {
	var req CreateAppRequest
	if err := ctx.BodyParser(&req); err != nil {
		return ctx.Status(400).JSON(fiber.Map{
			"error": "invalid request body",
		})
	}

	req.trim()
	if err := validation.Struct(&req); err != nil {
		return ctx.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	// Generate app ID
	appID, err := common.GenerateAppID()
	if err != nil {
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to generate app ID",
		})
	}

	// Generate API key (plain text)
	apiKey, err := common.GenerateAPIKey()
	if err != nil {
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to generate API key",
		})
	}

	// Generate API secret (plain text)
	apiSecret, err := common.GenerateAPISecret()
	if err != nil {
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to generate API secret",
		})
	}

	// Hash the API key for storage
	hashedAPIKey, err := common.HashAPIKey(apiKey)
	if err != nil {
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to hash API key",
		})
	}

	// Create app record (webhook_url intentionally removed from admin API surface)
	app := storage.ExternalApp{
		ID:           appID,
		Name:         req.Name,
		APIKey:       hashedAPIKey, // Store hashed version
		APISecret:    apiSecret,    // Store plain secret for webhook verification
		WebhookURL:   "",
		IsActive:     true,
		ContactName:  req.ContactName,
		ContactEmail: req.ContactEmail,
		ContactPhone: req.ContactPhone,
	}

	if err := c.repo.Create(ctx.Context(), app); err != nil {
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to create app",
		})
	}

	// Return the newly generated credentials EXACTLY ONCE so the admin can copy them.
	// encodeCredential: base64-encodes plaintext and injects "*" markers in encoded form.
	// Frontend strips "*" before atob() to recover plaintext.
	return ctx.Status(201).JSON(CreateAppResponse{
		AppID:     appID,
		Name:      req.Name,
		APIKey:    encodeCredential(apiKey),
		APISecret: encodeCredential(apiSecret),
		CreatedAt: "now",
	})
}

// UpdateAppRequest represents the request body for updating an app.
//
// The rules are identical to CreateAppRequest's on purpose: an edit endpoint
// that accepts what its create endpoint refuses is the same hole with one extra
// step.
type UpdateAppRequest struct {
	Name         string `json:"name"          validate:"required,min=2,max=100,safetext"`
	IsActive     bool   `json:"is_active"`
	ContactName  string `json:"contact_name"  validate:"omitempty,min=2,max=100,safetext"`
	ContactEmail string `json:"contact_email" validate:"omitempty,email,max=254"`
	ContactPhone string `json:"contact_phone" validate:"omitempty,phonedigits"`
}

func (r *UpdateAppRequest) trim() {
	r.Name = strings.TrimSpace(r.Name)
	r.ContactName = strings.TrimSpace(r.ContactName)
	r.ContactEmail = strings.TrimSpace(r.ContactEmail)
	r.ContactPhone = strings.TrimSpace(r.ContactPhone)
}

// Update modifies an existing external app.
// PUT /api/admin/apps/:id
func (c *AppsManagementController) Update(ctx *fiber.Ctx) error {
	appID := ctx.Params("id")
	if appID == "" {
		return ctx.Status(400).JSON(fiber.Map{
			"error": "app ID is required",
		})
	}

	var req UpdateAppRequest
	if err := ctx.BodyParser(&req); err != nil {
		return ctx.Status(400).JSON(fiber.Map{
			"error": "invalid request body",
		})
	}

	req.trim()
	if err := validation.Struct(&req); err != nil {
		return ctx.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	// Check if app exists
	existing, err := c.repo.FindByID(ctx.Context(), appID)
	if err != nil {
		if err == sql.ErrNoRows {
			return ctx.Status(404).JSON(fiber.Map{
				"error": "app not found",
			})
		}
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to find app",
		})
	}

	if existing == nil {
		return ctx.Status(404).JSON(fiber.Map{
			"error": "app not found",
		})
	}

	// Update app record (preserve stored webhook_url for backward compatibility; field is no longer exposed via admin API)
	app := storage.ExternalApp{
		ID:           appID,
		Name:         req.Name,
		WebhookURL:   existing.WebhookURL,
		IsActive:     req.IsActive,
		ContactName:  req.ContactName,
		ContactEmail: req.ContactEmail,
		ContactPhone: req.ContactPhone,
	}

	if err := c.repo.Update(ctx.Context(), app); err != nil {
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to update app",
		})
	}

	return ctx.JSON(fiber.Map{
		"message": "app updated successfully",
		"app_id":  appID,
	})
}

// AppDetailItem is the safe response projection for GET /api/admin/apps/:id.
// Includes a masked api_key_hint (first 8 chars + "...") for UI display.
// Fields excluded: full api_key hash, api_secret, submerchant_id, dk_account, agency_name.
type AppDetailItem struct {
	AppListItem
	APIKeyHint string `json:"api_key_hint"` // first 8 chars of the stored key for UI hint only
}

func toAppDetailItem(a storage.ExternalApp) AppDetailItem {
	return AppDetailItem{
		AppListItem: toAppListItem(a),
		APIKeyHint:  maskAPIKeyHint(a.APIKey),
	}
}

// maskAPIKeyHint returns the first 8 characters of the key followed by "********"
// so the admin UI can confirm which key is in use without exposing the full value.
func maskAPIKeyHint(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 8 {
		return "********"
	}
	return key[:8] + "********"
}

// encodeCredential base64-encodes the full credential value and injects "**"
// clusters at fixed positions in the encoded string. The frontend strips '*'
// before atob() to recover the original plaintext credential.
func encodeCredential(cred string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(cred)))
	if len(b64) <= 8 {
		return "**" + b64 + "**"
	}
	q := len(b64) / 4
	return "**" + b64[:q] + "**" + b64[q:q*2] + "**" + b64[q*2:q*3] + "**" + b64[q*3:] + "**"
}

// Get retrieves a specific app by ID.
// Sensitive fields (api_key hash, api_secret, submerchant_id, dk_account, agency_name)
// are omitted from the response. Only a short hint of the api_key is included for UI display.
// GET /api/admin/apps/:id
func (c *AppsManagementController) Get(ctx *fiber.Ctx) error {
	appID := ctx.Params("id")
	if appID == "" {
		return ctx.Status(400).JSON(fiber.Map{
			"error": "app ID is required",
		})
	}

	app, err := c.repo.FindByID(ctx.Context(), appID)
	if err != nil {
		if err == sql.ErrNoRows {
			return ctx.Status(404).JSON(fiber.Map{
				"error": "app not found",
			})
		}
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to find app",
		})
	}

	if app == nil {
		return ctx.Status(404).JSON(fiber.Map{
			"error": "app not found",
		})
	}

	return ctx.JSON(toAppDetailItem(*app))
}

// Delete removes a specific app by ID.
// DELETE /api/admin/apps/:id
func (c *AppsManagementController) Delete(ctx *fiber.Ctx) error {
	appID := ctx.Params("id")
	if appID == "" {
		return ctx.Status(400).JSON(fiber.Map{"error": "app ID is required"})
	}

	app, err := c.repo.FindByID(ctx.Context(), appID)
	if err != nil {
		if err == sql.ErrNoRows {
			return ctx.Status(404).JSON(fiber.Map{"error": "app not found"})
		}
		return ctx.Status(500).JSON(fiber.Map{"error": "failed to find app"})
	}

	if app == nil {
		return ctx.Status(404).JSON(fiber.Map{"error": "app not found"})
	}

	if err := c.repo.Delete(ctx.Context(), appID); err != nil {
		return ctx.Status(500).JSON(fiber.Map{"error": "failed to delete app"})
	}

	return ctx.JSON(fiber.Map{"message": "app deleted successfully"})
}

// RegenerateKey generates a new API key for an app.
// POST /api/admin/apps/:id/regenerate-key
func (c *AppsManagementController) RegenerateKey(ctx *fiber.Ctx) error {
	appID := ctx.Params("id")
	if appID == "" {
		return ctx.Status(400).JSON(fiber.Map{
			"error": "app ID is required",
		})
	}

	// Check if app exists
	existing, err := c.repo.FindByID(ctx.Context(), appID)
	if err != nil || existing == nil {
		return ctx.Status(404).JSON(fiber.Map{
			"error": "app not found",
		})
	}

	// Generate new API key
	newAPIKey, err := common.GenerateAPIKey()
	if err != nil {
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to generate API key",
		})
	}

	// Hash the new API key
	hashedAPIKey, err := common.HashAPIKey(newAPIKey)
	if err != nil {
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to hash API key",
		})
	}

	// Update the API key in database
	if err := c.repo.UpdateAPIKey(ctx.Context(), appID, hashedAPIKey); err != nil {
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to update API key",
		})
	}

	// Return the newly generated API key.
	// encodeCredential: base64-encodes plaintext and injects "*" markers in encoded form.
	// Frontend strips "*" before atob() to recover plaintext.
	return ctx.JSON(fiber.Map{
		"message":    "API key regenerated successfully",
		"app_id":     appID,
		"api_key":    encodeCredential(newAPIKey),
		"api_secret": encodeCredential(existing.APISecret),
	})
}

// GetStats retrieves statistics for a specific app.
// GET /api/admin/apps/:id/stats
func (c *AppsManagementController) GetStats(ctx *fiber.Ctx) error {
	appID := ctx.Params("id")
	if appID == "" {
		return ctx.Status(400).JSON(fiber.Map{
			"error": "app ID is required",
		})
	}

	stats, err := c.repo.GetStats(ctx.Context(), appID)
	if err != nil {
		return ctx.Status(500).JSON(fiber.Map{
			"error": "failed to get stats",
		})
	}

	return ctx.JSON(stats)
}

// maskAPIKey is kept for backward compatibility in tests.
func maskAPIKey(key string) string {
	return maskAPIKeyHint(key)
}
