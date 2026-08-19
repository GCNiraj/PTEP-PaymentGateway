package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"example.com/fiber-mvc/internal/storage"
)

// Client handles communication with Stripe Payment API.
// Why needed: abstracts Stripe API integration for international payments.
// Called from: InternationalController for payment operations.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
	logRepo *storage.LogRepository
}

// CheckoutSessionResponse represents Stripe checkout session creation response.
type CheckoutSessionResponse struct {
	ResponseCode        string `json:"response_code"`
	ResponseMessage     string `json:"response_message"`
	ResponseDescription string `json:"response_description"`
	ResponseData        struct {
		SessionID  string `json:"session_id"`
		SessionURL string `json:"session_url"`
	} `json:"response_data"`
}

// ApplicationStatusResponse represents Stripe application status check response.
type ApplicationStatusResponse struct {
	ResponseCode        string `json:"response_code"`
	ResponseMessage     string `json:"response_message"`
	ResponseDescription string `json:"response_description"`
	ResponseData        bool   `json:"response_data"`
}

// NewClient creates a Stripe HTTP client with credentials.
// Called from: main() during service initialization.
func NewClient(baseURL, apiKey string, logRepo *storage.LogRepository) *Client {
	return &Client{
		http: &http.Client{
			Timeout: 2 * time.Minute,
		},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		logRepo: logRepo,
	}
}

// CreateCheckoutSession creates a Stripe checkout session for payment.
// Why needed: initiates payment flow by creating Stripe checkout URL.
// Called from: InternationalController.CreatePayment.
// Request payload should contain: amount, currency, agency_name, application_no,
// submerchant_id, dk_account, success_url, cancel_url.
func (c *Client) CreateCheckoutSession(ctx context.Context, payload map[string]interface{}) (*CheckoutSessionResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	requestID := readRequestID(payload)
	start := time.Now().UTC()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/checkout", bytes.NewReader(body))
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/checkout", requestID, string(body), err.Error(), 0, start)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gravitee-Api-Key", c.apiKey)

	// #nosec G704 -- baseURL is operator-configured and points to a fixed upstream integration host.
	res, err := c.http.Do(req)
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/checkout", requestID, string(body), err.Error(), 0, start)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		c.logInternalCall(ctx, http.MethodPost, "/checkout", requestID, string(body), err.Error(), res.StatusCode, start)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	c.logInternalCall(ctx, http.MethodPost, "/checkout", requestID, string(body), string(raw), res.StatusCode, start)

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("stripe API error: status=%d body=%s", res.StatusCode, string(raw))
	}

	var response CheckoutSessionResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Check for error response codes
	if response.ResponseCode != "0000" {
		return nil, fmt.Errorf("stripe error: code=%s message=%s", response.ResponseCode, response.ResponseMessage)
	}

	return &response, nil
}

// CheckApplicationStatus checks if a payment application has been completed.
// Why needed: verifies payment completion status from Stripe.
// Called from: InternationalController.CheckPaymentStatus.
// Returns true if payment completed, false otherwise.
func (c *Client) CheckApplicationStatus(ctx context.Context, applicationNo string) (*ApplicationStatusResponse, error) {
	url := fmt.Sprintf("%s/check-application-status?application_no=%s", c.baseURL, applicationNo)
	start := time.Now().UTC()
	requestID := strings.TrimSpace(applicationNo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.logInternalCall(ctx, http.MethodGet, "/check-application-status", requestID, "", err.Error(), 0, start)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-Gravitee-Api-Key", c.apiKey)

	// #nosec G704 -- request host comes from configured Stripe baseURL; user input is query data only.
	res, err := c.http.Do(req)
	if err != nil {
		c.logInternalCall(ctx, http.MethodGet, "/check-application-status", requestID, "", err.Error(), 0, start)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		c.logInternalCall(ctx, http.MethodGet, "/check-application-status", requestID, "", err.Error(), res.StatusCode, start)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	c.logInternalCall(ctx, http.MethodGet, "/check-application-status", requestID, "", string(raw), res.StatusCode, start)

	var response ApplicationStatusResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	switch response.ResponseCode {
	case "4000", "4001", "4002":
		log.Printf("ERROR stripe check-application-status validation error: application_no=%s response_code=%s message=%s description=%s",
			requestID, response.ResponseCode, response.ResponseMessage, response.ResponseDescription)
		c.logValidationErrorToDB(ctx, requestID, response, string(raw), res.StatusCode, start)
	}

	return &response, nil
}

func (c *Client) logValidationErrorToDB(ctx context.Context, applicationNo string, resp ApplicationStatusResponse, rawBody string, httpStatus int, start time.Time) {
	if c.logRepo == nil || !c.logRepo.Enabled() {
		return
	}
	entry := storage.LogEntry{
		Method:     http.MethodGet,
		Path:       "/check-application-status",
		Status:     httpStatus,
		Duration:   time.Since(start).Milliseconds(),
		RequestID:  applicationNo,
		APISurface: "stripe",
		Route:      "stripe:/check-application-status",
		AuthResult: resp.ResponseCode,
	}
	if err := c.logRepo.Insert(ctx, entry, "", rawBody); err != nil {
		log.Printf("failed to store stripe validation error to DB: %v", err)
	}
}

func (c *Client) logInternalCall(ctx context.Context, method, path, requestID, reqBody, resBody string, status int, start time.Time) {
	if c == nil {
		return
	}
	_ = ctx
	payload := map[string]any{
		"type":         "internal_call_log",
		"sink":         "stdout",
		"service":      "stripe",
		"method":       method,
		"path":         strings.TrimSpace(path),
		"status":       status,
		"duration_ms":  time.Since(start).Milliseconds(),
		"request_id":   strings.TrimSpace(requestID),
		"req_len":      len(reqBody),
		"res_len":      len(resBody),
		"api_surface":  "internal",
		"request_path": "stripe:" + strings.TrimSpace(path),
	}
	if b, err := json.Marshal(payload); err == nil {
		log.Printf("%s", string(b))
		return
	}
	// #nosec G706 -- fallback logging uses trimmed internal fields; primary path uses JSON encoding above.
	log.Printf("internal_call_log service=stripe method=%s path=%s status=%d request_id=%s", method, strings.TrimSpace(path), status, strings.TrimSpace(requestID))
}

func readRequestID(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	for _, key := range []string{"request_id", "requestId", "requestID"} {
		if v, ok := payload[key]; ok && v != nil {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func limitLogBody(s string) string {
	const max = 64 * 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
