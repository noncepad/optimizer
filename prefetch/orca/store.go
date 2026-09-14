package orca

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// poolCount returns how many pools are already persisted, so Create can skip
// re-fetching from the chain on repeat runs against the same database.
func poolCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orca_whirlpool_pool`).Scan(&n); err != nil {
		return 0, fmt.Errorf("orca: count pools: %w", err)
	}
	return n, nil
}

const poolColumns = `pubkey, whirlpools_config, mint_a, mint_b, vault_a, vault_b,
		sqrt_price_lo, sqrt_price_hi, liquidity_lo, liquidity_hi,
		tick_current_index, tick_spacing, fee_rate`

// scanPool scans one row in the column order of poolColumns.
func scanPool(rows *sql.Rows) (*Whirlpool, error) {
	var pubkey, cfg, mintA, mintB, vaultA, vaultB []byte
	var sqrtLo, sqrtHi, liqLo, liqHi int64
	p := new(Whirlpool)
	if err := rows.Scan(&pubkey, &cfg, &mintA, &mintB, &vaultA, &vaultB,
		&sqrtLo, &sqrtHi, &liqLo, &liqHi,
		&p.TickCurrentIndex, &p.TickSpacing, &p.FeeRate); err != nil {
		return nil, fmt.Errorf("orca: scan pool: %w", err)
	}
	p.Pubkey = sgo.PublicKeyFromBytes(pubkey)
	p.WhirlpoolsConfig = sgo.PublicKeyFromBytes(cfg)
	p.TokenMintA = sgo.PublicKeyFromBytes(mintA)
	p.TokenMintB = sgo.PublicKeyFromBytes(mintB)
	p.VaultA = sgo.PublicKeyFromBytes(vaultA)
	p.VaultB = sgo.PublicKeyFromBytes(vaultB)
	p.SqrtPriceLo = uint64(sqrtLo)
	p.SqrtPriceHi = uint64(sqrtHi)
	p.LiquidityLo = uint64(liqLo)
	p.LiquidityHi = uint64(liqHi)
	return p, nil
}

// loadPools reconstructs the in-memory pool list from the database.
func loadPools(db *sql.DB) ([]*Whirlpool, map[sgo.PublicKey]int, error) {
	rows, err := db.Query(`SELECT ` + poolColumns + ` FROM orca_whirlpool_pool`)
	if err != nil {
		return nil, nil, fmt.Errorf("orca: query pools: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var pools []*Whirlpool
	mPool := make(map[sgo.PublicKey]int)
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, nil, err
		}
		mPool[p.Pubkey] = len(pools)
		pools = append(pools, p)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("orca: iterate pools: %w", err)
	}
	return pools, mPool, nil
}
