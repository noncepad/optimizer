package drift

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// spotMarketCount returns how many spot markets are already persisted, so
// Create can skip re-fetching from the chain on repeat runs against the
// same database.
func spotMarketCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM drift_spot_market`).Scan(&n); err != nil {
		return 0, fmt.Errorf("drift: count spot markets: %w", err)
	}
	return n, nil
}

// insertSpotMarkets persists spot markets as the source of truth for
// future runs.
func insertSpotMarkets(db *sql.DB, markets []*SpotMarket) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("drift: begin: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO drift_spot_market
		(pubkey, mint, vault)
		VALUES (?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("drift: prepare insert: %w", err)
	}
	for _, m := range markets {
		if _, err = stmt.Exec(m.Pubkey[:], m.Mint[:], m.Vault[:]); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("drift: insert spot market %s: %w", m.Pubkey, err)
		}
	}
	if err = stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("drift: close stmt: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("drift: commit: %w", err)
	}
	return nil
}

// loadSpotMarkets reconstructs the in-memory spot market list from the
// database.
func loadSpotMarkets(db *sql.DB) ([]*SpotMarket, map[sgo.PublicKey]int, error) {
	rows, err := db.Query(`SELECT pubkey, mint, vault FROM drift_spot_market`)
	if err != nil {
		return nil, nil, fmt.Errorf("drift: query spot markets: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var markets []*SpotMarket
	mMarket := make(map[sgo.PublicKey]int)
	for rows.Next() {
		var pubkey, mint, vault []byte
		if err = rows.Scan(&pubkey, &mint, &vault); err != nil {
			return nil, nil, fmt.Errorf("drift: scan spot market: %w", err)
		}
		m := &SpotMarket{
			Pubkey: sgo.PublicKeyFromBytes(pubkey),
			Mint:   sgo.PublicKeyFromBytes(mint),
			Vault:  sgo.PublicKeyFromBytes(vault),
		}
		mMarket[m.Pubkey] = len(markets)
		markets = append(markets, m)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("drift: iterate spot markets: %w", err)
	}
	return markets, mMarket, nil
}
