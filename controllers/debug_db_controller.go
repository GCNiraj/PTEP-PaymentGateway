package controllers

import (
	"database/sql"
	"net/http"

	"github.com/gofiber/fiber/v2"
)

type DBDebugController struct {
	DB *sql.DB
}

// NewDBDebugController constructs low-level SQL DB debug endpoint handler.
// Called from: main() setup.
func NewDBDebugController(db *sql.DB) *DBDebugController {
	return &DBDebugController{DB: db}
}

// Status checks database connectivity and payment_transactions row availability.
// Why needed: fast operational signal without requiring full admin dashboard flow.
// Response: always 200 with db_connected flag and optional error/count data.
// Called from: GET /api/debug/db.
func (ctl *DBDebugController) Status(c *fiber.Ctx) error {
	if ctl == nil || ctl.DB == nil {
		return c.Status(http.StatusOK).JSON(fiber.Map{
			"db_connected": false,
			"error":        "DB is nil",
		})
	}

	var count int64
	if err := ctl.DB.QueryRowContext(c.UserContext(), "select count(*) from payment_transactions").Scan(&count); err != nil {
		return c.Status(http.StatusOK).JSON(fiber.Map{
			"db_connected": false,
			"error":        err.Error(),
		})
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{
		"db_connected": true,
		"payment_transactions_count": count,
	})
}
