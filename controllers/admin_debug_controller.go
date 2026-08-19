package controllers

import (
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	gormdb "example.com/fiber-mvc/internal/storage/gorm"
)

type AdminDebugController struct {
	DB *gorm.DB
}

// NewAdminDebugController constructs DB debug handler for admin-user checks.
// Called from: main() wiring stage.
func NewAdminDebugController(db *gorm.DB) *AdminDebugController {
	return &AdminDebugController{DB: db}
}

// Status reports whether GORM DB is available and how many admin users exist.
// Why needed: quick diagnostics for admin auth data availability.
// Response: always 200 with debug payload.
// Called from: GET /api/debug/admin-users.
func (ctl *AdminDebugController) Status(c *fiber.Ctx) error {
	if ctl.DB == nil {
		return c.JSON(fiber.Map{"db": "nil"})
	}

	var count int64
	_ = ctl.DB.Model(&gormdb.AdminUser{}).Count(&count).Error

	return c.JSON(fiber.Map{
		"db": "ok",
		"admin_users": count,
	})
}
