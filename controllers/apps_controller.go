package controllers

import (
	"log"
	"net/http"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/internal/storage"
)

// AppListItem is the safe response projection for GET /api/admin/apps.
// Fields excluded: api_key, api_secret, submerchant_id, dk_account, agency_name.
// Those fields are internal credentials / payment-processor config not needed by the UI list view.
type AppListItem struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	IsActive     bool    `json:"is_active"`
	ContactName  string  `json:"contact_name"`
	ContactEmail string  `json:"contact_email"`
	ContactPhone string  `json:"contact_phone"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	LastUsedAt   *string `json:"last_used_at,omitempty"`
}

func toAppListItem(a storage.ExternalApp) AppListItem {
	return AppListItem{
		ID:           a.ID,
		Name:         a.Name,
		IsActive:     a.IsActive,
		ContactName:  a.ContactName,
		ContactEmail: a.ContactEmail,
		ContactPhone: a.ContactPhone,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
		LastUsedAt:   a.LastUsedAt,
	}
}

type AppsController struct {
	Repo *storage.AppsRepository
}

// NewAppsController constructs the apps listing controller with repository dependency.
// Called from: main() during server bootstrap.
func NewAppsController(repo *storage.AppsRepository) *AppsController {
	return &AppsController{Repo: repo}
}

// List returns connected external apps for admin dashboards.
// Why needed: operators need visibility into registered app integrations.
// Sensitive fields (api_key, api_secret, submerchant_id, dk_account, agency_name)
// are stripped from the response — credentials must not be retrievable via GET after creation.
// Response:
// - 503 when DB is not configured
// - 500 on query failure
// - 200 with {"data":[...]} on success
// Called from: GET /api/admin/apps (protected by AdminAuth).
func (ctl *AppsController) List(c *fiber.Ctx) error {
	if ctl.Repo == nil || !ctl.Repo.Enabled() {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{"error": "database not configured"})
	}

	apps, err := ctl.Repo.List(c.UserContext(), 50)
	if err != nil {
		log.Printf("list apps failed: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": genericQueryError})
	}

	safe := make([]AppListItem, 0, len(apps))
	for _, a := range apps {
		safe = append(safe, toAppListItem(a))
	}

	return c.JSON(fiber.Map{"data": safe})
}
