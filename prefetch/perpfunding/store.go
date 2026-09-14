package perpfunding

import (
	"database/sql"
	"fmt"
	"time"
)

// SetTargetAllocation persists symbol's latest target allocation
// (fraction of total portfolio value, 0.0-1.0), overwriting whatever was
// there before -- this table is a latest-write-wins cache, not a
// history.
func SetTargetAllocation(db *sql.DB, symbol string, allocationPct float64) error {
	_, err := db.Exec(
		`INSERT OR REPLACE INTO perp_funding_target_allocation (symbol, allocation_pct, updated_at_unix) VALUES (?,?,?)`,
		symbol, allocationPct, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("perpfunding: set target allocation %s: %w", symbol, err)
	}
	return nil
}

// GetTargetAllocation reads symbol's persisted target allocation, if one
// has ever been set.
func GetTargetAllocation(db *sql.DB, symbol string) (float64, bool, error) {
	var allocationPct float64
	err := db.QueryRow(`SELECT allocation_pct FROM perp_funding_target_allocation WHERE symbol = ?`, symbol).Scan(&allocationPct)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("perpfunding: get target allocation %s: %w", symbol, err)
	}
	return allocationPct, true, nil
}

// GetAllTargetAllocations reads every persisted target allocation, keyed
// by symbol.
func GetAllTargetAllocations(db *sql.DB) (map[string]float64, error) {
	rows, err := db.Query(`SELECT symbol, allocation_pct FROM perp_funding_target_allocation`)
	if err != nil {
		return nil, fmt.Errorf("perpfunding: query target allocations: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	m := make(map[string]float64)
	for rows.Next() {
		var symbol string
		var allocationPct float64
		if err = rows.Scan(&symbol, &allocationPct); err != nil {
			return nil, fmt.Errorf("perpfunding: scan target allocation: %w", err)
		}
		m[symbol] = allocationPct
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("perpfunding: iterate target allocations: %w", err)
	}
	return m, nil
}
