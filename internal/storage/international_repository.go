package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// InternationalPayment represents an international payment transaction record.
type InternationalPayment struct {
	ID                               string
	PaymentID                        string
	StripeSessionID                  string
	ReferenceID                      string
	MerchantID                       string
	ExternalMerchantReference        string
	RecipientID                      string
	GatewayProvider                  string
	GatewayCredentialConfigurationID string
	GatewayCredentialVersion         int
	RoutingMode                      string
	Amount                           float64
	TotalAmount                      float64
	Currency                         string
	Status                           string
	ErrorCode                        string
	ErrorMessage                     string
	Description                      string
	CheckoutURL                      string
	CreatedAt                        time.Time
	CompletedAt                      *time.Time
	LastStatusCheckAt                *time.Time
	UpdatedAt                        time.Time
}

// InternationalRepository handles international payment database operations.
type InternationalRepository struct {
	DB *sql.DB
}

// NewInternationalRepository constructs international payment repository.
// Called from: main() for InternationalController.
func NewInternationalRepository(db *DB) *InternationalRepository {
	if db == nil {
		return &InternationalRepository{DB: nil}
	}
	return &InternationalRepository{DB: db.Conn}
}

// Enabled reports whether repository has an active DB handle.
// Called from: controller guard checks.
func (r *InternationalRepository) Enabled() bool {
	return r != nil && r.DB != nil
}

// CreatePayment inserts a new international payment record.
// Why needed: tracks payment session details for status checking and reconciliation.
// Called from: InternationalController.CreatePayment.
func (r *InternationalRepository) CreatePayment(ctx context.Context, payment InternationalPayment) error {
	if !r.Enabled() {
		return fmt.Errorf("international payment repository is not configured")
	}
	if err := validateMerchantRouting(payment.RoutingMode, payment.MerchantID, payment.ExternalMerchantReference, payment.RecipientID, payment.GatewayProvider, payment.GatewayCredentialConfigurationID, payment.GatewayCredentialVersion); err != nil {
		return err
	}

	query := `
		INSERT INTO international_payments 
		(payment_id, stripe_session_id, reference_id, merchant_id, external_merchant_reference, recipient_id, gateway_provider, gateway_credential_configuration_id, gateway_credential_version, routing_mode, amount, total_amount, 
		 currency, status, error_code, error_message, description, checkout_url, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::uuid, $7, $8::uuid, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
	`

	_, err := r.DB.ExecContext(ctx, query,
		payment.PaymentID,
		payment.StripeSessionID,
		payment.ReferenceID,
		payment.MerchantID,
		payment.ExternalMerchantReference,
		payment.RecipientID,
		payment.GatewayProvider,
		payment.GatewayCredentialConfigurationID,
		payment.GatewayCredentialVersion,
		payment.RoutingMode,
		payment.Amount,
		payment.TotalAmount,
		payment.Currency,
		payment.Status,
		payment.ErrorCode,
		payment.ErrorMessage,
		payment.Description,
		payment.CheckoutURL,
		payment.CreatedAt,
		payment.UpdatedAt,
	)
	return err
}

func (r *InternationalRepository) GetPaymentByReferenceForMerchant(ctx context.Context, merchantID, referenceID string) (*InternationalPayment, error) {
	if !r.Enabled() {
		return nil, nil
	}

	query := `
		SELECT id, payment_id, stripe_session_id, reference_id, merchant_id, COALESCE(external_merchant_reference, ''), COALESCE(recipient_id::text, ''), COALESCE(gateway_provider, ''), COALESCE(gateway_credential_configuration_id::text, ''), COALESCE(gateway_credential_version, 0), routing_mode,
		       amount, total_amount, currency, status, error_code, error_message, description, checkout_url,
		       created_at, completed_at, last_status_check_at, updated_at
		FROM international_payments
		WHERE reference_id = $1 AND merchant_id = $2
	`

	var payment InternationalPayment
	var stripeSessionID sql.NullString
	var errorCode sql.NullString
	var errorMessage sql.NullString
	var description sql.NullString
	var checkoutURL sql.NullString
	var lastStatusCheckAt sql.NullTime
	err := r.DB.QueryRowContext(ctx, query, strings.TrimSpace(referenceID), strings.TrimSpace(merchantID)).Scan(
		&payment.ID,
		&payment.PaymentID,
		&stripeSessionID,
		&payment.ReferenceID,
		&payment.MerchantID, &payment.ExternalMerchantReference, &payment.RecipientID, &payment.GatewayProvider, &payment.GatewayCredentialConfigurationID, &payment.GatewayCredentialVersion, &payment.RoutingMode,
		&payment.Amount,
		&payment.TotalAmount,
		&payment.Currency,
		&payment.Status,
		&errorCode,
		&errorMessage,
		&description,
		&checkoutURL,
		&payment.CreatedAt,
		&payment.CompletedAt,
		&lastStatusCheckAt,
		&payment.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	payment.StripeSessionID = nullableString(stripeSessionID)
	payment.ErrorCode = nullableString(errorCode)
	payment.ErrorMessage = nullableString(errorMessage)
	payment.Description = nullableString(description)
	payment.CheckoutURL = nullableString(checkoutURL)
	payment.LastStatusCheckAt = nullableTime(lastStatusCheckAt)
	return &payment, nil
}

// UpdatePaymentStatus updates payment status and completion timestamp.
// Why needed: marks payment as completed when Stripe confirms payment.
// Called from: InternationalController.CheckPaymentStatus.
func (r *InternationalRepository) UpdatePaymentStatus(ctx context.Context, merchantID, referenceID, status string) error {
	if !r.Enabled() {
		return nil
	}

	query := `
		UPDATE international_payments
		SET status = $1, 
		    completed_at = CASE WHEN $1 = 'completed' THEN NOW() ELSE completed_at END,
		    last_status_check_at = NOW(),
		    updated_at = NOW()
		WHERE merchant_id = $2 AND reference_id = $3
	`

	result, err := r.DB.ExecContext(ctx, query, status, merchantID, referenceID)
	return requireOneAffectedRow(result, err)
}

func (r *InternationalRepository) ReferenceExistsForMerchant(ctx context.Context, merchantID, referenceID string) (bool, error) {
	if !r.Enabled() || strings.TrimSpace(merchantID) == "" || strings.TrimSpace(referenceID) == "" {
		return false, nil
	}
	var count int
	err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM international_payments WHERE merchant_id=$1 AND reference_id=$2`, strings.TrimSpace(merchantID), strings.TrimSpace(referenceID)).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// List returns recent international payments, ordered by created_at desc.
// Called from: InternationalController.List (admin endpoint).
func (r *InternationalRepository) List(ctx context.Context, limit int, status, from, to, search string) ([]InternationalPayment, error) {
	if !r.Enabled() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	whereParts := []string{}
	args := []any{}

	if strings.TrimSpace(status) != "" {
		args = append(args, strings.TrimSpace(status))
		whereParts = append(whereParts, fmt.Sprintf("lower(status) = lower($%d)", len(args)))
	}
	if strings.TrimSpace(from) != "" {
		args = append(args, strings.TrimSpace(from))
		whereParts = append(whereParts, fmt.Sprintf("created_at::timestamp >= $%d::date", len(args)))
	}
	if strings.TrimSpace(to) != "" {
		args = append(args, strings.TrimSpace(to))
		whereParts = append(whereParts, fmt.Sprintf("created_at::timestamp < $%d::date + interval '1 day'", len(args)))
	}
	if strings.TrimSpace(search) != "" {
		s := "%" + strings.TrimSpace(search) + "%"
		args = append(args, s)
		idx := len(args)
		whereParts = append(whereParts, fmt.Sprintf(
			"(reference_id ilike $%d OR payment_id ilike $%d OR merchant_id ilike $%d OR description ilike $%d)",
			idx, idx, idx, idx,
		))
	}

	query := `
		SELECT id, payment_id, stripe_session_id, reference_id, merchant_id, COALESCE(external_merchant_reference, ''), COALESCE(recipient_id::text, ''), COALESCE(gateway_provider, ''), COALESCE(gateway_credential_configuration_id::text, ''), COALESCE(gateway_credential_version, 0), routing_mode,
		       amount, total_amount, currency, status, error_code, error_message, description, checkout_url,
		       created_at, completed_at, last_status_check_at, updated_at
		FROM international_payments
	`
	if len(whereParts) > 0 {
		query += " WHERE " + strings.Join(whereParts, " AND ")
	}

	// #nosec G202 -- only placeholder positions are formatted; query values remain parameterized.
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args)+1)
	args = append(args, limit)

	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []InternationalPayment
	for rows.Next() {
		var p InternationalPayment
		var stripeSessionID sql.NullString
		var errorCode sql.NullString
		var errorMessage sql.NullString
		var description sql.NullString
		var checkoutURL sql.NullString
		var lastStatusCheckAt sql.NullTime
		if err := rows.Scan(
			&p.ID, &p.PaymentID, &stripeSessionID, &p.ReferenceID, &p.MerchantID, &p.ExternalMerchantReference, &p.RecipientID, &p.GatewayProvider, &p.GatewayCredentialConfigurationID, &p.GatewayCredentialVersion, &p.RoutingMode,
			&p.Amount, &p.TotalAmount, &p.Currency, &p.Status, &errorCode, &errorMessage, &description, &checkoutURL,
			&p.CreatedAt, &p.CompletedAt, &lastStatusCheckAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		p.StripeSessionID = nullableString(stripeSessionID)
		p.ErrorCode = nullableString(errorCode)
		p.ErrorMessage = nullableString(errorMessage)
		p.Description = nullableString(description)
		p.CheckoutURL = nullableString(checkoutURL)
		p.LastStatusCheckAt = nullableTime(lastStatusCheckAt)
		result = append(result, p)
	}
	return result, rows.Err()
}

// ListPendingForStatusCheck returns pending payments that are due for status reconciliation.
// A payment is due when it has never been checked or was last checked before dueBefore,
// and was created within the last week.
func (r *InternationalRepository) ListPendingForStatusCheck(ctx context.Context, dueBefore time.Time, createdAfter time.Time, limit int) ([]InternationalPayment, error) {
	if !r.Enabled() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT id, payment_id, stripe_session_id, reference_id, merchant_id, COALESCE(external_merchant_reference, ''), COALESCE(recipient_id::text, ''), COALESCE(gateway_provider, ''), COALESCE(gateway_credential_configuration_id::text, ''), COALESCE(gateway_credential_version, 0), routing_mode,
		       amount, total_amount, currency, status, error_code, error_message, description, checkout_url,
		       created_at, completed_at, last_status_check_at, updated_at
		FROM international_payments
		WHERE lower(status) = 'pending'
		  AND (last_status_check_at IS NULL OR last_status_check_at <= $1)
		  AND created_at >= $3
		ORDER BY COALESCE(last_status_check_at, created_at) ASC, created_at ASC
		LIMIT $2
	`
	rows, err := r.DB.QueryContext(ctx, query, dueBefore.UTC(), limit, createdAfter.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]InternationalPayment, 0, limit)
	for rows.Next() {
		var p InternationalPayment
		var stripeSessionID sql.NullString
		var errorCode sql.NullString
		var errorMessage sql.NullString
		var description sql.NullString
		var checkoutURL sql.NullString
		var lastStatusCheckAt sql.NullTime
		if err := rows.Scan(
			&p.ID, &p.PaymentID, &stripeSessionID, &p.ReferenceID, &p.MerchantID, &p.ExternalMerchantReference, &p.RecipientID, &p.GatewayProvider, &p.GatewayCredentialConfigurationID, &p.GatewayCredentialVersion, &p.RoutingMode,
			&p.Amount, &p.TotalAmount, &p.Currency, &p.Status, &errorCode, &errorMessage, &description, &checkoutURL,
			&p.CreatedAt, &p.CompletedAt, &lastStatusCheckAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		p.StripeSessionID = nullableString(stripeSessionID)
		p.ErrorCode = nullableString(errorCode)
		p.ErrorMessage = nullableString(errorMessage)
		p.Description = nullableString(description)
		p.CheckoutURL = nullableString(checkoutURL)
		p.LastStatusCheckAt = nullableTime(lastStatusCheckAt)
		result = append(result, p)
	}
	return result, rows.Err()
}

// MarkStatusCheckAttempt records when status reconciliation was last attempted.
func (r *InternationalRepository) MarkStatusCheckAttempt(ctx context.Context, merchantID, referenceID string, checkedAt time.Time) error {
	if !r.Enabled() {
		return nil
	}
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}

	result, err := r.DB.ExecContext(ctx, `
		UPDATE international_payments
		SET last_status_check_at = $1,
		    updated_at = NOW()
		WHERE merchant_id = $2 AND reference_id = $3
	`, checkedAt.UTC(), merchantID, referenceID)
	return requireOneAffectedRow(result, err)
}

func nullableString(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

func nullableTime(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}
