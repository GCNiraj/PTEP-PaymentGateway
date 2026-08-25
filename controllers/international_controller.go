package controllers

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"example.com/fiber-mvc/config"
	"example.com/fiber-mvc/internal/storage"
	"example.com/fiber-mvc/internal/stripe"
	"example.com/fiber-mvc/models"
)

// SafeInternationalPayment is the safe response projection for GET /api/admin/international/transactions.
// Fields excluded: stripe_session_id (internal token),
// checkout_url (time-limited sensitive URL), error_message (may leak internal detail),
// id (internal DB row ID).
type SafeInternationalPayment struct {
	PaymentID   string  `json:"payment_id"`
	ReferenceID string  `json:"reference_id"`
	MerchantID  string  `json:"merchant_id"`
	Amount      float64 `json:"amount"`
	TotalAmount float64 `json:"total_amount"`
	Currency    string  `json:"currency"`
	Status      string  `json:"status"`
	ErrorCode   string  `json:"error_code,omitempty"`
	Description string  `json:"description,omitempty"`
	CreatedAt   string  `json:"created_at"`
	CompletedAt string  `json:"completed_at,omitempty"`
}

func toSafeIntlPayment(p storage.InternationalPayment) SafeInternationalPayment {
	completedAt := ""
	if p.CompletedAt != nil {
		completedAt = p.CompletedAt.Format(time.RFC3339)
	}
	return SafeInternationalPayment{
		PaymentID:   p.PaymentID,
		ReferenceID: p.ReferenceID,
		MerchantID:  p.MerchantID,
		Amount:      p.Amount,
		TotalAmount: p.TotalAmount,
		Currency:    p.Currency,
		Status:      p.Status,
		ErrorCode:   p.ErrorCode,
		Description: p.Description,
		CreatedAt:   p.CreatedAt.Format(time.RFC3339),
		CompletedAt: completedAt,
	}
}

// InternationalController handles international payment API endpoints.
// Why needed: provides business-level API for Stripe payment processing.
// Called from: routes for /api/v1/payments endpoints.
type InternationalController struct {
	StripeClient *stripe.Client
	IntlRepo     *storage.InternationalRepository
	AppsRepo     *storage.AppsRepository
	Cfg          config.Config
	Resolver     *GatewayResolver
}

type InternationalListRequest struct {
	Limit  int    `json:"limit"`
	Status string `json:"status"`
	From   string `json:"from"`
	To     string `json:"to"`
	Search string `json:"search"`
}

const (
	pendingStatusCheckInterval  = 10 * time.Minute
	pendingStatusCheckBatchSize = 100
	pendingStatusCheckWorkers   = 5
	pendingStatusCheckMaxAge    = 7 * 24 * time.Hour
)

// NewInternationalController constructs the international payment controller.
// Called from: main() to wire /api/v1/payments routes.
func NewInternationalController(stripeClient *stripe.Client, intlRepo *storage.InternationalRepository, appsRepo *storage.AppsRepository, cfg config.Config, resolver *GatewayResolver) *InternationalController {
	return &InternationalController{
		StripeClient: stripeClient,
		IntlRepo:     intlRepo,
		AppsRepo:     appsRepo,
		Cfg:          cfg,
		Resolver:     resolver,
	}
}

// CreatePayment creates a Stripe checkout session for international payment.
// Why needed: business API endpoint for initiating international payments.
// Request: amount, reference_id, description, success_url, cancel_url.
// Response: payment_id, session_id, checkout_url, total_amount with fees, status.
// Called from: POST /api/v1/payments (authenticated with API key).
func (ctrl *InternationalController) CreatePayment(c *fiber.Ctx) error {
	if ctrl.IntlRepo == nil || !ctrl.IntlRepo.Enabled() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "SERVICE_UNAVAILABLE",
				Message: "Payment storage is not available",
			},
		})
	}
	if ctrl.AppsRepo == nil || !ctrl.AppsRepo.Enabled() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "SERVICE_UNAVAILABLE",
				Message: "Merchant store is not available",
			},
		})
	}

	// Get merchant from context (set by API key auth middleware)
	merchantID := c.Locals("app_id")
	if merchantID == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "UNAUTHORIZED",
				Message: "Invalid API key",
			},
		})
	}

	// Parse request
	var req models.CreateInternationalPaymentRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "INVALID_REQUEST",
				Message: "Invalid request body",
			},
		})
	}
	req.ReferenceID = strings.TrimSpace(req.ReferenceID)
	req.MerchantReference = strings.TrimSpace(req.MerchantReference)
	req.SuccessURL = strings.TrimSpace(req.SuccessURL)
	req.CancelURL = strings.TrimSpace(req.CancelURL)
	merchantIDStr, _ := merchantID.(string)
	if req.MerchantReference == "" {
		return merchantConfigurationError(c)
	}
	resolved, err := ctrl.Resolver.Resolve(merchantIDStr, req.MerchantReference, "stripe")
	if err != nil {
		return merchantConfigurationError(c)
	}
	stripeClient, err := ctrl.Resolver.StripeClient(resolved)
	if err != nil {
		return merchantConfigurationError(c)
	}
	allowedCurrency := ctrl.resolvedStripeCurrency()
	const allowedCurrencyUpper = "USD"
	const minAmount = 1.00

	// Validate amount
	if req.Amount < minAmount {
		minAmountMessage := fmt.Sprintf("Payment amount must be at least %.2f %s", minAmount, allowedCurrencyUpper)
		ctrl.storeFailedInternationalCreate(c.Context(), merchantIDStr, req, resolved, "INVALID_AMOUNT", minAmountMessage)
		return c.Status(fiber.StatusBadRequest).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "INVALID_AMOUNT",
				Message: minAmountMessage,
			},
		})
	}

	// Validate reference_id
	if req.ReferenceID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "INVALID_REFERENCE",
				Message: "reference_id is required",
			},
		})
	}

	// Check for duplicate reference_id
	exists, err := ctrl.IntlRepo.ReferenceExistsForMerchant(c.Context(), merchantIDStr, req.ReferenceID)
	if err != nil {
		log.Printf("Error checking reference existence: %v", err)
		ctrl.storeFailedInternationalCreate(c.Context(), merchantIDStr, req, resolved, "SERVER_ERROR", "Internal server error")
		return c.Status(fiber.StatusInternalServerError).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "SERVER_ERROR",
				Message: "Internal server error",
			},
		})
	}
	if exists {
		return c.Status(fiber.StatusConflict).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "DUPLICATE_REFERENCE",
				Message: "Payment with this reference_id already exists",
			},
		})
	}

	// Fetch merchant existence (API-key auth already resolves app_id; this is a defensive check)
	merchant, err := ctrl.AppsRepo.FindByID(c.Context(), merchantID.(string))
	if err != nil || merchant == nil {
		log.Printf("Error fetching merchant: %v", err)
		ctrl.storeFailedInternationalCreate(c.Context(), merchantIDStr, req, resolved, "UNAUTHORIZED", "Invalid merchant")
		return c.Status(fiber.StatusUnauthorized).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "UNAUTHORIZED",
				Message: "Invalid merchant",
			},
		})
	}

	agencyName := ctrl.Resolver.Value(resolved, "agency_name")
	submerchantID := ctrl.Resolver.Value(resolved, "submerchant_id")
	dkAccount := ctrl.Resolver.Value(resolved, "dk_account")
	successURL := req.SuccessURL
	cancelURL := req.CancelURL

	// Validate merchant has Stripe credentials (from app config or env fallback).
	if agencyName == "" || submerchantID == "" || dkAccount == "" {
		ctrl.storeFailedInternationalCreate(c.Context(), merchantIDStr, req, resolved, "MERCHANT_NOT_CONFIGURED", "Merchant not configured for international payments")
		return c.Status(fiber.StatusBadRequest).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "MERCHANT_NOT_CONFIGURED",
				Message: "Merchant not configured for international payments",
			},
		})
	}
	// Validate incoming redirect URLs.
	if successURL == "" || cancelURL == "" {
		ctrl.storeFailedInternationalCreate(c.Context(), merchantIDStr, req, resolved, "INVALID_URL", "success_url and cancel_url are required")
		return c.Status(fiber.StatusBadRequest).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "INVALID_URL",
				Message: "success_url and cancel_url are required",
			},
		})
	}
	if !isValidHTTPSURL(successURL) || !isValidHTTPSURL(cancelURL) {
		ctrl.storeFailedInternationalCreate(c.Context(), merchantIDStr, req, resolved, "INVALID_URL", "success_url and cancel_url must be valid HTTPS URLs")
		return c.Status(fiber.StatusBadRequest).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "INVALID_URL",
				Message: "success_url and cancel_url must be valid HTTPS URLs",
			},
		})
	}

	// Calculate total amount with fees
	totalAmount := calculateTotalAmount(req.Amount)

	// Create Stripe checkout session
	stripePayload := map[string]interface{}{
		"amount":         req.Amount,
		"currency":       allowedCurrency,
		"agency_name":    agencyName,
		"application_no": req.ReferenceID,
		"submerchant_id": submerchantID,
		"dk_account":     dkAccount,
		"success_url":    successURL,
		"cancel_url":     cancelURL,
	}

	stripeResp, err := stripeClient.CreateCheckoutSession(c.Context(), stripePayload)
	if err != nil {
		log.Printf("Stripe API error: %v", err)
		ctrl.storeFailedInternationalCreate(c.Context(), merchantIDStr, req, resolved, "PAYMENT_SERVICE_ERROR", "Failed to create payment session")
		return c.Status(fiber.StatusBadGateway).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "PAYMENT_SERVICE_ERROR",
				Message: "Failed to create payment session",
			},
		})
	}

	// Generate internal payment ID
	paymentID := fmt.Sprintf("pay_%s", uuid.New().String()[:16])

	// Store payment record
	now := time.Now()
	payment := storage.InternationalPayment{
		PaymentID:                        paymentID,
		StripeSessionID:                  stripeResp.ResponseData.SessionID,
		ReferenceID:                      req.ReferenceID,
		MerchantID:                       merchantID.(string),
		ExternalMerchantReference:        req.MerchantReference,
		RecipientID:                      resolved.Routing.RecipientID,
		GatewayProvider:                  resolved.Routing.Credential.Provider,
		GatewayCredentialConfigurationID: resolved.Routing.Credential.ID,
		GatewayCredentialVersion:         resolved.Routing.Credential.Version,
		RoutingMode:                      "merchant",
		Amount:                           req.Amount,
		TotalAmount:                      totalAmount,
		Currency:                         allowedCurrency,
		Status:                           "pending",
		ErrorCode:                        "",
		ErrorMessage:                     "",
		Description:                      req.Description,
		CheckoutURL:                      stripeResp.ResponseData.SessionURL,
		CreatedAt:                        now,
		UpdatedAt:                        now,
	}

	if err := ctrl.IntlRepo.CreatePayment(c.Context(), payment); err != nil {
		log.Printf("Error storing payment: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(models.CreateInternationalPaymentResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "PAYMENT_RECORD_ERROR",
				Message: "Payment session created but failed to store payment record",
			},
		})
	}

	// Return response
	expiresAt := now.Add(24 * time.Hour).Format(time.RFC3339)
	return c.JSON(models.CreateInternationalPaymentResponse{
		Success: true,
		Message: "Payment session created successfully",
		Data: &models.InternationalPaymentData{
			PaymentID:   paymentID,
			SessionID:   stripeResp.ResponseData.SessionID,
			CheckoutURL: stripeResp.ResponseData.SessionURL,
			Amount:      req.Amount,
			TotalAmount: totalAmount,
			Currency:    allowedCurrency,
			ReferenceID: req.ReferenceID,
			Status:      "pending",
			ExpiresAt:   expiresAt,
		},
	})
}

func (ctrl *InternationalController) storeFailedInternationalCreate(ctx context.Context, merchantID string, req models.CreateInternationalPaymentRequest, resolved *ResolvedGateway, code, message string) {
	if ctrl == nil || ctrl.IntlRepo == nil || !ctrl.IntlRepo.Enabled() {
		return
	}
	merchantID = strings.TrimSpace(merchantID)
	if merchantID == "" || strings.TrimSpace(req.ReferenceID) == "" {
		return
	}

	now := time.Now()
	totalAmount := 0.0
	if req.Amount > 0 {
		totalAmount = calculateTotalAmount(req.Amount)
	}
	currency := ctrl.resolvedStripeCurrency()

	payment := storage.InternationalPayment{
		PaymentID:                 fmt.Sprintf("pay_%s", uuid.New().String()[:16]),
		StripeSessionID:           "",
		ReferenceID:               strings.TrimSpace(req.ReferenceID),
		MerchantID:                merchantID,
		ExternalMerchantReference: strings.TrimSpace(req.MerchantReference),
		RoutingMode:               "merchant",
		Amount:                    req.Amount,
		TotalAmount:               totalAmount,
		Currency:                  currency,
		Status:                    "failed",
		ErrorCode:                 strings.TrimSpace(code),
		ErrorMessage:              strings.TrimSpace(message),
		Description:               strings.TrimSpace(req.Description),
		CheckoutURL:               "",
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
	if resolved != nil && resolved.Routing != nil {
		payment.RecipientID = resolved.Routing.RecipientID
		payment.GatewayProvider = resolved.Routing.Credential.Provider
		payment.GatewayCredentialConfigurationID = resolved.Routing.Credential.ID
		payment.GatewayCredentialVersion = resolved.Routing.Credential.Version
	}

	if err := ctrl.IntlRepo.CreatePayment(ctx, payment); err != nil {
		log.Printf("Error storing failed international payment: %v", err)
	}
}

func (ctrl *InternationalController) resolvedStripeCurrency() string {
	currency := strings.ToLower(strings.TrimSpace(ctrl.Cfg.StripeCurrency))
	if currency != "usd" {
		return "usd"
	}
	return currency
}

// StartPendingStatusSync runs a background reconciler that refreshes pending
// Stripe payment statuses at most once per pendingStatusCheckInterval per reference.
func (ctrl *InternationalController) StartPendingStatusSync(ctx context.Context) {
	if ctrl == nil || ctrl.IntlRepo == nil || !ctrl.IntlRepo.Enabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	go func() {
		ctrl.reconcilePendingPayments(ctx)

		ticker := time.NewTicker(pendingStatusCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ctrl.reconcilePendingPayments(ctx)
			}
		}
	}()
}

func (ctrl *InternationalController) reconcileSinglePendingPayment(ctx context.Context, payment storage.InternationalPayment, checkedAt time.Time) (bool, error) {
	stripeClient, err := ctrl.stripeClientForPayment(payment)
	if err != nil {
		_ = ctrl.IntlRepo.MarkStatusCheckAttempt(ctx, payment.MerchantID, payment.ReferenceID, checkedAt)
		return false, err
	}
	stripeResp, err := stripeClient.CheckApplicationStatus(ctx, payment.ReferenceID)
	if err != nil {
		if markErr := ctrl.IntlRepo.MarkStatusCheckAttempt(ctx, payment.MerchantID, payment.ReferenceID, checkedAt); markErr != nil {
			return false, fmt.Errorf("status check failed: %w (mark attempt error: %v)", err, markErr)
		}
		return false, err
	}

	if stripeResp != nil && stripeResp.ResponseData {
		if err := ctrl.IntlRepo.UpdatePaymentStatus(ctx, payment.MerchantID, payment.ReferenceID, "completed"); err != nil {
			return false, err
		}
		return true, nil
	}

	if err := ctrl.IntlRepo.MarkStatusCheckAttempt(ctx, payment.MerchantID, payment.ReferenceID, checkedAt); err != nil {
		return false, err
	}
	return false, nil
}

func (ctrl *InternationalController) stripeClientForPayment(payment storage.InternationalPayment) (*stripe.Client, error) {
	if ctrl == nil {
		return nil, ErrMerchantNotConfigured
	}
	if payment.RoutingMode == "legacy" && strings.TrimSpace(payment.GatewayCredentialConfigurationID) == "" {
		// Rows created before merchant routing have no immutable credential link.
		// This explicit legacy path is intentionally not used for new payments.
		if ctrl.StripeClient == nil {
			return nil, ErrMerchantNotConfigured
		}
		return ctrl.StripeClient, nil
	}
	if ctrl.Resolver == nil {
		return nil, ErrMerchantNotConfigured
	}
	resolved, err := ctrl.Resolver.ResolveCredential(payment.GatewayCredentialConfigurationID)
	if err != nil || resolved.Routing.Credential.Provider != "stripe" {
		return nil, ErrMerchantNotConfigured
	}
	return ctrl.Resolver.StripeClient(resolved)
}

func (ctrl *InternationalController) reconcilePendingPayments(ctx context.Context) {
	if ctrl == nil || ctrl.IntlRepo == nil || !ctrl.IntlRepo.Enabled() {
		return
	}

	now := time.Now().UTC()
	dueBefore := now.Add(-pendingStatusCheckInterval)
	createdAfter := now.Add(-pendingStatusCheckMaxAge)
	totalChecked, totalCompleted := 0, 0

	for {
		if ctx.Err() != nil {
			return
		}

		pending, err := ctrl.IntlRepo.ListPendingForStatusCheck(ctx, dueBefore, createdAfter, pendingStatusCheckBatchSize)
		if err != nil {
			log.Printf("international status sync list pending failed: %v", err)
			return
		}
		if len(pending) == 0 {
			break
		}

		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, pendingStatusCheckWorkers)
		batchCompleted := 0

		for _, p := range pending {
			if ctx.Err() != nil {
				break
			}
			if strings.TrimSpace(p.ReferenceID) == "" {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(p storage.InternationalPayment) {
				defer wg.Done()
				defer func() { <-sem }()
				completed, err := ctrl.reconcileSinglePendingPayment(ctx, p, now)
				if err != nil {
					log.Printf("international status sync check failed reference_id=%s err=%v", p.ReferenceID, err)
					return
				}
				if completed {
					mu.Lock()
					batchCompleted++
					mu.Unlock()
				}
			}(p)
		}
		wg.Wait()

		totalChecked += len(pending)
		totalCompleted += batchCompleted

		if len(pending) < pendingStatusCheckBatchSize {
			break
		}
	}

	log.Printf("international status sync completed: checked=%d completed=%d", totalChecked, totalCompleted)
}

// CheckPaymentStatus retrieves the current status of a payment.
// Why needed: allows merchants to verify payment completion.
// Called from: GET /api/v1/payments/:reference_id/status (authenticated with API key).
func (ctrl *InternationalController) CheckPaymentStatus(c *fiber.Ctx) error {
	// Get merchant from context
	merchantID := c.Locals("app_id")
	if merchantID == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.CheckPaymentStatusResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "UNAUTHORIZED",
				Message: "Invalid API key",
			},
		})
	}

	// Get reference_id from URL
	referenceID := c.Params("reference_id")
	if referenceID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.CheckPaymentStatusResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "INVALID_REQUEST",
				Message: "reference_id is required",
			},
		})
	}

	// Get payment from database
	merchantIDStr, _ := merchantID.(string)
	payment, err := ctrl.IntlRepo.GetPaymentByReferenceForMerchant(c.Context(), merchantIDStr, referenceID)
	if err != nil {
		log.Printf("Error fetching payment: %v", err)
		return c.Status(fiber.StatusNotFound).JSON(models.CheckPaymentStatusResponse{
			Success: false,
			Error: &models.APIError{
				Code:    "PAYMENT_NOT_FOUND",
				Message: "Payment with this reference_id does not exist",
			},
		})
	}

	// Check Stripe for updated status if currently pending
	if strings.EqualFold(payment.Status, "pending") {
		now := time.Now().UTC()
		if _, err := ctrl.reconcileSinglePendingPayment(c.Context(), *payment, now); err != nil {
			log.Printf("Error refreshing pending payment status reference_id=%s: %v", referenceID, err)
			return c.Status(fiber.StatusBadGateway).JSON(models.CheckPaymentStatusResponse{
				Success: false,
				Error: &models.APIError{
					Code:    "STATUS_CHECK_FAILED",
					Message: "Unable to verify payment status, please try again",
				},
			})
		}
		// Re-read from DB to get definitive status — background job may have concurrently updated it
		if updated, err := ctrl.IntlRepo.GetPaymentByReferenceForMerchant(c.Context(), merchantIDStr, referenceID); err != nil {
			log.Printf("Error re-reading payment after status check reference_id=%s: %v", referenceID, err)
		} else {
			payment = updated
		}
	}

	// Build response
	statusData := &models.PaymentStatusData{
		PaymentID:   payment.PaymentID,
		ReferenceID: payment.ReferenceID,
		Status:      payment.Status,
		Amount:      payment.Amount,
		TotalAmount: payment.TotalAmount,
		Currency:    payment.Currency,
		CreatedAt:   payment.CreatedAt.Format(time.RFC3339),
	}

	if payment.CompletedAt != nil {
		statusData.CompletedAt = payment.CompletedAt.Format(time.RFC3339)
	}

	return c.JSON(models.CheckPaymentStatusResponse{
		Success: true,
		Data:    statusData,
	})
}

// calculateTotalAmount computes total amount including all fees.
// Fee structure: 4.15% Stripe + 0.7% DK + $0.60 fixed.
// Called from: CreatePayment.
func calculateTotalAmount(baseAmount float64) float64 {
	const (
		stripeFeePercent = 0.0415 // 4.15%
		dkFeePercent     = 0.007  // 0.7%
		fixedFee         = 0.60
	)

	stripeFee := baseAmount * stripeFeePercent
	dkFee := baseAmount * dkFeePercent
	totalFees := stripeFee + dkFee + fixedFee

	return math.Round((baseAmount+totalFees)*100) / 100
}

// isValidHTTPSURL validates that a URL is a valid HTTPS URL.
// Called from: CreatePayment for success_url and cancel_url validation.
func isValidHTTPSURL(urlStr string) bool {
	u, err := url.Parse(urlStr)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Host != ""
}

// List returns recent international payment transactions.
// Why needed: admin portal requires visibility into international payments.
// Sensitive fields (stripe_session_id, checkout_url, error_message, internal id)
// are omitted from the response.
// Called from: GET /api/admin/international/transactions (protected by AdminAuth).
func (ctrl *InternationalController) List(c *fiber.Ctx) error {
	if strings.TrimSpace(c.Query("search")) != "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "use POST /api/admin/international/transactions/search for search queries",
		})
	}
	req := InternationalListRequest{
		Limit:  c.QueryInt("limit", 50),
		Status: c.Query("status"),
		From:   c.Query("from"),
		To:     c.Query("to"),
	}
	return ctrl.listWithFilters(c, req)
}

// ListByBody returns recent international payment transactions using JSON filters
// so sensitive search terms are not exposed in URL query strings.
// Called from: POST /api/admin/international/transactions/search (protected by AdminAuth + CSRF).
func (ctrl *InternationalController) ListByBody(c *fiber.Ctx) error {
	var req InternationalListRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	return ctrl.listWithFilters(c, req)
}

func (ctrl *InternationalController) listWithFilters(c *fiber.Ctx, req InternationalListRequest) error {
	if ctrl.IntlRepo == nil || !ctrl.IntlRepo.Enabled() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "database not configured"})
	}

	limit := req.Limit
	if limit == 0 {
		limit = 50
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	status := strings.TrimSpace(req.Status)
	from := strings.TrimSpace(req.From)
	to := strings.TrimSpace(req.To)
	search := strings.TrimSpace(req.Search)

	payments, err := ctrl.IntlRepo.List(c.Context(), limit, status, from, to, search)
	if err != nil {
		log.Printf("Error listing international payments: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	safe := make([]SafeInternationalPayment, 0, len(payments))
	for _, p := range payments {
		safe = append(safe, toSafeIntlPayment(p))
	}

	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"data": safe})
}
