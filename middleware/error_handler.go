package middleware

import (
	"errors"
	"log"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// genericServerError is what a caller is told about any failure this service
// did not deliberately describe. Everything else — the driver's text, the
// SQLSTATE, the table name — goes to the log instead.
const genericServerError = "Something went wrong"

// ErrorHandler is the last thing between a panic-free handler error and the
// client.
//
// WHY IT IS HERE. Fiber's default handler puts err.Error() in the response
// body. For an error that came back from the database that is a free
// description of the schema and the engine, handed to anybody who can reach the
// endpoint (ASD Cyber Security, 23 September 2026, finding V4). Handlers that
// mean to say something specific still can — a *fiber.Error below 500 is passed
// through with its own message, which is how "not found" and "invalid date
// range" keep working. Anything else becomes one sentence.
//
// The real error is not discarded. It is logged with the method, the path and
// the X-Request-Id, which is the same identifier the request logger stores
// against the row, so a report of "it returned 500 at 14:32" can be traced to
// the actual cause.
func ErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	message := genericServerError

	var fiberErr *fiber.Error
	if errors.As(err, &fiberErr) {
		code = fiberErr.Code
		if code < fiber.StatusInternalServerError {
			// A deliberate client-facing error: the handler chose these words.
			message = fiberErr.Message
		}
	}

	if code >= fiber.StatusInternalServerError {
		requestID := strings.TrimSpace(c.Get(fiber.HeaderXRequestID))
		if requestID == "" {
			if v, ok := c.Locals("request_id").(string); ok {
				requestID = strings.TrimSpace(v)
			}
		}
		if requestID == "" {
			requestID = "-"
		}
		log.Printf("unhandled error: method=%s path=%s request_id=%s status=%d error=%v",
			c.Method(), c.Path(), requestID, code, err)
	}

	return c.Status(code).JSON(fiber.Map{"error": message})
}
