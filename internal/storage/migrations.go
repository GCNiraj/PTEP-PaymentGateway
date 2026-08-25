package storage

import "fmt"

// EnsureIndexes creates tenant-scoped indexes used by transaction uniqueness checks.
// Called from: main() after storage.Connect.
func EnsureIndexes(db *DB) error {
	if db == nil || db.Conn == nil {
		return nil
	}
	const q = `
		create unique index if not exists payment_transactions_app_order_id_uniq
		on payment_transactions (external_app_id, order_id)
		where external_app_id is not null and order_id is not null;
	`
	if _, err := db.Conn.Exec(q); err != nil {
		return fmt.Errorf("ensure indexes: %w", err)
	}
	return nil
}
