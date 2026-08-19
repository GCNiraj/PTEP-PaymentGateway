package controllers

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	gormdb "example.com/fiber-mvc/internal/storage/gorm"
)

type AdminSignupController struct {
	DB        *gorm.DB
	SignupKey string
}

type AdminSignupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"` // #nosec G117 -- field name is required by signup request JSON contract.
}

// NewAdminSignupController builds backend-only admin signup handler.
func NewAdminSignupController(db *gorm.DB, signupKey string) *AdminSignupController {
	return &AdminSignupController{
		DB:        db,
		SignupKey: strings.TrimSpace(signupKey),
	}
}

// Signup creates a new admin user with bcrypt password hashing.
// Access control:
// - route should be private/unguessable (configured in env)
// - request must include X-Signup-Key header matching ADMIN_SIGNUP_KEY
// Called from: main() custom internal signup path.
func (ctl *AdminSignupController) Signup(c *fiber.Ctx) error {
	if ctl == nil || ctl.DB == nil || strings.TrimSpace(ctl.SignupKey) == "" {
		return c.SendStatus(http.StatusNotFound)
	}

	if !secureEquals(strings.TrimSpace(c.Get("X-Signup-Key")), ctl.SignupKey) {
		// Uniform not-found response avoids disclosing endpoint validity.
		time.Sleep(120 * time.Millisecond)
		return c.SendStatus(http.StatusNotFound)
	}

	var req AdminSignupRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}

	username := strings.TrimSpace(req.Username)
	password := req.Password
	if username != "" {
		c.Locals("admin_username", username)
	}
	if username == "" || strings.TrimSpace(password) == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "username and password are required"})
	}

	if err := gormdb.CreateAdminUser(ctl.DB, username, password); err != nil {
		switch {
		case errors.Is(err, gormdb.ErrAdminUserExists):
			return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "admin username already exists"})
		case isValidationErr(err):
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		default:
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "failed to create admin user"})
		}
	}

	c.Set("Cache-Control", "no-store")
	return c.Status(http.StatusCreated).JSON(fiber.Map{
		"ok":       true,
		"username": username,
		"message":  "admin user created",
	})
}

func secureEquals(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aHash := sha256.Sum256([]byte(a))
	bHash := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aHash[:], bHash[:]) == 1
}

func isValidationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "username") || strings.Contains(msg, "password")
}
