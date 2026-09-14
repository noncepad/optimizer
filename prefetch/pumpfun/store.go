package pumpfun

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// curveCount returns how many bonding curves are already persisted, so
// Create can skip re-fetching from the chain on repeat runs against the
// same database.
func curveCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pumpfun_bonding_curve`).Scan(&n); err != nil {
		return 0, fmt.Errorf("pumpfun: count curves: %w", err)
	}
	return n, nil
}

// upsertCurve persists one bonding curve -- called per-account as curves
// are discovered (see event.go), not batched at the end, since pump.fun's
// mint volume can be far larger than what's comfortable to hold in memory
// for a single bulk insert.
func upsertCurve(tx *sql.Tx, e *BondingCurveEntry) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO pumpfun_bonding_curve
		(mint, bonding_curve, creator, quote_mint, virtual_token_reserves,
		 virtual_sol_reserves, real_token_reserves, real_sol_reserves, complete)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		e.Mint[:], e.BondingCurve[:], e.Creator[:], e.QuoteMint[:],
		int64(e.VirtualTokenReserves), int64(e.VirtualSolReserves),
		int64(e.RealTokenReserves), int64(e.RealSolReserves),
		e.Complete,
	)
	if err != nil {
		return fmt.Errorf("pumpfun: upsert curve %s: %w", e.Mint, err)
	}
	return nil
}

// loadCurves reconstructs the in-memory curve list from the database.
func loadCurves(db *sql.DB) ([]*BondingCurveEntry, error) {
	rows, err := db.Query(`SELECT mint, bonding_curve, creator, quote_mint,
		virtual_token_reserves, virtual_sol_reserves, real_token_reserves,
		real_sol_reserves, complete FROM pumpfun_bonding_curve`)
	if err != nil {
		return nil, fmt.Errorf("pumpfun: query curves: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []*BondingCurveEntry
	for rows.Next() {
		var mint, bondingCurve, creator, quoteMint []byte
		e := new(BondingCurveEntry)
		if err = rows.Scan(&mint, &bondingCurve, &creator, &quoteMint,
			&e.VirtualTokenReserves, &e.VirtualSolReserves,
			&e.RealTokenReserves, &e.RealSolReserves, &e.Complete); err != nil {
			return nil, fmt.Errorf("pumpfun: scan curve: %w", err)
		}
		e.Mint = sgo.PublicKeyFromBytes(mint)
		e.BondingCurve = sgo.PublicKeyFromBytes(bondingCurve)
		e.Creator = sgo.PublicKeyFromBytes(creator)
		e.QuoteMint = sgo.PublicKeyFromBytes(quoteMint)
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("pumpfun: iterate curves: %w", err)
	}
	return out, nil
}
