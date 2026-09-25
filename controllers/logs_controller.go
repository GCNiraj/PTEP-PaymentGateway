package controllers

import (
	"log"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/internal/storage"
)

// SafeLogEntry is the safe response projection for GET /api/admin/logs.
// Fields excluded: request_body, response_body (may contain credentials/financial data),
// ip (client PII), actor_id (internal username).
// Only operational/monitoring metadata is returned.
type SafeLogEntry struct {
	ID         int64  `json:"id"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	Duration   int64  `json:"duration_ms"`
	RequestID  string `json:"request_id"`
	APISurface string `json:"api_surface"`
	Route      string `json:"route"`
	AuthResult string `json:"auth_result"`
	CreatedAt  string `json:"created_at"`
}

func toSafeLogEntry(e storage.LogEntry) SafeLogEntry {
	return SafeLogEntry{
		ID:         e.ID,
		Method:     e.Method,
		Path:       e.Path,
		Status:     e.Status,
		Duration:   e.Duration,
		RequestID:  e.RequestID,
		APISurface: e.APISurface,
		Route:      e.Route,
		AuthResult: e.AuthResult,
		CreatedAt:  e.CreatedAt,
	}
}

type LogsController struct {
	Repo *storage.LogRepository
}

type LogsListRequest struct {
	Status int    `json:"status"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Search string `json:"search"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// NewLogsController constructs a controller for serving API request logs.
func NewLogsController(repo *storage.LogRepository) *LogsController {
	return &LogsController{Repo: repo}
}

// List returns API log entries with optional filtering by status code, method, path, and free-text search.
// Sensitive fields (request_body, response_body, ip, actor_id) are stripped from the response.
// GET /api/admin/logs?status=&method=&path=&search=&limit=&offset=
func (ctl *LogsController) List(c *fiber.Ctx) error {
	if strings.TrimSpace(c.Query("search")) != "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"error": "use POST /api/admin/logs/search for search queries",
		})
	}
	req := LogsListRequest{
		Status: c.QueryInt("status", 0),
		Method: c.Query("method"),
		Path:   c.Query("path"),
		Limit:  c.QueryInt("limit", 50),
		Offset: c.QueryInt("offset", 0),
	}
	return ctl.listWithFilters(c, req)
}

// ListByBody returns API logs using JSON filters so sensitive search terms are
// not exposed in URL query strings.
// POST /api/admin/logs/search
func (ctl *LogsController) ListByBody(c *fiber.Ctx) error {
	var req LogsListRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	return ctl.listWithFilters(c, req)
}

func (ctl *LogsController) listWithFilters(c *fiber.Ctx, req LogsListRequest) error {
	if ctl.Repo == nil || !ctl.Repo.Enabled() {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{"error": "database not configured"})
	}

	status := req.Status
	method := strings.TrimSpace(req.Method)
	path := strings.TrimSpace(req.Path)
	search := strings.TrimSpace(req.Search)
	limit := req.Limit
	offset := req.Offset
	if limit == 0 {
		limit = 50
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	items, err := ctl.Repo.List(c.UserContext(), limit, offset, status, method, path, search)
	if err != nil {
		log.Printf("list api logs failed: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": genericQueryError})
	}

	safe := make([]SafeLogEntry, 0, len(items))
	for _, e := range items {
		safe = append(safe, toSafeLogEntry(e))
	}

	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"data": safe})
}
