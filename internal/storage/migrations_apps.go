package storage

import "fmt"

// EnsureAppsSchema creates or updates the external_apps table with all required fields.
// Why needed: supports multi-application management with API keys, webhooks, and contact info.
// Called from: main() after storage.Connect.
func EnsureAppsSchema(db *DB) error {
	if db == nil || db.Conn == nil {
		return nil
	}

	// Add missing columns to existing external_apps table
	migrations := []string{
		// Add api_secret column if it doesn't exist
		`ALTER TABLE external_apps ADD COLUMN IF NOT EXISTS api_secret VARCHAR(255)`,
		
		// Add contact information columns
		`ALTER TABLE external_apps ADD COLUMN IF NOT EXISTS contact_name VARCHAR(200)`,
		`ALTER TABLE external_apps ADD COLUMN IF NOT EXISTS contact_email VARCHAR(100)`,
		`ALTER TABLE external_apps ADD COLUMN IF NOT EXISTS contact_phone VARCHAR(20)`,
		
		// Add timestamp columns
		`ALTER TABLE external_apps ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP`,
		`ALTER TABLE external_apps ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMP`,
		
		// Create index on api_key for faster lookups
		`CREATE INDEX IF NOT EXISTS idx_external_apps_api_key ON external_apps(api_key)`,
		
		// Create index on is_active for filtering
		`CREATE INDEX IF NOT EXISTS idx_external_apps_is_active ON external_apps(is_active)`,
	}

	for _, migration := range migrations {
		if _, err := db.Conn.Exec(migration); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	return nil
}
