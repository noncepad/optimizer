package kamino

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// reserveCount returns how many reserves are already persisted, so Create
// can skip re-fetching from the chain on repeat runs against the same
// database.
func reserveCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM kamino_reserve`).Scan(&n); err != nil {
		return 0, fmt.Errorf("kamino: count reserves: %w", err)
	}
	return n, nil
}

// insertReserves persists reserves as the source of truth for future runs.
func insertReserves(db *sql.DB, reserves []*Reserve) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("kamino: begin: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO kamino_reserve
		(pubkey, lending_market, mint, supply_vault, fee_vault)
		VALUES (?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("kamino: prepare insert: %w", err)
	}
	for _, r := range reserves {
		if _, err = stmt.Exec(r.Pubkey[:], r.LendingMarket[:], r.Mint[:], r.SupplyVault[:], r.FeeVault[:]); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("kamino: insert reserve %s: %w", r.Pubkey, err)
		}
	}
	if err = stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("kamino: close stmt: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("kamino: commit: %w", err)
	}
	return nil
}

// loadReserves reconstructs the in-memory reserve list from the database.
func loadReserves(db *sql.DB) ([]*Reserve, map[sgo.PublicKey]int, error) {
	rows, err := db.Query(`SELECT pubkey, lending_market, mint, supply_vault, fee_vault FROM kamino_reserve`)
	if err != nil {
		return nil, nil, fmt.Errorf("kamino: query reserves: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var reserves []*Reserve
	mReserve := make(map[sgo.PublicKey]int)
	for rows.Next() {
		var pubkey, lendingMarket, mint, supplyVault, feeVault []byte
		if err = rows.Scan(&pubkey, &lendingMarket, &mint, &supplyVault, &feeVault); err != nil {
			return nil, nil, fmt.Errorf("kamino: scan reserve: %w", err)
		}
		r := &Reserve{
			Pubkey:        sgo.PublicKeyFromBytes(pubkey),
			LendingMarket: sgo.PublicKeyFromBytes(lendingMarket),
			Mint:          sgo.PublicKeyFromBytes(mint),
			SupplyVault:   sgo.PublicKeyFromBytes(supplyVault),
			FeeVault:      sgo.PublicKeyFromBytes(feeVault),
		}
		mReserve[r.Pubkey] = len(reserves)
		reserves = append(reserves, r)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("kamino: iterate reserves: %w", err)
	}
	return reserves, mReserve, nil
}
