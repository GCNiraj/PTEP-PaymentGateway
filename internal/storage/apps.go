package storage

import (
	"context"
	"database/sql"
)

type ExternalApp struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	APIKey        string  `json:"api_key"`    // #nosec G117 -- field names are part of API/storage model contract.
	APISecret     string  `json:"api_secret"` // #nosec G117 -- field names are part of API/storage model contract.
	WebhookURL    string  `json:"-"`
	IsActive      bool    `json:"is_active"`
	ContactName   string  `json:"contact_name"`
	ContactEmail  string  `json:"contact_email"`
	ContactPhone  string  `json:"contact_phone"`
	AgencyName    string  `json:"agency_name"`
	SubmerchantID string  `json:"submerchant_id"`
	DKAccount     string  `json:"dk_account"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	LastUsedAt    *string `json:"last_used_at,omitempty"`
}

type AppsRepository struct {
	DB *sql.DB
}

// NewAppsRepository constructs external-app repository from shared DB wrapper.
// Called from: main() for AppsController.
func NewAppsRepository(db *DB) *AppsRepository {
	if db == nil {
		return &AppsRepository{DB: nil}
	}
	return &AppsRepository{DB: db.Conn}
}

// Enabled reports whether repository has an active DB handle.
// Called from: controller guard checks.
func (r *AppsRepository) Enabled() bool {
	return r != nil && r.DB != nil
}

// List fetches external application records for admin visibility.
// Called from: AppsController.List.
// Response semantics: returns nil,nil when DB is disabled.
func (r *AppsRepository) List(ctx context.Context, limit int) ([]ExternalApp, error) {
	if !r.Enabled() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}

	rows, err := r.DB.QueryContext(ctx, `
		select id, name, api_key, api_secret, webhook_url, is_active, 
		       contact_name, contact_email, contact_phone,
		       agency_name, submerchant_id, dk_account,
		       created_at::text, updated_at::text, last_used_at::text
		from external_apps
		order by created_at desc
		limit $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ExternalApp
	for rows.Next() {
		var app ExternalApp
		if err := rows.Scan(&app.ID, &app.Name, &app.APIKey, &app.APISecret, &app.WebhookURL, &app.IsActive,
			&app.ContactName, &app.ContactEmail, &app.ContactPhone,
			&app.AgencyName, &app.SubmerchantID, &app.DKAccount,
			&app.CreatedAt, &app.UpdatedAt, &app.LastUsedAt); err != nil {
			return nil, err
		}
		result = append(result, app)
	}
	return result, rows.Err()
}

// ListForAuth fetches app fields required by API-key auth without applying a limit.
// Why needed: auth must check all stored keys to avoid false negatives when app count grows.
// Called from: API key authentication middleware.
func (r *AppsRepository) ListForAuth(ctx context.Context) ([]ExternalApp, error) {
	if !r.Enabled() {
		return nil, nil
	}

	rows, err := r.DB.QueryContext(ctx, `
		select id, name, api_key, is_active
		from external_apps
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ExternalApp
	for rows.Next() {
		var app ExternalApp
		if err := rows.Scan(&app.ID, &app.Name, &app.APIKey, &app.IsActive); err != nil {
			return nil, err
		}
		result = append(result, app)
	}
	return result, rows.Err()
}

// Create inserts a new external application record.
// Why needed: allows admins to register new applications for API access.
// Called from: AppsController.Create.
func (r *AppsRepository) Create(ctx context.Context, app ExternalApp) error {
	if !r.Enabled() {
		return nil
	}

	query := `
		insert into external_apps 
		(id, name, api_key, api_secret, webhook_url, is_active, contact_name, contact_email, contact_phone, agency_name, submerchant_id, dk_account, created_at, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now(), now())
	`

	_, err := r.DB.ExecContext(ctx, query,
		app.ID,
		app.Name,
		app.APIKey,
		app.APISecret,
		app.WebhookURL,
		app.IsActive,
		app.ContactName,
		app.ContactEmail,
		app.ContactPhone,
		app.AgencyName,
		app.SubmerchantID,
		app.DKAccount,
	)
	return err
}

// FindByID retrieves an external app by its ID.
// Why needed: allows lookup of specific app details.
// Called from: AppsController.Get, Update, Delete.
func (r *AppsRepository) FindByID(ctx context.Context, id string) (*ExternalApp, error) {
	if !r.Enabled() {
		return nil, nil
	}

	var app ExternalApp
	query := `
		select id, name, api_key, api_secret, webhook_url, is_active,
		       contact_name, contact_email, contact_phone,
		       agency_name, submerchant_id, dk_account,
		       created_at::text, updated_at::text, last_used_at::text
		from external_apps
		where id = $1
	`

	err := r.DB.QueryRowContext(ctx, query, id).Scan(
		&app.ID, &app.Name, &app.APIKey, &app.APISecret, &app.WebhookURL, &app.IsActive,
		&app.ContactName, &app.ContactEmail, &app.ContactPhone,
		&app.AgencyName, &app.SubmerchantID, &app.DKAccount,
		&app.CreatedAt, &app.UpdatedAt, &app.LastUsedAt,
	)
	if err != nil {
		return nil, err
	}
	return &app, nil
}

// FindByAPIKey retrieves an external app by its API key (hashed).
// Why needed: authentication middleware needs to find app by API key.
// Called from: API key authentication middleware.
func (r *AppsRepository) FindByAPIKey(ctx context.Context, apiKey string) (*ExternalApp, error) {
	if !r.Enabled() {
		return nil, nil
	}

	var app ExternalApp
	query := `
		select id, name, api_key, api_secret, webhook_url, is_active,
		       contact_name, contact_email, contact_phone,
		       agency_name, submerchant_id, dk_account,
		       created_at::text, updated_at::text, last_used_at::text
		from external_apps
		where api_key = $1
	`

	err := r.DB.QueryRowContext(ctx, query, apiKey).Scan(
		&app.ID, &app.Name, &app.APIKey, &app.APISecret, &app.WebhookURL, &app.IsActive,
		&app.ContactName, &app.ContactEmail, &app.ContactPhone,
		&app.AgencyName, &app.SubmerchantID, &app.DKAccount,
		&app.CreatedAt, &app.UpdatedAt, &app.LastUsedAt,
	)
	if err != nil {
		return nil, err
	}
	return &app, nil
}

// Update modifies an existing external app record.
// Why needed: allows admins to update app configuration.
// Called from: AppsController.Update.
func (r *AppsRepository) Update(ctx context.Context, app ExternalApp) error {
	if !r.Enabled() {
		return nil
	}

	query := `
		update external_apps
		set name = $1, webhook_url = $2, is_active = $3,
		    contact_name = $4, contact_email = $5, contact_phone = $6,
		    agency_name = $7, submerchant_id = $8, dk_account = $9,
		    updated_at = now()
		where id = $10
	`

	_, err := r.DB.ExecContext(ctx, query,
		app.Name,
		app.WebhookURL,
		app.IsActive,
		app.ContactName,
		app.ContactEmail,
		app.ContactPhone,
		app.AgencyName,
		app.SubmerchantID,
		app.DKAccount,
		app.ID,
	)
	return err
}

// Delete removes an external app by ID.
// Why needed: allows admins to revoke access entirely.
// Called from: AppsController.Delete.
func (r *AppsRepository) Delete(ctx context.Context, id string) error {
	if !r.Enabled() {
		return nil
	}
	query := `delete from external_apps where id = $1`
	_, err := r.DB.ExecContext(ctx, query, id)
	return err
}

// UpdateAPIKey updates the API key for an app (after regeneration).
// Why needed: allows admins to regenerate compromised API keys.
// Called from: AppsController.RegenerateKey.
func (r *AppsRepository) UpdateAPIKey(ctx context.Context, id, newAPIKey string) error {
	if !r.Enabled() {
		return nil
	}

	query := `update external_apps set api_key = $1, updated_at = now() where id = $2`
	_, err := r.DB.ExecContext(ctx, query, newAPIKey, id)
	return err
}

// UpdateLastUsed updates the last_used_at timestamp for an app.
// Why needed: tracks app activity for monitoring and analytics.
// Called from: API key authentication middleware after successful auth.
func (r *AppsRepository) UpdateLastUsed(ctx context.Context, id string) error {
	if !r.Enabled() {
		return nil
	}

	query := `update external_apps set last_used_at = now() where id = $1`
	_, err := r.DB.ExecContext(ctx, query, id)
	return err
}

// GetStats retrieves statistics for a specific app.
// Why needed: provides insights into app usage and transaction metrics.
// Called from: AppsController.GetStats.
func (r *AppsRepository) GetStats(ctx context.Context, appID string) (map[string]interface{}, error) {
	if !r.Enabled() {
		return nil, nil
	}

	query := `
		select 
			count(*) as total_transactions,
			count(*) filter (where status = 'COMPLETED') as successful_transactions,
			count(*) filter (where status = 'FAILED') as failed_transactions,
			count(*) filter (where status = 'PENDING') as pending_transactions,
			coalesce(sum(amount) filter (where status = 'COMPLETED'), 0) as total_amount
		from payment_transactions
		where external_app_id = $1
	`

	var totalTxns, successfulTxns, failedTxns, pendingTxns int64
	var totalAmount float64

	err := r.DB.QueryRowContext(ctx, query, appID).Scan(
		&totalTxns, &successfulTxns, &failedTxns, &pendingTxns, &totalAmount,
	)
	if err != nil {
		return nil, err
	}

	stats := map[string]interface{}{
		"total_transactions":      totalTxns,
		"successful_transactions": successfulTxns,
		"failed_transactions":     failedTxns,
		"pending_transactions":    pendingTxns,
		"total_amount":            totalAmount,
	}

	if totalTxns > 0 {
		stats["success_rate"] = float64(successfulTxns) / float64(totalTxns) * 100
	} else {
		stats["success_rate"] = 0.0
	}

	return stats, nil
}
