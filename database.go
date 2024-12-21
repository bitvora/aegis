package main

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3" // SQLite driver
)

func migrateDB(db *sql.DB) error {
	query := `
	CREATE TABLE IF NOT EXISTS subscriptions (
		pubkey TEXT PRIMARY KEY,
		npub TEXT,
		active BOOLEAN,
		paid_at TIMESTAMP,
		expires_at TIMESTAMP
	);`

	_, err := db.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}
