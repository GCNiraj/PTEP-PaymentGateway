package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Repository struct {
	DB *sql.DB
}

// NewRepository constructs the payment transaction repository.
// Called from: main() and injected into business/DKPG controllers.
func NewRepository(db *DB) *Repository {
	if db == nil {
		return &Repository{DB: nil}
	}
	return &Repository{DB: db.Conn}
}

// Enabled reports whether repository has a usable SQL connection.
// Called from: repository methods and controller guard logic.
func (r *Repository) Enabled() bool {
	return r != nil && r.DB != nil
}

// CreatePullPayment inserts a payment transaction snapshot into payment_transactions.
// Why needed: preserves request metadata and local status tracking before/after DK calls.
// Payload fields used: app/order/inquiry/STAN/BFS ids, amount/fee, remitter+beneficiary info,
// descriptive fields (payment_desc/currency/status/error) and transaction datetime.
// Called from: merchant-routed business payment initiation.
func (r *Repository) CreatePullPayment(ctx context.Context, payload PullPaymentRecord) error {
	if !r.Enabled() {
		return fmt.Errorf("payment repository is not configured")
	}
	if err := validateMerchantRouting(payload.RoutingMode, payload.ExternalAppID, payload.ExternalMerchantReference, payload.RecipientID, payload.GatewayProvider, payload.GatewayCredentialConfigurationID, payload.GatewayCredentialVersion); err != nil {
		return err
	}

	query := `
		insert into payment_transactions
		(external_app_id, external_merchant_reference, recipient_id, gateway_provider, gateway_credential_configuration_id, gateway_credential_version, routing_mode,
		 order_id, inquiry_id, stan_number, bfs_txn_id, bfs_request_id, bfs_order_no, amount, transaction_fee, remitter_account, remitter_name, remitter_phone,
		 email_id, remitter_bank, beneficiary_account, transaction_datetime, payment_desc, currency, status, error_code, error_message, created_at, updated_at)
		values ($1,$2,$3::uuid,$4,$5::uuid,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,now(),now())
	`

	result, err := r.DB.ExecContext(ctx, query,
		payload.ExternalAppID,
		payload.ExternalMerchantReference,
		payload.RecipientID,
		payload.GatewayProvider,
		payload.GatewayCredentialConfigurationID,
		payload.GatewayCredentialVersion,
		payload.RoutingMode,
		payload.OrderID,
		payload.InquiryID,
		payload.STAN,
		payload.BFSTxnID,
		payload.BFSRequestID,
		payload.BFSOrderNo,
		payload.Amount,
		payload.TransactionFee,
		payload.RemitterAccount,
		payload.RemitterName,
		payload.RemitterPhone,
		payload.EmailID,
		payload.RemitterBank,
		payload.BeneficiaryAccount,
		payload.TransactionDatetime,
		payload.PaymentDesc,
		payload.Currency,
		payload.Status,
		payload.ErrorCode,
		payload.ErrorMessage,
	)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("read pull-payment insert result: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("pull-payment insert affected %d rows", affected)
	}
	return nil
}

// UpdatePullPaymentStatus updates status/error fields by STAN.
// Why needed: tracks transition from pending to terminal result.
// Terminal statuses COMPLETED/FAILED automatically set completed_at.
// Called from: business and DKPG controllers after upstream responses/errors.
func (r *Repository) UpdatePullPaymentStatus(ctx context.Context, appID, stan, status, errorCode, errorMessage string) error {
	if !r.Enabled() {
		return nil
	}

	query := `
		update payment_transactions
		set status = $1, error_code = $2, error_message = $3, updated_at = now(),
		    completed_at = case when $1 in ('COMPLETED','FAILED') then now() else completed_at end
		where external_app_id = $4 and stan_number = $5
	`

	result, err := r.DB.ExecContext(ctx, query, status, errorCode, errorMessage, appID, stan)
	return requireOneAffectedRow(result, err)
}

// UpdateBFSTxnID stores bfs_txn_id against a transaction identified by STAN.
// Called from: pull-initiation handlers once DK response contains BFS txn id.
func (r *Repository) UpdateBFSTxnID(ctx context.Context, appID, stan, bfsTxnID string) error {
	if !r.Enabled() {
		return nil
	}

	result, err := r.DB.ExecContext(ctx, `update payment_transactions set bfs_txn_id=$1, updated_at=now() where external_app_id=$2 and stan_number=$3`, bfsTxnID, appID, stan)
	return requireOneAffectedRow(result, err)
}

// UpdateBFSOrderNo stores bfs_order_no against a transaction identified by STAN.
// Called from: pull-initiation handlers when upstream response includes order no.
func (r *Repository) UpdateBFSOrderNo(ctx context.Context, appID, stan, bfsOrderNo string) error {
	if !r.Enabled() {
		return nil
	}

	result, err := r.DB.ExecContext(ctx, `update payment_transactions set bfs_order_no=$1, updated_at=now() where external_app_id=$2 and stan_number=$3`, bfsOrderNo, appID, stan)
	return requireOneAffectedRow(result, err)
}

// UpdateConfirmMeta stores confirm-step metadata (bfs_request_id and bfs_order_no).
// Called from: BusinessController.PullPaymentConfirm.
func (r *Repository) UpdateConfirmMeta(ctx context.Context, appID, stan, requestID, orderNo string) error {
	if !r.Enabled() {
		return nil
	}
	result, err := r.DB.ExecContext(ctx, `update payment_transactions set bfs_request_id=$1, bfs_order_no=$2, updated_at=now() where external_app_id=$3 and stan_number=$4`, requestID, orderNo, appID, stan)
	return requireOneAffectedRow(result, err)
}

// GetBFSTxnIDBySTAN retrieves previously stored bfs_txn_id for a STAN.
// Why needed: confirm flow can proceed even when client omits bfs_txn_id.
// Called from: BusinessController.PullPaymentConfirm.
func (r *Repository) GetBFSTxnIDBySTAN(ctx context.Context, stan string) (string, error) {
	if !r.Enabled() {
		return "", nil
	}

	var bfsTxnID sql.NullString
	if err := r.DB.QueryRowContext(ctx, `select bfs_txn_id from payment_transactions where stan_number=$1`, stan).Scan(&bfsTxnID); err != nil {
		return "", err
	}
	return bfsTxnID.String, nil
}

// GetBFSTxnIDByOrderID retrieves previously stored bfs_txn_id for an external_reference/order_id.
// Why needed: status endpoints can resolve client-provided external_reference to DK transaction_id.
// Called from: BusinessController status handlers.
func (r *Repository) GetBFSTxnIDByOrderID(ctx context.Context, orderID string) (string, error) {
	if !r.Enabled() {
		return "", nil
	}

	var bfsTxnID sql.NullString
	if err := r.DB.QueryRowContext(ctx, `select bfs_txn_id from payment_transactions where order_id=$1 order by created_at desc limit 1`, orderID).Scan(&bfsTxnID); err != nil {
		return "", err
	}
	return bfsTxnID.String, nil
}

// GetSTANByOrderID retrieves previously stored stan_number for an external_reference/order_id.
// Why needed: intra status endpoint can resolve app order_id to DK reference_no.
// Called from: BusinessController.StatusIntra.
func (r *Repository) GetSTANByOrderID(ctx context.Context, orderID string) (string, error) {
	if !r.Enabled() {
		return "", nil
	}

	var stan sql.NullString
	if err := r.DB.QueryRowContext(ctx, `select stan_number from payment_transactions where order_id=$1 order by created_at desc limit 1`, orderID).Scan(&stan); err != nil {
		return "", err
	}
	return stan.String, nil
}

// GetBFSOrderNoBySTAN retrieves previously stored bfs_order_no for a STAN.
// Why needed: confirm flow can work without client sending bfs_orderNo.
// Called from: BusinessController.PullPaymentConfirm.
func (r *Repository) GetBFSOrderNoBySTAN(ctx context.Context, stan string) (string, error) {
	if !r.Enabled() {
		return "", nil
	}

	var orderNo sql.NullString
	if err := r.DB.QueryRowContext(ctx, `select bfs_order_no from payment_transactions where stan_number=$1`, stan).Scan(&orderNo); err != nil {
		return "", err
	}
	return orderNo.String, nil
}

// OrderIDExists checks whether external reference/order_id has already been used.
// Why needed: prevents accidental duplicate payment initiation.
// Called from: pull initiation endpoints in business and DKPG controllers.
func (r *Repository) OrderIDExists(ctx context.Context, orderID string) (bool, error) {
	if !r.Enabled() || strings.TrimSpace(orderID) == "" {
		return false, nil
	}
	var exists bool
	if err := r.DB.QueryRowContext(ctx, `select exists(select 1 from payment_transactions where order_id=$1)`, orderID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (r *Repository) OrderIDExistsForApp(ctx context.Context, appID, orderID string) (bool, error) {
	if !r.Enabled() || strings.TrimSpace(appID) == "" || strings.TrimSpace(orderID) == "" {
		return false, nil
	}
	var exists bool
	if err := r.DB.QueryRowContext(ctx, `select exists(select 1 from payment_transactions where external_app_id=$1 and order_id=$2)`, strings.TrimSpace(appID), strings.TrimSpace(orderID)).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// IntraInquiryOrderIDExists checks whether order_id already exists in intra_inquiries.
// Why needed: prevents duplicate intra inquiry requests for the same external reference.
// Called from: BusinessController.IntraInquiry.
func (r *Repository) IntraInquiryOrderIDExists(ctx context.Context, orderID string) (bool, error) {
	if !r.Enabled() || strings.TrimSpace(orderID) == "" {
		return false, nil
	}

	var exists bool
	err := r.DB.QueryRowContext(ctx, `select exists(select 1 from intra_inquiries where order_id=$1)`, orderID).Scan(&exists)
	if err == nil {
		return exists, nil
	}

	// Backward compatibility for older schemas missing order_id column.
	if isMissingColumnError(err, "order_id") {
		return false, nil
	}
	return false, err
}

func (r *Repository) IntraInquiryOrderIDExistsForApp(ctx context.Context, appID, orderID string) (bool, error) {
	if !r.Enabled() || strings.TrimSpace(appID) == "" || strings.TrimSpace(orderID) == "" {
		return false, nil
	}
	var exists bool
	err := r.DB.QueryRowContext(ctx, `select exists(select 1 from intra_inquiries where external_app_id=$1 and order_id=$2)`, strings.TrimSpace(appID), strings.TrimSpace(orderID)).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// UpdateOrderIDStatus updates transaction state by order_id instead of STAN.
// Called from: compatibility paths where order_id is primary lookup key.
func (r *Repository) UpdateOrderIDStatus(ctx context.Context, orderID, status, errorCode, errorMessage string) error {
	if !r.Enabled() || strings.TrimSpace(orderID) == "" {
		return nil
	}
	_, err := r.DB.ExecContext(ctx, `update payment_transactions set status=$1, error_code=$2, error_message=$3, updated_at=now() where order_id=$4`, status, errorCode, errorMessage, orderID)
	return err
}

// IsDuplicateOrderIDError classifies DB unique-constraint errors for order_id.
// Called from: handlers to return user-facing 409 conflict responses.
func IsDuplicateOrderIDError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "payment_transactions_order_id_uniq") ||
		strings.Contains(msg, "duplicate key value") && strings.Contains(msg, "order_id")
}

// IsDuplicateIntraInquiryOrderIDError classifies duplicate order_id errors for intra_inquiries.
func IsDuplicateIntraInquiryOrderIDError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "intra_inquiries_order_id_uniq") ||
		(strings.Contains(msg, "duplicate key value") && strings.Contains(msg, "order_id"))
}

func isMissingColumnError(err error, column string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "column") &&
		strings.Contains(msg, strings.ToLower(column)) &&
		strings.Contains(msg, "does not exist")
}

func isAnyMissingColumnError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "column") && strings.Contains(msg, "does not exist")
}

func (r *Repository) paymentTransactionsColumns(ctx context.Context) (map[string]bool, error) {
	if !r.Enabled() {
		return map[string]bool{}, nil
	}

	rows, err := r.DB.QueryContext(ctx, `select * from payment_transactions limit 0`)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "relation") && strings.Contains(msg, "payment_transactions") {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	defer rows.Close()

	cols := map[string]bool{}
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		cols[strings.ToLower(strings.TrimSpace(name))] = true
	}
	return cols, nil
}

func selectTextExpr(cols map[string]bool, column, alias string) string {
	column = strings.ToLower(strings.TrimSpace(column))
	alias = strings.TrimSpace(alias)
	if cols[column] {
		return fmt.Sprintf("coalesce(%s::text,'') as %s", column, alias)
	}
	return fmt.Sprintf("''::text as %s", alias)
}

func selectInt64Expr(cols map[string]bool, column, alias string) string {
	column = strings.ToLower(strings.TrimSpace(column))
	alias = strings.TrimSpace(alias)
	if cols[column] {
		return fmt.Sprintf("%s::bigint as %s", column, alias)
	}
	return fmt.Sprintf("0::bigint as %s", alias)
}

type PullPaymentRecord struct {
	ExternalAppID                    string
	ExternalMerchantReference        string
	RecipientID                      string
	GatewayProvider                  string
	GatewayCredentialConfigurationID string
	GatewayCredentialVersion         int
	RoutingMode                      string
	OrderID                          string
	InquiryID                        string
	STAN                             string
	BFSTxnID                         string
	BFSRequestID                     string
	BFSOrderNo                       string
	Amount                           float64
	TransactionFee                   float64
	RemitterAccount                  string
	RemitterName                     string
	RemitterPhone                    string
	EmailID                          string
	RemitterBank                     string
	BeneficiaryAccount               string
	TransactionDatetime              time.Time
	PaymentDesc                      string
	Currency                         string
	Status                           string
	ErrorCode                        string
	ErrorMessage                     string
	CreatedAt                        time.Time
}

func validateMerchantRouting(mode, appID, merchantReference, recipientID, provider, credentialID string, credentialVersion int) error {
	if mode != "merchant" {
		return fmt.Errorf("new payment transactions must use merchant routing")
	}
	if strings.TrimSpace(appID) == "" || strings.TrimSpace(merchantReference) == "" || strings.TrimSpace(recipientID) == "" || strings.TrimSpace(provider) == "" || strings.TrimSpace(credentialID) == "" || credentialVersion <= 0 {
		return fmt.Errorf("merchant routing metadata is incomplete")
	}
	return nil
}

func requireOneAffectedRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return fmt.Errorf("database update returned no result")
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read update result: %w", err)
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

type IntraInquiryRecord struct {
	InquiryID                        string
	ExternalAppID                    string
	ExternalMerchantReference        string
	RecipientID                      string
	GatewayProvider                  string
	GatewayCredentialConfigurationID string
	GatewayCredentialVersion         int
	RoutingMode                      string
	OrderID                          string
	BeneficiaryAccount               string
	Amount                           float64
	Status                           string
	ErrorCode                        string
	ErrorMessage                     string
}

// CreateIntraInquiry persists an inquiry with the same immutable merchant routing
// linkage as a payment attempt.
func (r *Repository) CreateIntraInquiry(ctx context.Context, record IntraInquiryRecord) error {
	if !r.Enabled() {
		return fmt.Errorf("repository not enabled")
	}
	if err := validateMerchantRouting(record.RoutingMode, record.ExternalAppID, record.ExternalMerchantReference, record.RecipientID, record.GatewayProvider, record.GatewayCredentialConfigurationID, record.GatewayCredentialVersion); err != nil {
		return err
	}
	if record.InquiryID == "" {
		record.InquiryID = "FAILED-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	result, err := r.DB.ExecContext(ctx, `
		INSERT INTO intra_inquiries
		(inquiry_id, external_app_id, external_merchant_reference, recipient_id, gateway_provider, gateway_credential_configuration_id, gateway_credential_version, routing_mode, order_id, bene_account_number, amount, status, error_code, error_message, created_at)
		VALUES ($1,$2,$3,$4::uuid,$5,$6::uuid,$7,$8,$9,$10,$11,$12,$13,$14,now())
	`, record.InquiryID, record.ExternalAppID, record.ExternalMerchantReference, record.RecipientID, record.GatewayProvider, record.GatewayCredentialConfigurationID, record.GatewayCredentialVersion, record.RoutingMode, record.OrderID, record.BeneficiaryAccount, record.Amount, record.Status, record.ErrorCode, record.ErrorMessage)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("read intra-inquiry insert result: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("intra-inquiry insert affected %d rows", affected)
	}
	return nil
}

// GetIntraInquiryOrderID returns stored external reference/order_id for an inquiry id.
// Why needed: intra transfer can reuse order_id captured at inquiry step.
// Called from: BusinessController.IntraTransfer.
func (r *Repository) GetIntraInquiryOrderID(ctx context.Context, inquiryID string) (string, error) {
	if !r.Enabled() {
		return "", nil
	}

	var orderID sql.NullString
	if err := r.DB.QueryRowContext(ctx, `select order_id from intra_inquiries where inquiry_id=$1`, inquiryID).Scan(&orderID); err != nil {
		if strings.Contains(err.Error(), "order_id") {
			return "", nil
		}
		return "", err
	}
	return orderID.String, nil
}

type TransactionRow struct {
	ID                  int64  `json:"id"`
	ExternalAppID       string `json:"external_app_id"`
	OrderID             string `json:"order_id"`
	InquiryID           string `json:"inquiry_id"`
	StanNumber          string `json:"stan_number"`
	BFSTxnID            string `json:"bfs_txn_id"`
	Amount              string `json:"amount"`
	Currency            string `json:"currency"`
	Status              string `json:"status"`
	RemitterAccount     string `json:"remitter_account"`
	BeneficiaryAccount  string `json:"beneficiary_account"`
	TransactionDatetime string `json:"transaction_datetime"`
	CreatedAt           string `json:"created_at"`
}

// FullTransactionRow holds all columns from payment_transactions for the detail modal.
type FullTransactionRow struct {
	TransactionRow
	BFSRequestID   string `json:"bfs_request_id"`
	BFSOrderNo     string `json:"bfs_order_no"`
	TransactionFee string `json:"transaction_fee"`
	RemitterName   string `json:"remitter_name"`
	RemitterPhone  string `json:"remitter_phone"`
	RemitterBank   string `json:"remitter_bank"`
	EmailID        string `json:"email_id"`
	PaymentDesc    string `json:"payment_desc"`
	ErrorCode      string `json:"error_code"`
	ErrorMessage   string `json:"error_message"`
	UpdatedAt      string `json:"updated_at"`
	CompletedAt    string `json:"completed_at"`
}

// DailyVolume holds an aggregated count and total amount for a single day.
type DailyVolume struct {
	Day          string  `json:"day"`
	Count        int     `json:"count"`
	TotalAmount  float64 `json:"total_amount"`
	SuccessCount int     `json:"success_count"`
	FailedCount  int     `json:"failed_count"`
}

// GetDailyVolumes returns per-day transaction counts for the volume trend chart.
// from/to are optional ISO date strings (YYYY-MM-DD). Defaults to last 30 days.
func (r *Repository) GetDailyVolumes(ctx context.Context, from, to string) ([]DailyVolume, error) {
	if !r.Enabled() {
		return nil, nil
	}
	if from == "" {
		from = time.Now().AddDate(0, 0, -29).Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}
	cols, err := r.paymentTransactionsColumns(ctx)
	if err != nil || len(cols) == 0 {
		return []DailyVolume{}, nil
	}

	dateCol := "created_at::timestamp"
	amountCol := "amount::numeric"
	statusCol := "status::text"
	fromTable := "payment_transactions"

	if !cols["created_at"] || !cols["amount"] || !cols["status"] {
		dateCol = "(t.j->>'created_at')::timestamp"
		amountCol = "(t.j->>'amount')::numeric"
		statusCol = "t.j->>'status'"
		fromTable = "(SELECT to_jsonb(pt) as j FROM payment_transactions pt) t"
	}

	query := fmt.Sprintf(`
		SELECT
			date_trunc('day', %s)::date::text AS day,
			COUNT(*) AS count,
			COALESCE(SUM(%s), 0) AS total_amount,
			COUNT(CASE WHEN lower(%s) IN ('success', 'successful', 'completed') THEN 1 END) AS success_count,
			COUNT(CASE WHEN lower(%s) = 'failed' THEN 1 END) AS failed_count
		FROM %s
		WHERE %s >= $1::date
		  AND %s <  $2::date + interval '1 day'
		GROUP BY 1
		ORDER BY 1 ASC
	`, dateCol, amountCol, statusCol, statusCol, fromTable, dateCol, dateCol)
	rows, err := r.DB.QueryContext(ctx, query, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DailyVolume
	for rows.Next() {
		var v DailyVolume
		if err := rows.Scan(&v.Day, &v.Count, &v.TotalAmount, &v.SuccessCount, &v.FailedCount); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// ListRecent returns payment transactions with optional filters.
// Supports: app_id, status, date range (from/to as YYYY-MM-DD), and free-text search.
// Called from: TransactionsListController.List.
func (r *Repository) ListRecent(ctx context.Context, limit int, appID, status, from, to, search string) ([]TransactionRow, error) {
	if !r.Enabled() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	cols, err := r.paymentTransactionsColumns(ctx)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return []TransactionRow{}, nil
	}

	selectParts := []string{
		selectInt64Expr(cols, "id", "id"),
		selectTextExpr(cols, "external_app_id", "external_app_id"),
		selectTextExpr(cols, "order_id", "order_id"),
		selectTextExpr(cols, "inquiry_id", "inquiry_id"),
		selectTextExpr(cols, "stan_number", "stan_number"),
		selectTextExpr(cols, "bfs_txn_id", "bfs_txn_id"),
		selectTextExpr(cols, "amount", "amount"),
		selectTextExpr(cols, "currency", "currency"),
		selectTextExpr(cols, "status", "status"),
		selectTextExpr(cols, "remitter_account", "remitter_account"),
		selectTextExpr(cols, "beneficiary_account", "beneficiary_account"),
	}

	switch {
	case cols["transaction_datetime"]:
		selectParts = append(selectParts, "coalesce(transaction_datetime::text,'') as transaction_datetime")
	case cols["created_at"]:
		selectParts = append(selectParts, "coalesce(created_at::text,'') as transaction_datetime")
	case cols["updated_at"]:
		selectParts = append(selectParts, "coalesce(updated_at::text,'') as transaction_datetime")
	default:
		selectParts = append(selectParts, "''::text as transaction_datetime")
	}

	switch {
	case cols["created_at"]:
		selectParts = append(selectParts, "coalesce(created_at::text,'') as created_at")
	case cols["updated_at"]:
		selectParts = append(selectParts, "coalesce(updated_at::text,'') as created_at")
	default:
		selectParts = append(selectParts, "''::text as created_at")
	}

	// #nosec G202 -- selectParts are built from an internal allowlist of known column expressions.
	query := "select " + strings.Join(selectParts, ", ") + " from payment_transactions"

	whereParts := []string{}
	args := []any{}
	if cols["external_app_id"] && strings.TrimSpace(appID) != "" {
		args = append(args, appID)
		whereParts = append(whereParts, fmt.Sprintf("external_app_id = $%d", len(args)))
	}
	if cols["status"] && strings.TrimSpace(status) != "" {
		args = append(args, status)
		whereParts = append(whereParts, fmt.Sprintf("lower(status::text) = lower($%d)", len(args)))
	}
	if cols["created_at"] && strings.TrimSpace(from) != "" {
		args = append(args, strings.TrimSpace(from))
		whereParts = append(whereParts, fmt.Sprintf("created_at::timestamp >= $%d::date", len(args)))
	}
	if cols["created_at"] && strings.TrimSpace(to) != "" {
		args = append(args, strings.TrimSpace(to))
		whereParts = append(whereParts, fmt.Sprintf("created_at::timestamp < $%d::date + interval '1 day'", len(args)))
	}
	if strings.TrimSpace(search) != "" {
		s := "%" + strings.TrimSpace(search) + "%"
		args = append(args, s)
		searchIdx := len(args)
		searchClauses := []string{}
		for _, col := range []string{"order_id", "stan_number", "bfs_txn_id", "remitter_account", "beneficiary_account", "inquiry_id"} {
			if cols[col] {
				searchClauses = append(searchClauses, fmt.Sprintf("coalesce(%s::text,'') ilike $%d", col, searchIdx))
			}
		}
		if len(searchClauses) > 0 {
			whereParts = append(whereParts, "("+strings.Join(searchClauses, " or ")+")")
		}
	}
	if len(whereParts) > 0 {
		query += " where " + strings.Join(whereParts, " and ")
	}

	switch {
	case cols["created_at"]:
		query += " order by created_at desc"
	case cols["updated_at"]:
		query += " order by updated_at desc"
	case cols["id"]:
		query += " order by id desc"
	}

	query += fmt.Sprintf(" limit $%d", len(args)+1)
	args = append(args, limit)

	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		// Handle partially migrated environments gracefully.
		if strings.Contains(strings.ToLower(err.Error()), "relation") && strings.Contains(strings.ToLower(err.Error()), "payment_transactions") {
			return []TransactionRow{}, nil
		}
		// Last fallback: generic JSON row extraction to avoid admin dashboard hard-fail.
		genericRows, gErr := r.listRecentGeneric(ctx, limit, appID, status, from, to, search)
		if gErr == nil {
			return genericRows, nil
		}
		return nil, err
	}
	defer rows.Close()

	result := make([]TransactionRow, 0, limit)
	for rows.Next() {
		var row TransactionRow
		if err := rows.Scan(
			&row.ID,
			&row.ExternalAppID,
			&row.OrderID,
			&row.InquiryID,
			&row.StanNumber,
			&row.BFSTxnID,
			&row.Amount,
			&row.Currency,
			&row.Status,
			&row.RemitterAccount,
			&row.BeneficiaryAccount,
			&row.TransactionDatetime,
			&row.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// GetTransactionFull fetches all columns for a single payment transaction by its row ID.
func (r *Repository) GetTransactionFull(ctx context.Context, id int64) (*FullTransactionRow, error) {
	if !r.Enabled() {
		return nil, fmt.Errorf("repository not enabled")
	}
	row := r.DB.QueryRowContext(ctx, `
		SELECT
			COALESCE(id::text,'') AS id,
			COALESCE(external_app_id,'') AS external_app_id,
			COALESCE(order_id,'') AS order_id,
			COALESCE(inquiry_id,'') AS inquiry_id,
			COALESCE(stan_number,'') AS stan_number,
			COALESCE(bfs_txn_id,'') AS bfs_txn_id,
			COALESCE(bfs_request_id,'') AS bfs_request_id,
			COALESCE(bfs_order_no,'') AS bfs_order_no,
			COALESCE(amount::text,'') AS amount,
			COALESCE(transaction_fee::text,'') AS transaction_fee,
			COALESCE(currency,'') AS currency,
			COALESCE(status,'') AS status,
			COALESCE(remitter_account,'') AS remitter_account,
			COALESCE(remitter_name,'') AS remitter_name,
			COALESCE(remitter_phone,'') AS remitter_phone,
			COALESCE(remitter_bank,'') AS remitter_bank,
			COALESCE(email_id,'') AS email_id,
			COALESCE(beneficiary_account,'') AS beneficiary_account,
			COALESCE(payment_desc,'') AS payment_desc,
			COALESCE(error_code,'') AS error_code,
			COALESCE(error_message,'') AS error_message,
			COALESCE(transaction_datetime::text,'') AS transaction_datetime,
			COALESCE(created_at::text,'') AS created_at,
			COALESCE(updated_at::text,'') AS updated_at,
			COALESCE(completed_at::text,'') AS completed_at
		FROM payment_transactions
		WHERE id = $1
	`, id)
	var r2 FullTransactionRow
	var idStr string
	err := row.Scan(
		&idStr,
		&r2.ExternalAppID, &r2.OrderID, &r2.InquiryID, &r2.StanNumber, &r2.BFSTxnID,
		&r2.BFSRequestID, &r2.BFSOrderNo,
		&r2.Amount, &r2.TransactionFee, &r2.Currency, &r2.Status,
		&r2.RemitterAccount, &r2.RemitterName, &r2.RemitterPhone, &r2.RemitterBank, &r2.EmailID,
		&r2.BeneficiaryAccount, &r2.PaymentDesc,
		&r2.ErrorCode, &r2.ErrorMessage,
		&r2.TransactionDatetime, &r2.CreatedAt, &r2.UpdatedAt, &r2.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	r2.ID = toInt64(idStr)
	return &r2, nil
}

func (r *Repository) listRecentGeneric(ctx context.Context, limit int, appID, status, from, to, search string) ([]TransactionRow, error) {
	base := `select to_jsonb(t) from payment_transactions t`
	whereParts := []string{}
	args := []any{}

	if strings.TrimSpace(appID) != "" {
		args = append(args, strings.TrimSpace(appID))
		whereParts = append(whereParts, fmt.Sprintf("coalesce(to_jsonb(t)->>'external_app_id','') = $%d", len(args)))
	}
	if strings.TrimSpace(status) != "" {
		args = append(args, strings.TrimSpace(status))
		whereParts = append(whereParts, fmt.Sprintf("lower(coalesce(to_jsonb(t)->>'status','')) = lower($%d)", len(args)))
	}
	if strings.TrimSpace(from) != "" {
		args = append(args, strings.TrimSpace(from))
		whereParts = append(whereParts, fmt.Sprintf("(to_jsonb(t)->>'created_at')::timestamp >= $%d::date", len(args)))
	}
	if strings.TrimSpace(to) != "" {
		args = append(args, strings.TrimSpace(to))
		whereParts = append(whereParts, fmt.Sprintf("(to_jsonb(t)->>'created_at')::timestamp < $%d::date + interval '1 day'", len(args)))
	}
	if strings.TrimSpace(search) != "" {
		s := "%" + strings.TrimSpace(search) + "%"
		args = append(args, s)
		whereParts = append(whereParts, fmt.Sprintf(`(coalesce(to_jsonb(t)->>'order_id','') ilike $%d OR coalesce(to_jsonb(t)->>'stan_number','') ilike $%d OR coalesce(to_jsonb(t)->>'bfs_txn_id','') ilike $%d OR coalesce(to_jsonb(t)->>'remitter_account','') ilike $%d OR coalesce(to_jsonb(t)->>'beneficiary_account','') ilike $%d)`, len(args), len(args), len(args), len(args), len(args)))
	}

	query := base
	if len(whereParts) > 0 {
		query += " where " + strings.Join(whereParts, " and ")
	}
	query += fmt.Sprintf(" order by t.ctid desc limit $%d", len(args)+1)
	args = append(args, limit)

	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]TransactionRow, 0, limit)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}

		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}

		row := TransactionRow{
			ID:                  toInt64(m["id"]),
			ExternalAppID:       toStringSafe(m["external_app_id"]),
			OrderID:             toStringSafe(m["order_id"]),
			InquiryID:           toStringSafe(m["inquiry_id"]),
			StanNumber:          toStringSafe(m["stan_number"]),
			BFSTxnID:            toStringSafe(m["bfs_txn_id"]),
			Amount:              toStringSafe(m["amount"]),
			Currency:            toStringSafe(m["currency"]),
			Status:              toStringSafe(m["status"]),
			RemitterAccount:     toStringSafe(m["remitter_account"]),
			BeneficiaryAccount:  toStringSafe(m["beneficiary_account"]),
			TransactionDatetime: firstNonEmpty(toStringSafe(m["transaction_datetime"]), toStringSafe(m["created_at"]), toStringSafe(m["updated_at"])),
			CreatedAt:           firstNonEmpty(toStringSafe(m["created_at"]), toStringSafe(m["updated_at"])),
		}
		result = append(result, row)
	}

	return result, rows.Err()
}

func toStringSafe(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(t, 'f', -1, 64))
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return int64(t)
	case int64:
		return t
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

type TransactionStats struct {
	TotalCount   int     `json:"total_count"`
	SuccessCount int     `json:"success_count"`
	FailedCount  int     `json:"failed_count"`
	PendingCount int     `json:"pending_count"`
	TotalAmount  float64 `json:"total_amount"`
}

// GetTransactionStats aggregates transaction metrics.
// Used for dashboard graphs/metrics.
func (r *Repository) GetTransactionStats(ctx context.Context, from, to string) (*TransactionStats, error) {
	if !r.Enabled() {
		return &TransactionStats{}, nil
	}

	cols, err := r.paymentTransactionsColumns(ctx)
	if err != nil || len(cols) == 0 {
		return &TransactionStats{}, nil
	}

	dateCol := "created_at::timestamp"
	amountCol := "amount::numeric"
	statusCol := "status::text"
	fromTable := "payment_transactions"

	if !cols["created_at"] || !cols["amount"] || !cols["status"] {
		dateCol = "(t.j->>'created_at')::timestamp"
		amountCol = "(t.j->>'amount')::numeric"
		statusCol = "t.j->>'status'"
		fromTable = "(SELECT to_jsonb(pt) as j FROM payment_transactions pt) t"
	}

	whereParts := []string{}
	args := []any{}

	if strings.TrimSpace(from) != "" {
		args = append(args, strings.TrimSpace(from))
		whereParts = append(whereParts, fmt.Sprintf("%s >= $%d::date", dateCol, len(args)))
	}
	if strings.TrimSpace(to) != "" {
		args = append(args, strings.TrimSpace(to))
		whereParts = append(whereParts, fmt.Sprintf("%s < $%d::date + interval '1 day'", dateCol, len(args)))
	}

	query := fmt.Sprintf(`
		SELECT 
			COUNT(*) as total,
			COUNT(CASE WHEN lower(%s) IN ('success', 'successful', 'completed') THEN 1 END) as success,
			COUNT(CASE WHEN lower(%s) = 'failed' THEN 1 END) as failed,
			COUNT(CASE WHEN lower(%s) = 'pending' THEN 1 END) as pending,
			COALESCE(SUM(%s), 0) as amount
		FROM %s
	`, statusCol, statusCol, statusCol, amountCol, fromTable)

	if len(whereParts) > 0 {
		query += " WHERE " + strings.Join(whereParts, " AND ")
	}

	var stats TransactionStats
	if len(args) > 0 {
		err = r.DB.QueryRowContext(ctx, query, args...).Scan(
			&stats.TotalCount,
			&stats.SuccessCount,
			&stats.FailedCount,
			&stats.PendingCount,
			&stats.TotalAmount,
		)
	} else {
		err = r.DB.QueryRowContext(ctx, query).Scan(
			&stats.TotalCount,
			&stats.SuccessCount,
			&stats.FailedCount,
			&stats.PendingCount,
			&stats.TotalAmount,
		)
	}

	if err != nil {
		return nil, err
	}

	return &stats, nil
}
