package controllers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/config"
)

// UPI payments, which this gateway does not process.
//
// The guest pays the property from their own payment app; the money never
// passes through here, and there is no transaction row to show. But somebody
// chasing a payment opens this dashboard, and "we don't handle that" is not an
// answer when the platform next door has the record.
//
// So this reads them. Server-side, over the platform's internal API, with a key
// held only by this process — the browser never sees that key and never talks
// to the platform. The rows are not stored: they are fetched on each view, so
// the dashboard cannot show a stale decision, and this database stays free of
// records it is not the authority for.
//
// The cost of that choice is a dependency: when the platform is down this tab
// fails, visibly. That is the right failure. An empty table would read as
// "no UPI payments", which is a different and much worse thing to say.

// UpiPayment is one proof as the dashboard shows it. The shape is the
// platform's; this service does not reinterpret it.
type UpiPayment struct {
	BookingReference     string  `json:"bookingReference"`
	TransactionReference string  `json:"transactionReference"`
	PayerContact         string  `json:"payerContact,omitempty"`
	Property             string  `json:"property"`
	Amount               float64 `json:"amount"`
	Currency             string  `json:"currency"`
	Decision             string  `json:"decision"`
	DecisionReason       string  `json:"decisionReason,omitempty"`
	SubmittedAt          string  `json:"submittedAt"`
	DecidedAt            string  `json:"decidedAt,omitempty"`
	ScreenshotURL        string  `json:"screenshotUrl,omitempty"`
	FileName             string  `json:"fileName,omitempty"`
	SizeBytes            int     `json:"sizeBytes,omitempty"`
}

// UpiController serves the dashboard's UPI tab.
type UpiController struct {
	cfg    config.Config
	client *http.Client
}

func NewUpiController(cfg config.Config) *UpiController {
	return &UpiController{
		cfg: cfg,
		// A short timeout on purpose. This is one panel of a dashboard; it must
		// not hold the page open while the platform decides whether to answer.
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// configured reports whether this gateway has somewhere to read from.
func (uc *UpiController) configured() bool {
	return strings.TrimSpace(uc.cfg.PlatformBaseURL) != "" && strings.TrimSpace(uc.cfg.PlatformAPIKey) != ""
}

// call makes one authenticated request to the booking platform.
func (uc *UpiController) call(ctx context.Context, path string) (*http.Response, error) {
	base := strings.TrimRight(strings.TrimSpace(uc.cfg.PlatformBaseURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal-Key", strings.TrimSpace(uc.cfg.PlatformAPIKey))
	req.Header.Set("Accept", "application/json")
	return uc.client.Do(req)
}

// List returns UPI payment records for the dashboard.
// GET /api/admin/upi/payments
func (uc *UpiController) List(c *fiber.Ctx) error {
	if !uc.configured() {
		// Not an error. The gateway is allowed to run without the platform;
		// the tab says so rather than pretending there are no payments.
		return c.JSON(fiber.Map{
			"connected": false,
			"payments":  []UpiPayment{},
			"message":   "This gateway is not connected to the booking platform. Set PLATFORM_BASE_URL and PLATFORM_API_KEY to list UPI payment records.",
		})
	}

	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	resp, err := uc.call(c.Context(), "/v1/internal/upi-payments?limit="+strconv.Itoa(limit))
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"connected": false,
			"error":     "could not reach the booking platform",
		})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The platform's own message is not repeated: it is written for a
		// service, not for the person reading this dashboard.
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"connected": false,
			"error":     "the booking platform refused the request (" + strconv.Itoa(resp.StatusCode) + ")",
		})
	}

	var body struct {
		Payments []UpiPayment `json:"payments"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"connected": false,
			"error":     "the booking platform sent something unreadable",
		})
	}

	// Rewrite each screenshot address to point at this service. The platform's
	// path needs the shared key, which must never reach a browser.
	for i := range body.Payments {
		if body.Payments[i].ScreenshotURL != "" {
			body.Payments[i].ScreenshotURL = "/api/admin/upi/payments/" +
				body.Payments[i].BookingReference + "/screenshot"
		}
	}

	return c.JSON(fiber.Map{
		"connected": true,
		"payments":  body.Payments,
		"count":     len(body.Payments),
	})
}

// Screenshot proxies one proof image from the platform.
// GET /api/admin/upi/payments/:reference/screenshot
//
// A proxy rather than a redirect, so the key stays on this side. The response
// is sandboxed and never sniffed: it is a file a stranger uploaded, and it is
// shown inside an administrative page.
func (uc *UpiController) Screenshot(c *fiber.Ctx) error {
	if !uc.configured() {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not connected to the booking platform"})
	}

	reference := strings.TrimSpace(c.Params("reference"))
	// The reference goes into a URL path. Only the shape the platform issues is
	// accepted, so nothing can be steered out of that path.
	if reference == "" || len(reference) > 64 || strings.ContainsAny(reference, "/?#%\\") {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid booking reference"})
	}

	resp, err := uc.call(c.Context(), "/v1/internal/upi-payments/"+reference+"/screenshot")
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "could not reach the booking platform"})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "no screenshot for that booking"})
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType != "image/png" && contentType != "image/jpeg" {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "unexpected file type"})
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "could not read the screenshot"})
	}

	c.Set("Content-Type", contentType)
	c.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Cache-Control", "no-store")
	c.Set("Content-Disposition", "inline")
	return c.Send(body)
}
