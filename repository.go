package main

import (
	"fmt"
)

func createSubscription(npub string) error {
	pubkey := nPubToPubkey(npub)
	query := `
	INSERT INTO subscriptions (pubkey, npub, active, paid_at, expires_at)
	VALUES (?, ?, false, NULL, NULL)
	ON CONFLICT(pubkey) DO NOTHING;
	`

	_, err := sqlDB.Exec(query, pubkey, npub)
	if err != nil {
		return fmt.Errorf("failed to create subscription: %w", err)
	}

	return nil
}

func setPaidSubscription(npub string) error {
	pubkey := nPubToPubkey(npub)
	query := `
	UPDATE subscriptions
	SET active = true, paid_at = CURRENT_TIMESTAMP, expires_at = datetime('now', '+1 year')
	WHERE pubkey = ?;
	`

	_, err := sqlDB.Exec(query, pubkey)
	if err != nil {
		return fmt.Errorf("failed to set paid subscription: %w", err)
	}

	return nil
}

func pollPayment(npub string) bool {
	pubkey := nPubToPubkey(npub)
	query := `
	SELECT active FROM subscriptions
	WHERE pubkey = ?;
	`

	var active bool
	err := sqlDB.QueryRow(query, pubkey).Scan(&active)
	if err != nil {
		return false
	}

	return active
}
