package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// RunMerchantRoutingMigrations installs the tenant-routing tables and immutable
// transaction links used to select a business-specific gateway configuration.
func RunMerchantRoutingMigrations(db *sql.DB) error {
	if db == nil {
		return nil
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	statements := []string{
		`CREATE TABLE IF NOT EXISTS payment_recipients (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name TEXT NOT NULL,
			is_active BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			created_by TEXT NOT NULL DEFAULT '',
			updated_by TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS integration_recipient_mappings (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			external_app_id TEXT NOT NULL REFERENCES external_apps(id) ON DELETE RESTRICT,
			external_merchant_reference TEXT NOT NULL,
			recipient_id UUID NOT NULL REFERENCES payment_recipients(id) ON DELETE RESTRICT,
			is_active BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			created_by TEXT NOT NULL DEFAULT '',
			updated_by TEXT NOT NULL DEFAULT '',
			CHECK (length(trim(external_merchant_reference)) > 0)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS integration_recipient_active_reference_uniq
			ON integration_recipient_mappings (external_app_id, external_merchant_reference)
			WHERE is_active`,
		`CREATE INDEX IF NOT EXISTS integration_recipient_recipient_idx
			ON integration_recipient_mappings (recipient_id)`,
		`CREATE TABLE IF NOT EXISTS gateway_credential_configurations (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			recipient_id UUID NOT NULL REFERENCES payment_recipients(id) ON DELETE RESTRICT,
			provider TEXT NOT NULL,
			version INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'draft',
			encrypted_credentials TEXT NOT NULL,
			encryption_key_id TEXT NOT NULL DEFAULT 'env:PAYMENT_CREDENTIALS_MASTER_KEY_B64',
			rotated_from_id UUID NULL REFERENCES gateway_credential_configurations(id) ON DELETE RESTRICT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			activated_at TIMESTAMPTZ NULL,
			disabled_at TIMESTAMPTZ NULL,
			created_by TEXT NOT NULL DEFAULT '',
			updated_by TEXT NOT NULL DEFAULT '',
			CHECK (provider IN ('dkpg', 'stripe')),
			CHECK (status IN ('draft', 'active', 'disabled', 'retired')),
			CHECK (version > 0),
			UNIQUE (recipient_id, provider, version)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS gateway_credential_one_active_uniq
			ON gateway_credential_configurations (recipient_id, provider)
			WHERE status = 'active'`,
		`CREATE INDEX IF NOT EXISTS gateway_credential_resolve_idx
			ON gateway_credential_configurations (recipient_id, provider, status)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS gateway_credential_identity_uniq
			ON gateway_credential_configurations (id, recipient_id, provider, version)`,
		`ALTER TABLE payment_transactions ADD COLUMN IF NOT EXISTS external_merchant_reference TEXT`,
		`ALTER TABLE payment_transactions ADD COLUMN IF NOT EXISTS recipient_id UUID`,
		`ALTER TABLE payment_transactions ADD COLUMN IF NOT EXISTS gateway_provider TEXT`,
		`ALTER TABLE payment_transactions ADD COLUMN IF NOT EXISTS gateway_credential_configuration_id UUID`,
		`ALTER TABLE payment_transactions ADD COLUMN IF NOT EXISTS gateway_credential_version INTEGER`,
		`ALTER TABLE payment_transactions ADD COLUMN IF NOT EXISTS routing_mode TEXT`,
		`ALTER TABLE international_payments ADD COLUMN IF NOT EXISTS external_merchant_reference TEXT`,
		`ALTER TABLE international_payments ADD COLUMN IF NOT EXISTS recipient_id UUID`,
		`ALTER TABLE international_payments ADD COLUMN IF NOT EXISTS gateway_provider TEXT`,
		`ALTER TABLE international_payments ADD COLUMN IF NOT EXISTS gateway_credential_configuration_id UUID`,
		`ALTER TABLE international_payments ADD COLUMN IF NOT EXISTS gateway_credential_version INTEGER`,
		`ALTER TABLE international_payments ADD COLUMN IF NOT EXISTS routing_mode TEXT`,
		`ALTER TABLE intra_inquiries ADD COLUMN IF NOT EXISTS external_app_id TEXT`,
		`ALTER TABLE intra_inquiries ADD COLUMN IF NOT EXISTS external_merchant_reference TEXT`,
		`ALTER TABLE intra_inquiries ADD COLUMN IF NOT EXISTS recipient_id UUID`,
		`ALTER TABLE intra_inquiries ADD COLUMN IF NOT EXISTS gateway_provider TEXT`,
		`ALTER TABLE intra_inquiries ADD COLUMN IF NOT EXISTS gateway_credential_configuration_id UUID`,
		`ALTER TABLE intra_inquiries ADD COLUMN IF NOT EXISTS gateway_credential_version INTEGER`,
		`ALTER TABLE intra_inquiries ADD COLUMN IF NOT EXISTS routing_mode TEXT`,
		`UPDATE payment_transactions SET routing_mode='legacy' WHERE routing_mode IS NULL`,
		`UPDATE international_payments SET routing_mode='legacy' WHERE routing_mode IS NULL`,
		`UPDATE intra_inquiries SET routing_mode='legacy' WHERE routing_mode IS NULL`,
		`ALTER TABLE payment_transactions ALTER COLUMN routing_mode SET NOT NULL`,
		`ALTER TABLE international_payments ALTER COLUMN routing_mode SET NOT NULL`,
		`ALTER TABLE intra_inquiries ALTER COLUMN routing_mode SET NOT NULL`,
		`CREATE INDEX IF NOT EXISTS payment_transactions_app_merchant_reference_idx
			ON payment_transactions (external_app_id, external_merchant_reference)`,
		`CREATE INDEX IF NOT EXISTS payment_transactions_credential_configuration_idx
			ON payment_transactions (gateway_credential_configuration_id)`,
		`CREATE INDEX IF NOT EXISTS international_payments_merchant_reference_idx
			ON international_payments (merchant_id, external_merchant_reference)`,
		`CREATE INDEX IF NOT EXISTS international_payments_credential_configuration_idx
			ON international_payments (gateway_credential_configuration_id)`,
		`CREATE INDEX IF NOT EXISTS intra_inquiries_app_merchant_reference_idx
			ON intra_inquiries (external_app_id, external_merchant_reference)`,
		`DROP INDEX IF EXISTS payment_transactions_order_id_uniq`,
		`CREATE UNIQUE INDEX IF NOT EXISTS payment_transactions_app_order_id_uniq
			ON payment_transactions (external_app_id, order_id)
			WHERE external_app_id IS NOT NULL AND order_id IS NOT NULL`,
		`DROP INDEX IF EXISTS intra_inquiries_order_id_uniq`,
		`CREATE UNIQUE INDEX IF NOT EXISTS intra_inquiries_app_order_id_uniq
			ON intra_inquiries (external_app_id, order_id)
			WHERE external_app_id IS NOT NULL AND order_id IS NOT NULL`,
		`ALTER TABLE international_payments DROP CONSTRAINT IF EXISTS international_payments_reference_id_key`,
		`CREATE UNIQUE INDEX IF NOT EXISTS international_payments_merchant_reference_id_uniq
			ON international_payments (merchant_id, reference_id)`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='payment_transactions_recipient_id_fkey') THEN
				ALTER TABLE payment_transactions ADD CONSTRAINT payment_transactions_recipient_id_fkey FOREIGN KEY (recipient_id) REFERENCES payment_recipients(id) ON DELETE RESTRICT;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='payment_transactions_credential_configuration_id_fkey') THEN
				ALTER TABLE payment_transactions ADD CONSTRAINT payment_transactions_credential_configuration_id_fkey FOREIGN KEY (gateway_credential_configuration_id) REFERENCES gateway_credential_configurations(id) ON DELETE RESTRICT;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='international_payments_recipient_id_fkey') THEN
				ALTER TABLE international_payments ADD CONSTRAINT international_payments_recipient_id_fkey FOREIGN KEY (recipient_id) REFERENCES payment_recipients(id) ON DELETE RESTRICT;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='international_payments_credential_configuration_id_fkey') THEN
				ALTER TABLE international_payments ADD CONSTRAINT international_payments_credential_configuration_id_fkey FOREIGN KEY (gateway_credential_configuration_id) REFERENCES gateway_credential_configurations(id) ON DELETE RESTRICT;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='intra_inquiries_recipient_id_fkey') THEN
				ALTER TABLE intra_inquiries ADD CONSTRAINT intra_inquiries_recipient_id_fkey FOREIGN KEY (recipient_id) REFERENCES payment_recipients(id) ON DELETE RESTRICT;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='intra_inquiries_credential_configuration_id_fkey') THEN
				ALTER TABLE intra_inquiries ADD CONSTRAINT intra_inquiries_credential_configuration_id_fkey FOREIGN KEY (gateway_credential_configuration_id) REFERENCES gateway_credential_configurations(id) ON DELETE RESTRICT;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='payment_transactions_credential_identity_fkey') THEN
				ALTER TABLE payment_transactions ADD CONSTRAINT payment_transactions_credential_identity_fkey FOREIGN KEY (gateway_credential_configuration_id, recipient_id, gateway_provider, gateway_credential_version) REFERENCES gateway_credential_configurations(id, recipient_id, provider, version) ON DELETE RESTRICT;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='international_payments_credential_identity_fkey') THEN
				ALTER TABLE international_payments ADD CONSTRAINT international_payments_credential_identity_fkey FOREIGN KEY (gateway_credential_configuration_id, recipient_id, gateway_provider, gateway_credential_version) REFERENCES gateway_credential_configurations(id, recipient_id, provider, version) ON DELETE RESTRICT;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='intra_inquiries_credential_identity_fkey') THEN
				ALTER TABLE intra_inquiries ADD CONSTRAINT intra_inquiries_credential_identity_fkey FOREIGN KEY (gateway_credential_configuration_id, recipient_id, gateway_provider, gateway_credential_version) REFERENCES gateway_credential_configurations(id, recipient_id, provider, version) ON DELETE RESTRICT;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='payment_transactions_routing_mode_check') THEN
				ALTER TABLE payment_transactions ADD CONSTRAINT payment_transactions_routing_mode_check CHECK (routing_mode = 'legacy' OR (routing_mode = 'merchant' AND external_app_id IS NOT NULL AND NULLIF(trim(external_merchant_reference), '') IS NOT NULL AND recipient_id IS NOT NULL AND NULLIF(trim(gateway_provider), '') IS NOT NULL AND gateway_credential_configuration_id IS NOT NULL AND gateway_credential_version IS NOT NULL));
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='international_payments_routing_mode_check') THEN
				ALTER TABLE international_payments ADD CONSTRAINT international_payments_routing_mode_check CHECK (routing_mode = 'legacy' OR (routing_mode = 'merchant' AND merchant_id IS NOT NULL AND NULLIF(trim(external_merchant_reference), '') IS NOT NULL AND recipient_id IS NOT NULL AND NULLIF(trim(gateway_provider), '') IS NOT NULL AND gateway_credential_configuration_id IS NOT NULL AND gateway_credential_version IS NOT NULL));
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='intra_inquiries_routing_mode_check') THEN
				ALTER TABLE intra_inquiries ADD CONSTRAINT intra_inquiries_routing_mode_check CHECK (routing_mode = 'legacy' OR (routing_mode = 'merchant' AND external_app_id IS NOT NULL AND NULLIF(trim(external_merchant_reference), '') IS NOT NULL AND recipient_id IS NOT NULL AND NULLIF(trim(gateway_provider), '') IS NOT NULL AND gateway_credential_configuration_id IS NOT NULL AND gateway_credential_version IS NOT NULL));
			END IF;
		END $$`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(context.Background(), statement); err != nil {
			return fmt.Errorf("merchant routing migration: %w", err)
		}
	}
	return tx.Commit()
}
