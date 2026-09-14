package phoenix

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// marketCount returns how many markets are already persisted, so Create
// can skip re-fetching from the chain on repeat runs against the same
// database.
func marketCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM phoenix_market`).Scan(&n); err != nil {
		return 0, fmt.Errorf("phoenix: count markets: %w", err)
	}
	return n, nil
}

// insertMarkets persists markets as the source of truth for future runs.
func insertMarkets(db *sql.DB, markets []*Market) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("phoenix: begin: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO phoenix_market
		(market_account, symbol, asset_id) VALUES (?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("phoenix: prepare insert: %w", err)
	}
	for _, m := range markets {
		if _, err = stmt.Exec(m.MarketAccount[:], m.Symbol, m.AssetID); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("phoenix: insert market %s: %w", m.MarketAccount, err)
		}
	}
	if err = stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("phoenix: close stmt: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("phoenix: commit: %w", err)
	}
	return nil
}

// loadMarkets reconstructs the in-memory market list from the database.
func loadMarkets(db *sql.DB) ([]*Market, error) {
	rows, err := db.Query(`SELECT market_account, symbol, asset_id FROM phoenix_market`)
	if err != nil {
		return nil, fmt.Errorf("phoenix: query markets: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []*Market
	for rows.Next() {
		var marketAccount []byte
		m := new(Market)
		if err = rows.Scan(&marketAccount, &m.Symbol, &m.AssetID); err != nil {
			return nil, fmt.Errorf("phoenix: scan market: %w", err)
		}
		m.MarketAccount = sgo.PublicKeyFromBytes(marketAccount)
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("phoenix: iterate markets: %w", err)
	}
	return out, nil
}
