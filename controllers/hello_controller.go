package controllers

import (
	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/models"
)

// Hello is a basic sample endpoint returning a static greeting message.
// Why needed: quick smoke test for JSON serialization and route wiring.
// Response: 200 with models.Message JSON.
// Called from: route GET /api/hello in routes.Register.
func Hello(c *fiber.Ctx) error {
	response := models.Message{
		Message: "Hello from Fiber MVC",
	}

	return c.JSON(response)
}
