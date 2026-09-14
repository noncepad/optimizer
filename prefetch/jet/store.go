package jet

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
	if err := db.QueryRow(`SELECT COUNT(*) FROM jet_reserve`).Scan(&n); err != nil {
		return 0, fmt.Errorf("jet: count reserves: %w", err)
	}
	return n, nil
}

// insertReserves persists reserves as the source of truth for future runs.
func insertReserves(db *sql.DB, reserves []*Reserve) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("jet: begin: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO jet_reserve
		(pubkey, market, mint, vault)
		VALUES (?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("jet: prepare insert: %w", err)
	}
	for _, r := range reserves {
		if _, err = stmt.Exec(r.Pubkey[:], r.Market[:], r.Mint[:], r.Vault[:]); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("jet: insert reserve %s: %w", r.Pubkey, err)
		}
	}
	if err = stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("jet: close stmt: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("jet: commit: %w", err)
	}
	return nil
}

// loadReserves reconstructs the in-memory reserve list from the database.
func loadReserves(db *sql.DB) ([]*Reserve, map[sgo.PublicKey]int, error) {
	rows, err := db.Query(`SELECT pubkey, market, mint, vault FROM jet_reserve`)
	if err != nil {
		return nil, nil, fmt.Errorf("jet: query reserves: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var reserves []*Reserve
	mReserve := make(map[sgo.PublicKey]int)
	for rows.Next() {
		var pubkey, market, mint, vault []byte
		if err = rows.Scan(&pubkey, &market, &mint, &vault); err != nil {
			return nil, nil, fmt.Errorf("jet: scan reserve: %w", err)
		}
		r := &Reserve{
			Pubkey: sgo.PublicKeyFromBytes(pubkey),
			Market: sgo.PublicKeyFromBytes(market),
			Mint:   sgo.PublicKeyFromBytes(mint),
			Vault:  sgo.PublicKeyFromBytes(vault),
		}
		mReserve[r.Pubkey] = len(reserves)
		reserves = append(reserves, r)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("jet: iterate reserves: %w", err)
	}
	return reserves, mReserve, nil
}
