package controllers

import (
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	gormdb "example.com/fiber-mvc/internal/storage/gorm"
)

type SeedController struct {
	DB *gorm.DB
}

// NewSeedController constructs admin seed endpoint handler.
// Called from: main() wiring.
func NewSeedController(db *gorm.DB) *SeedController {
	return &SeedController{DB: db}
}

// SeedFakeTransactions inserts sample payment rows for local/demo testing.
// Why needed: provides quick UI/data verification without external DKPG calls.
// Response:
// - 500 when DB is unavailable or insert fails
// - 200 with {"ok":true,"inserted":N} on success
// Called from: POST /api/admin/seed/transactions (protected by AdminAuth).
func (ctl *SeedController) SeedFakeTransactions(c *fiber.Ctx) error {
	if ctl.DB == nil {
		return c.Status(500).JSON(fiber.Map{"error": "db not available"})
	}

	now := time.Now()
	samples := []gormdb.PaymentTransaction{
		{
			ExternalAppID:      "APP-001",
			OrderID:            "ORDER-1001",
			StanNumber:         "020111571912",
			BFSTxnID:           "523400081332",
			Amount:             5000,
			TransactionFee:     5,
			RemitterAccount:    "770182571",
			RemitterName:       "John Doe",
			RemitterPhone:      "17811440",
			RemitterBank:       "1040",
			BeneficiaryAccount: "110158212197",
			PaymentDesc:        "Invoice #1001",
			Currency:           "BTN",
			Status:             "Success",
			CreatedAt:          now.Add(-10 * time.Minute),
			UpdatedAt:          now.Add(-10 * time.Minute),
		},
		{
			ExternalAppID:      "APP-002",
			OrderID:            "ORDER-1002",
			StanNumber:         "020111572000",
			BFSTxnID:           "523400081333",
			Amount:             12000,
			TransactionFee:     0,
			RemitterAccount:    "770182572",
			RemitterName:       "Jane Smith",
			RemitterPhone:      "17654321",
			RemitterBank:       "1040",
			BeneficiaryAccount: "110158212197",
			PaymentDesc:        "Invoice #1002",
			Currency:           "BTN",
			Status:             "Failed",
			CreatedAt:          now.Add(-30 * time.Minute),
			UpdatedAt:          now.Add(-30 * time.Minute),
		},
	}

	if err := ctl.DB.Create(&samples).Error; err != nil {
		log.Printf("seed sample transactions failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"error": genericQueryError})
	}

	return c.JSON(fiber.Map{"ok": true, "inserted": len(samples)})
}
