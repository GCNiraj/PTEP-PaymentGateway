package controllers

import "github.com/gofiber/fiber/v2"

// Health is a lightweight liveness endpoint.
// Why needed: external monitors and load balancers can quickly verify API uptime.
// Response: 200 with {"status":"ok"}.
// Called from: route GET /api/health in routes.Register.
func Health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status": "ok",
	})
}
