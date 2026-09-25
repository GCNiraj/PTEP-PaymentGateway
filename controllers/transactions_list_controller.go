package controllers

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"example.com/fiber-mvc/internal/storage"
)

// SafeTransactionRow is the safe response projection for GET /api/admin/transactions.
// account numbers are masked to last 4 digits.
// Internal bank reference IDs (stan_number, bfs_txn_id, inquiry_id, external_app_id)
// are omitted — they are not needed by the admin list UI.
type SafeTransactionRow struct {
	ID                  int64  `json:"id"`
	OrderID             string `json:"order_id"`
	Amount              string `json:"amount"`
	Currency            string `json:"currency"`
	Status              string `json:"status"`
	RemitterAccount     string `json:"remitter_account"`
	BeneficiaryAccount  string `json:"beneficiary_account"`
	TransactionDatetime string `json:"transaction_datetime"`
	CreatedAt           string `json:"created_at"`
}

func toSafeTransactionRow(r storage.TransactionRow) SafeTransactionRow {
	return SafeTransactionRow{
		ID:                  r.ID,
		OrderID:             r.OrderID,
		Amount:              r.Amount,
		Currency:            r.Currency,
		Status:              r.Status,
		RemitterAccount:     maskAccount(r.RemitterAccount),
		BeneficiaryAccount:  maskAccount(r.BeneficiaryAccount),
		TransactionDatetime: r.TransactionDatetime,
		CreatedAt:           r.CreatedAt,
	}
}

// SafeFullTransactionRow is the safe projection for GET /api/admin/transactions/:id.
// Financial account numbers are masked; all other diagnostic detail is retained for admins.
type SafeFullTransactionRow struct {
	ID                  int64  `json:"id"`
	OrderID             string `json:"order_id"`
	InquiryID           string `json:"inquiry_id"`
	StanNumber          string `json:"stan_number"`
	BFSTxnID            string `json:"bfs_txn_id"`
	BFSRequestID        string `json:"bfs_request_id"`
	BFSOrderNo          string `json:"bfs_order_no"`
	Amount              string `json:"amount"`
	TransactionFee      string `json:"transaction_fee"`
	Currency            string `json:"currency"`
	Status              string `json:"status"`
	RemitterAccount     string `json:"remitter_account"`
	RemitterName        string `json:"remitter_name"`
	RemitterPhone       string `json:"remitter_phone"`
	RemitterBank        string `json:"remitter_bank"`
	EmailID             string `json:"email_id"`
	BeneficiaryAccount  string `json:"beneficiary_account"`
	PaymentDesc         string `json:"payment_desc"`
	ErrorCode           string `json:"error_code"`
	ErrorMessage        string `json:"error_message"`
	TransactionDatetime string `json:"transaction_datetime"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
	CompletedAt         string `json:"completed_at"`
}

func toSafeFullTransactionRow(r *storage.FullTransactionRow) SafeFullTransactionRow {
	return SafeFullTransactionRow{
		ID:                  r.ID,
		OrderID:             r.OrderID,
		InquiryID:           r.InquiryID,
		StanNumber:          r.StanNumber,
		BFSTxnID:            r.BFSTxnID,
		BFSRequestID:        r.BFSRequestID,
		BFSOrderNo:          r.BFSOrderNo,
		Amount:              r.Amount,
		TransactionFee:      r.TransactionFee,
		Currency:            r.Currency,
		Status:              r.Status,
		RemitterAccount:     maskAccount(r.RemitterAccount),
		RemitterName:        r.RemitterName,
		RemitterPhone:       r.RemitterPhone,
		RemitterBank:        r.RemitterBank,
		EmailID:             r.EmailID,
		BeneficiaryAccount:  maskAccount(r.BeneficiaryAccount),
		PaymentDesc:         r.PaymentDesc,
		ErrorCode:           r.ErrorCode,
		ErrorMessage:        r.ErrorMessage,
		TransactionDatetime: r.TransactionDatetime,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
		CompletedAt:         r.CompletedAt,
	}
}

// maskAccount masks a financial account number, showing only the last 4 digits.
// e.g. "110158212197" → "********2197"
func maskAccount(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if len(s) <= 4 {
		return s
	}
	return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
}

type TransactionsListController struct {
	Repo *storage.Repository
}

type TransactionsListRequest struct {
	AppID  string `json:"app_id"`
	Status string `json:"status"`
	From   string `json:"from"`
	To     string `json:"to"`
	Search string `json:"search"`
	Limit  int    `json:"limit"`
}

// NewTransactionsListController builds a transaction listing controller.
func NewTransactionsListController(repo *storage.Repository) *TransactionsListController {
	return &TransactionsListController{Repo: repo}
}

// List returns payment transactions filtered by app_id, status, date range, and free-text search.
// GET /api/admin/transactions?app_id=&status=&from=&to=&search=&limit=
func (ctl *TransactionsListController) List(c *fiber.Ctx) error {
	if strings.TrimSpace(c.Query("search")) != "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"error": "use POST /api/admin/transactions/search for search queries",
		})
	}
	req := TransactionsListRequest{
		AppID:  c.Query("app_id"),
		Status: c.Query("status"),
		From:   c.Query("from"),
		To:     c.Query("to"),
		Limit:  c.QueryInt("limit", 100),
	}
	return ctl.listWithFilters(c, req)
}

// ListByBody returns payment transactions using request JSON filters so sensitive
// search terms are not exposed in URL query strings.
// POST /api/admin/transactions/search
func (ctl *TransactionsListController) ListByBody(c *fiber.Ctx) error {
	var req TransactionsListRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	return ctl.listWithFilters(c, req)
}

func (ctl *TransactionsListController) listWithFilters(c *fiber.Ctx, req TransactionsListRequest) error {
	if ctl.Repo == nil || !ctl.Repo.Enabled() {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "database not configured",
		})
	}

	appID := strings.TrimSpace(req.AppID)
	status := strings.TrimSpace(req.Status)
	from, to, err := parseDateRange(req.From, req.To)
	if err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	search := strings.TrimSpace(req.Search)
	limit := req.Limit
	if limit == 0 {
		limit = 100
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := ctl.Repo.ListRecent(c.UserContext(), limit, appID, status, from, to, search)
	if err != nil {
		log.Printf("list transactions failed: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
			"error": genericQueryError,
		})
	}

	// Project to a safe response shape before returning to the admin UI.
	// This avoids leaking sensitive identifiers (external_app_id, stan_number, bfs_txn_id)
	// and ensures account numbers are masked to the last 4 digits only.
	safe := make([]SafeTransactionRow, 0, len(rows))
	for _, r := range rows {
		safe = append(safe, toSafeTransactionRow(r))
	}

	c.Set("Cache-Control", "no-store")
	return c.JSON(fiber.Map{"data": safe})
}

// Stats returns aggregated transaction metrics.
// GET /api/admin/stats
func (ctl *TransactionsListController) Stats(c *fiber.Ctx) error {
	if ctl.Repo == nil || !ctl.Repo.Enabled() {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "database not configured",
		})
	}

	from, to, err := parseDateRange(c.Query("from"), c.Query("to"))
	if err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	stats, err := ctl.Repo.GetTransactionStats(c.UserContext(), from, to)
	if err != nil {
		log.Printf("transaction stats failed: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
			"error": genericQueryError,
		})
	}

	return c.JSON(stats)
}

// DailyStats returns per-day transaction aggregates for volume trend charts.
// GET /api/admin/stats/daily?from=YYYY-MM-DD&to=YYYY-MM-DD
func (ctl *TransactionsListController) DailyStats(c *fiber.Ctx) error {
	if ctl.Repo == nil || !ctl.Repo.Enabled() {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "database not configured",
		})
	}

	from, to, err := parseDateRange(c.Query("from"), c.Query("to"))
	if err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	volumes, err := ctl.Repo.GetDailyVolumes(c.UserContext(), from, to)
	if err != nil {
		log.Printf("daily transaction volumes failed: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
			"error": genericQueryError,
		})
	}

	return c.JSON(fiber.Map{"data": volumes})
}

// Get returns the full detail of a single transaction by ID.
// Account numbers are masked; all other diagnostic fields are retained for admin review.
// GET /api/admin/transactions/:id (kept for compatibility; prefer POST /transactions/detail to avoid id in URL).
func (ctl *TransactionsListController) Get(c *fiber.Ctx) error {
	if ctl.Repo == nil || !ctl.Repo.Enabled() {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "database not configured",
		})
	}

	idStr := c.Params("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid transaction id"})
	}

	row, err := ctl.Repo.GetTransactionFull(c.UserContext(), id)
	if err != nil {
		log.Printf("get transaction %d failed: %v", id, err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": genericQueryError})
	}

	c.Set("Cache-Control", "no-store")
	return c.JSON(toSafeFullTransactionRow(row))
}

// GetDetailByBody returns the full detail of a single transaction by ID sent in the request body.
// Use this instead of GET /transactions/:id to avoid transmitting identifiers in the URL (CWE-598).
// POST /api/admin/transactions/detail — body: { "id": <number> }
func (ctl *TransactionsListController) GetDetailByBody(c *fiber.Ctx) error {
	if ctl.Repo == nil || !ctl.Repo.Enabled() {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "database not configured",
		})
	}

	var req struct {
		ID int64 `json:"id"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}
	if req.ID <= 0 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "id must be a positive number"})
	}

	row, err := ctl.Repo.GetTransactionFull(c.UserContext(), req.ID)
	if err != nil {
		log.Printf("get transaction %d failed: %v", req.ID, err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": genericQueryError})
	}

	c.Set("Cache-Control", "no-store")
	return c.JSON(toSafeFullTransactionRow(row))
}
