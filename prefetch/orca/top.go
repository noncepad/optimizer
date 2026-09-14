package orca

import (
	"database/sql"
	"fmt"
)

// TopPools returns the n Whirlpool pools with the highest active liquidity
// (the Uniswap-v3-style u128 L, not a raw token-amount sum — Orca pools
// don't have vault balances in this schema), sorted descending.
//
// liquidity_lo/liquidity_hi are stored as bit-reinterpreted int64s of a u128
// split in two, so a plain "ORDER BY liquidity_hi DESC, liquidity_lo DESC"
// would sort wrong wherever the top bit is set (a huge unsigned value would
// look negative and sort as small). "(x < 0) DESC, x DESC" fixes that: it
// puts values whose sign bit is set (true unsigned magnitude >= 2^63) ahead
// of those without, and within each group signed-descending order already
// matches unsigned-descending order.
func TopPools(db *sql.DB, n int) ([]*Whirlpool, error) {
	rows, err := db.Query(`
		SELECT `+poolColumns+`
		FROM orca_whirlpool_pool
		ORDER BY (liquidity_hi < 0) DESC, liquidity_hi DESC,
		         (liquidity_lo < 0) DESC, liquidity_lo DESC
		LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("orca TopPools: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	out := make([]*Whirlpool, 0, n)
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, fmt.Errorf("orca TopPools: %w", err)
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("orca TopPools: %w", err)
	}
	return out, nil
}
