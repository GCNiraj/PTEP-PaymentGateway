package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type DB struct {
	Conn *sql.DB
}

// Connect initializes SQL DB connection pool and optional startup migrations.
// Why needed: repositories require a live *sql.DB for persistence operations.
// Called from: main() during startup.
// Behavior: returns DB with nil Conn when databaseURL is empty (DB disabled mode).
func Connect(databaseURL string) (*DB, error) {
	if databaseURL == "" {
		return &DB{Conn: nil}, nil
	}

	// Use pgx native ParseConfig so we can set QueryExecModeSimpleProtocol
	// directly on the config object — the only reliable way in pgx/v5.
	//
	// WHY SIMPLE PROTOCOL:
	//   pgx/v5 default is "extended query protocol" which caches a prepared
	//   statement per physical DB connection. When the pool recycles a
	//   connection (SetConnMaxLifetime / SetConnMaxIdleTime, or the server
	//   closes an idle backend), the server-side prepared statement vanishes
	//   but pgx still tries to execute it → "driver: bad connection".
	//   Simple protocol sends the full SQL text on every call: no cache,
	//   no stale-statement failures, fully PostgreSQL-compatible.
	connConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	conn := stdlib.OpenDB(*connConfig)

	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(3 * time.Minute)
	conn.SetConnMaxIdleTime(1 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		return nil, err
	}

	db := &DB{Conn: conn}

	// Auto-run migrations
	if err := db.RunMigrations(); err != nil {
		// Log but don't fail - migrations are optional
		println("Warning: migrations failed:", err.Error())
	}

	return db, nil
}

// RunMigrations applies lightweight SQL migrations for backward compatibility.
// Why needed: tolerates older schemas by adding missing intra_inquiries columns/indexes.
// Called from: Connect immediately after successful ping.
func (db *DB) RunMigrations() error {
	if db.Conn == nil {
		return nil
	}

	migrations := []string{
		// Link intra transfer transactions back to inquiry rows.
		`ALTER TABLE payment_transactions ADD COLUMN IF NOT EXISTS inquiry_id text`,
		`CREATE INDEX IF NOT EXISTS payment_transactions_inquiry_id_idx ON payment_transactions (inquiry_id)`,
		// Add status tracking columns to intra_inquiries
		`ALTER TABLE intra_inquiries ADD COLUMN IF NOT EXISTS order_id text`,
		`ALTER TABLE intra_inquiries ADD COLUMN IF NOT EXISTS status text`,
		`ALTER TABLE intra_inquiries ADD COLUMN IF NOT EXISTS error_code text`,
		`ALTER TABLE intra_inquiries ADD COLUMN IF NOT EXISTS error_message text`,
		// Backfill inquiry order_id from transfers where possible.
		`UPDATE intra_inquiries i
		 SET order_id = p.order_id
		FROM payment_transactions p
		WHERE i.order_id IS NULL
		  AND i.inquiry_id = p.inquiry_id
		  AND p.order_id IS NOT NULL`,
		// Ensure unique constraint for inquiry_id
		`CREATE UNIQUE INDEX IF NOT EXISTS intra_inquiries_inquiry_id_key ON intra_inquiries (inquiry_id)`,
		// Prevent duplicate external references in inquiry flow.
		`CREATE UNIQUE INDEX IF NOT EXISTS intra_inquiries_order_id_uniq ON intra_inquiries (order_id) WHERE order_id IS NOT NULL`,
	}

	for _, migration := range migrations {
		if _, err := db.Conn.Exec(migration); err != nil {
			return err
		}
	}

	return nil
}
