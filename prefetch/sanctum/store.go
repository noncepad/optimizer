package sanctum

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// lstCount returns how many LSTs are already persisted, so Create can skip
// re-fetching from the chain on repeat runs against the same database.
func lstCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sanctum_lst`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sanctum: count lsts: %w", err)
	}
	return n, nil
}

// insertLsts persists LSTs as the source of truth for future runs.
func insertLsts(db *sql.DB, lsts []*LstEntry) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("sanctum: begin: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO sanctum_lst
		(mint, sol_value_calculator, sol_value, pool_state, reserve) VALUES (?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sanctum: prepare insert: %w", err)
	}
	for _, e := range lsts {
		var poolState []byte
		if e.PoolState != (sgo.PublicKey{}) {
			poolState = e.PoolState[:]
		}
		if _, err = stmt.Exec(e.Mint[:], e.SolValueCalculator[:], int64(e.SolValue), poolState, int64(e.Reserve)); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("sanctum: insert lst %s: %w", e.Mint, err)
		}
	}
	if err = stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sanctum: close stmt: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("sanctum: commit: %w", err)
	}
	return nil
}

// loadLsts reconstructs the in-memory LST list from the database.
func loadLsts(db *sql.DB) ([]*LstEntry, error) {
	rows, err := db.Query(`SELECT mint, sol_value_calculator, sol_value, pool_state, reserve FROM sanctum_lst`)
	if err != nil {
		return nil, fmt.Errorf("sanctum: query lsts: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var lsts []*LstEntry
	for rows.Next() {
		var mint, calc, poolState []byte
		var solValue, reserve int64
		if err = rows.Scan(&mint, &calc, &solValue, &poolState, &reserve); err != nil {
			return nil, fmt.Errorf("sanctum: scan lst: %w", err)
		}
		e := &LstEntry{
			Mint:               sgo.PublicKeyFromBytes(mint),
			SolValueCalculator: sgo.PublicKeyFromBytes(calc),
			SolValue:           uint64(solValue),
			Reserve:            uint64(reserve),
		}
		if poolState != nil {
			e.PoolState = sgo.PublicKeyFromBytes(poolState)
		}
		lsts = append(lsts, e)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("sanctum: iterate lsts: %w", err)
	}
	return lsts, nil
}
