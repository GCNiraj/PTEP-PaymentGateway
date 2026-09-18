package controllers

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/config"
)

// DebugConfig returns a masked subset of runtime DKPG configuration.
// Why needed: helps validate env wiring without exposing secrets.
// Response: 200 with flags/masked values showing config readiness.
// Called from: route GET /api/debug/config.
func DebugConfig(c *fiber.Ctx) error {
	cfg := config.Load()
	maskedKey := mask(cfg.DKPGAPIKey)
	return c.JSON(fiber.Map{
		"dkpg_base_url":       cfg.DKPGBaseURL,
		"dkpg_api_key_set":    cfg.DKPGAPIKey != "",
		"dkpg_api_key_masked": maskedKey,
		"dkpg_username_set":   cfg.DKPGUsername != "",
		"dkpg_client_id_set":  cfg.DKPGClientID != "",
	})
}

// mask hides sensitive values while preserving last 4 chars for identification.
// Called from: DebugConfig.
func mask(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return strings.Repeat("*", len(s))
	}
	return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
}
