package middleware

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/internal/common"
	"example.com/fiber-mvc/internal/storage"
)

const maxLoggedBodyBytes = 64 * 1024

type requestLogSink int

const (
	requestLogSinkDB requestLogSink = iota
	requestLogSinkStdout
)

// RequestLogger captures request/response metadata and stores it in api_logs.
// Why needed: enables operational tracing for all API calls.
// Stored fields: method, path, status, duration, ip, request_id, request/response body.
// Called from: route groups/handlers that require DB-backed audit logging.
func RequestLogger(logRepo *storage.LogRepository) fiber.Handler {
	return requestLogger(logRepo, requestLogSinkDB)
}

// StdoutRequestLogger captures request metadata and emits a structured stdout log line.
// Why needed: operational visibility for routes that should not persist to api_logs.
func StdoutRequestLogger() fiber.Handler {
	return requestLogger(nil, requestLogSinkStdout)
}

func requestLogger(logRepo *storage.LogRepository, sink requestLogSink) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		var reqBody string
		if sink == requestLogSinkDB && len(c.Body()) > 0 {
			reqBody = sanitizePayload(c.Body(), c.Get(fiber.HeaderContentType))
		}
		requestID := resolveRequestID(c, reqBody)
		c.Locals("request_id", requestID)
		c.Set("X-Request-Id", requestID)

		err := c.Next()

		duration := time.Since(start).Milliseconds()
		status := c.Response().StatusCode()
		routePath := resolveRoutePath(c)
		path := c.Path()
		actorID := resolveActorID(c)
		apiSurface := deriveAPISurface(routePath, path)
		authResult := deriveAuthResult(status, actorID)

		if sink == requestLogSinkStdout {
			logStdoutRequest(storage.LogEntry{
				Method:     c.Method(),
				Path:       path,
				Status:     status,
				Duration:   duration,
				IP:         c.IP(),
				RequestID:  requestID,
				ActorID:    actorID,
				APISurface: apiSurface,
				Route:      routePath,
				AuthResult: authResult,
			})
			return err
		}

		resBody := sanitizePayload(c.Response().Body(), string(c.Response().Header.ContentType()))
		if requestID == "" {
			requestID = common.NewRequestID()
		}

		if logRepo != nil && logRepo.Enabled() {
			if err := logRepo.Insert(context.Background(), storage.LogEntry{
				Method:     c.Method(),
				Path:       path,
				Status:     status,
				Duration:   duration,
				IP:         c.IP(),
				RequestID:  requestID,
				ActorID:    actorID,
				APISurface: apiSurface,
				Route:      routePath,
				AuthResult: authResult,
			}, reqBody, resBody); err != nil {
				log.Printf("request logger insert failed: %v", err)
			}
		}

		return err
	}
}

func sanitizePayload(raw []byte, contentType string) string {
	rawTrimmed := strings.TrimSpace(string(raw))
	if rawTrimmed == "" {
		return ""
	}
	if !looksLikeJSON(rawTrimmed, contentType) {
		return nonJSONPayloadSummary(len(raw), contentType)
	}
	var payload any
	if err := json.Unmarshal([]byte(rawTrimmed), &payload); err != nil {
		return invalidJSONPayloadSummary(len(raw), contentType)
	}
	redactSensitive(payload)
	out, err := json.Marshal(payload)
	if err != nil {
		return invalidJSONPayloadSummary(len(raw), contentType)
	}
	return truncateLoggedBody(string(out))
}

func redactSensitive(v any) {
	switch node := v.(type) {
	case map[string]any:
		for key, value := range node {
			if isSensitiveKey(key) {
				node[key] = "[REDACTED]"
				continue
			}
			redactSensitive(value)
		}
	case []any:
		for i := range node {
			redactSensitive(node[i])
		}
	}
}

func isSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	switch k {
	case "credentials", "encrypted_credentials", "private_key", "password", "passcode", "otp", "token", "access_token", "refresh_token", "api_secret", "api_key", "client_secret", "signup_key", "x_signup_key", "authorization",
		"remitter_account_number", "beneficiary_account_number", "bene_account_number", "source_account_number",
		"account_number", "bank_account", "iban", "bic", "swift", "routing_number", "sort_code",
		"card_number", "card_pan", "pan", "card_no", "cardnumber",
		"cvv", "cvc", "cvv2", "cvc2", "card_cvv", "card_cvc",
		"expiry", "expiry_date", "expiration", "card_expiry", "exp_month", "exp_year",
		"card_holder", "cardholder_name", "card_name",
		"pin", "mpin", "transaction_pin", "withdrawal_pin",
		"national_id", "ssn", "passport_number", "dob", "date_of_birth", "id_number":
		return true
	default:
		return false
	}
}

func resolveRequestID(c *fiber.Ctx, reqBody string) string {
	if c == nil {
		return common.NewRequestID()
	}
	if v := strings.TrimSpace(c.Get("X-Request-Id")); v != "" {
		return v
	}
	if v, ok := c.Locals("request_id").(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v := extractRequestID(reqBody); v != "" {
		return v
	}
	return common.NewRequestID()
}

func extractRequestID(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	if v := requestIDFromMap(m); v != "" {
		return v
	}
	return ""
}

func requestIDFromMap(m map[string]any) string {
	if m == nil {
		return ""
	}
	for _, key := range []string{"request_id", "requestId", "requestID"} {
		if raw, ok := m[key]; ok && raw != nil {
			if v := strings.TrimSpace(toString(raw)); v != "" {
				return v
			}
		}
	}
	for _, key := range []string{"response", "request", "response_data", "data"} {
		nested, ok := m[key]
		if !ok || nested == nil {
			continue
		}
		if child, ok := nested.(map[string]any); ok {
			if v := requestIDFromMap(child); v != "" {
				return v
			}
		}
	}
	return ""
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func looksLikeJSON(raw, contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = strings.TrimSpace(ct[:idx])
	}
	if strings.Contains(ct, "json") {
		return true
	}
	if raw == "" {
		return false
	}
	switch raw[0] {
	case '{', '[':
		return true
	default:
		return false
	}
}

func nonJSONPayloadSummary(size int, contentType string) string {
	return buildPayloadSummary("non_json", size, contentType)
}

func invalidJSONPayloadSummary(size int, contentType string) string {
	return buildPayloadSummary("invalid_json", size, contentType)
}

func buildPayloadSummary(kind string, size int, contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		contentType = "unknown"
	}
	if b, err := json.Marshal(map[string]any{
		"_log_payload": kind,
		"size_bytes":   size,
		"content_type": contentType,
	}); err == nil {
		return string(b)
	}
	return `{"_log_payload":"` + kind + `"}`
}

func truncateLoggedBody(s string) string {
	if len(s) <= maxLoggedBodyBytes {
		return s
	}
	return s[:maxLoggedBodyBytes] + "...(truncated)"
}

func resolveRoutePath(c *fiber.Ctx) string {
	if c == nil {
		return ""
	}
	if r := c.Route(); r != nil {
		if v := strings.TrimSpace(r.Path); v != "" {
			return v
		}
	}
	return strings.TrimSpace(c.Path())
}

func resolveActorID(c *fiber.Ctx) string {
	if c == nil {
		return ""
	}
	if v := localString(c, "app_id"); v != "" {
		return v
	}
	if v := localString(c, "admin_username"); v != "" {
		return v
	}
	return ""
}

func localString(c *fiber.Ctx, key string) string {
	if c == nil {
		return ""
	}
	raw := c.Locals(key)
	if raw == nil {
		return ""
	}
	return strings.TrimSpace(toString(raw))
}

func deriveAPISurface(routePath, rawPath string) string {
	p := strings.TrimSpace(routePath)
	if p == "" {
		p = strings.TrimSpace(rawPath)
	}
	switch {
	case p == "/api/business" || strings.HasPrefix(p, "/api/business/"):
		return "business"
	case p == "/api/v1" || strings.HasPrefix(p, "/api/v1/"):
		return "business"
	case p == "/api/admin" || strings.HasPrefix(p, "/api/admin/"):
		return "internal"
	case p == "/api/dkpg" || strings.HasPrefix(p, "/api/dkpg/"):
		return "internal"
	case strings.HasPrefix(p, "/api/_internal/"):
		return "internal"
	default:
		return ""
	}
}

func deriveAuthResult(status int, actorID string) string {
	switch status {
	case fiber.StatusUnauthorized:
		return "unauthenticated"
	case fiber.StatusForbidden:
		return "forbidden"
	default:
		if strings.TrimSpace(actorID) != "" {
			return "authenticated"
		}
		return ""
	}
}

func logStdoutRequest(entry storage.LogEntry) {
	payload := map[string]any{
		"type":        "request_log",
		"sink":        "stdout",
		"method":      entry.Method,
		"path":        entry.Path,
		"route":       entry.Route,
		"status":      entry.Status,
		"duration_ms": entry.Duration,
		"ip":          entry.IP,
		"request_id":  entry.RequestID,
	}
	if strings.TrimSpace(entry.ActorID) != "" {
		payload["actor_id"] = entry.ActorID
	}
	if strings.TrimSpace(entry.APISurface) != "" {
		payload["api_surface"] = entry.APISurface
	}
	if strings.TrimSpace(entry.AuthResult) != "" {
		payload["auth_result"] = entry.AuthResult
	}
	if b, err := json.Marshal(payload); err == nil {
		log.Printf("%s", string(b))
		return
	}
	log.Printf("request stdout logger: method=%s path=%s status=%d request_id=%s", entry.Method, entry.Path, entry.Status, entry.RequestID)
}
