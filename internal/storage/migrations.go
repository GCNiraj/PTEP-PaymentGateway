package storage

import "fmt"

// EnsureIndexes creates required indexes used by transaction uniqueness checks.
// Why needed: duplicate order-id prevention depends on this partial unique index.
// Called from: main() after storage.Connect.
func EnsureIndexes(db *DB) error {
	if db == nil || db.Conn == nil {
		return nil
	}
	const q = `
		create unique index if not exists payment_transactions_order_id_uniq
		on payment_transactions (order_id)
		where order_id is not null;
	`
	if _, err := db.Conn.Exec(q); err != nil {
		return fmt.Errorf("ensure indexes: %w", err)
	}
	return nil
}
