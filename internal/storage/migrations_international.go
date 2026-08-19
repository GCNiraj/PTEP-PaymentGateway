package storage

import (
	"context"
	"database/sql"
)

// RunInternationalMigrations creates tables and indexes for international payments.
// Why needed: sets up database schema for Stripe payment tracking.
// Called from: main() during application startup.
func RunInternationalMigrations(db *sql.DB) error {
	if db == nil {
		return nil
	}

	ctx := context.Background()

	// Add merchant fields to external_apps table
	_, err := db.ExecContext(ctx, `
		ALTER TABLE external_apps 
		ADD COLUMN IF NOT EXISTS agency_name VARCHAR(255),
		ADD COLUMN IF NOT EXISTS submerchant_id VARCHAR(255),
		ADD COLUMN IF NOT EXISTS dk_account VARCHAR(255)
	`)
	if err != nil {
		return err
	}

	// Create international_payments table
	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS international_payments (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			payment_id VARCHAR(255) NOT NULL UNIQUE,
			stripe_session_id VARCHAR(255) NOT NULL,
			reference_id VARCHAR(255) NOT NULL UNIQUE,
			merchant_id VARCHAR(255) NOT NULL,
			amount DECIMAL(10,2) NOT NULL,
			total_amount DECIMAL(10,2) NOT NULL,
			currency VARCHAR(3) NOT NULL DEFAULT 'usd',
			status VARCHAR(50) NOT NULL,
			error_code VARCHAR(100),
			error_message TEXT,
			description TEXT,
			checkout_url TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT NOW(),
			completed_at TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
			FOREIGN KEY (merchant_id) REFERENCES external_apps(id)
		)
	`)
	if err != nil {
		return err
	}

	// Ensure error columns exist on older deployments.
	_, err = db.ExecContext(ctx, `
		ALTER TABLE international_payments
		ADD COLUMN IF NOT EXISTS error_code VARCHAR(100),
		ADD COLUMN IF NOT EXISTS error_message TEXT,
		ADD COLUMN IF NOT EXISTS last_status_check_at TIMESTAMP
	`)
	if err != nil {
		return err
	}

	// Create indexes
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_intl_payments_reference ON international_payments(reference_id)`,
		`CREATE INDEX IF NOT EXISTS idx_intl_payments_merchant ON international_payments(merchant_id)`,
		`CREATE INDEX IF NOT EXISTS idx_intl_payments_stripe_session ON international_payments(stripe_session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_intl_payments_status ON international_payments(status)`,
		`CREATE INDEX IF NOT EXISTS idx_intl_payments_pending_checks ON international_payments(status, last_status_check_at)`,
	}

	for _, idx := range indexes {
		if _, err := db.ExecContext(ctx, idx); err != nil {
			return err
		}
	}

	return nil
}
