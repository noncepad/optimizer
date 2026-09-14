package cpmm

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// PoolRow is a flat view of a CPMM pool joined with its fee config. Json
// tags let this struct be exported directly (see export.go) instead of via
// a separate JSON-shaped type.
type PoolRow struct {
	Pubkey        sgo.PublicKey `json:"pubkey"`
	AmmConfig     sgo.PublicKey `json:"amm_config"`
	Token0Mint    sgo.PublicKey `json:"token0_mint"`
	Token1Mint    sgo.PublicKey `json:"token1_mint"`
	Token0Vault   sgo.PublicKey `json:"token0_vault"`
	Token1Vault   sgo.PublicKey `json:"token1_vault"`
	LpSupply      uint64        `json:"lp_supply"`
	Token0Balance uint64        `json:"token0_balance"`
	Token1Balance uint64        `json:"token1_balance"`
	TradeFeeRate  uint64        `json:"trade_fee_rate"`
}

// poolCount returns how many pool rows exist (regardless of whether their
// balances have been fetched yet), so Download can skip re-fetching on
// repeat runs against an already-populated database.
func poolCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM raydium_cpmm_pool`).Scan(&n); err != nil {
		return 0, fmt.Errorf("cpmm: count pools: %w", err)
	}
	return n, nil
}

const liquidPoolQuery = `
	SELECT p.pubkey, p.amm_config,
	       p.token0_mint, p.token1_mint,
	       p.token0_vault, p.token1_vault,
	       p.lp_supply, p.token0_balance, p.token1_balance,
	       c.trade_fee_rate
	FROM raydium_cpmm_pool p
	JOIN raydium_cpmm_config c ON c.pubkey = p.amm_config
	WHERE p.lp_supply > 0
	  AND p.token0_balance > 0
	  AND p.token1_balance > 0`

func scanPool(rows *sql.Rows) (*PoolRow, error) {
	r := new(PoolRow)
	var pubkey, ammConfig, mint0, mint1, vault0, vault1 []byte
	var lpSupply, bal0, bal1, feeRate int64
	if err := rows.Scan(
		&pubkey, &ammConfig,
		&mint0, &mint1,
		&vault0, &vault1,
		&lpSupply, &bal0, &bal1,
		&feeRate,
	); err != nil {
		return nil, err
	}
	copy(r.Pubkey[:], pubkey)
	copy(r.AmmConfig[:], ammConfig)
	copy(r.Token0Mint[:], mint0)
	copy(r.Token1Mint[:], mint1)
	copy(r.Token0Vault[:], vault0)
	copy(r.Token1Vault[:], vault1)
	r.LpSupply = uint64(lpSupply)
	r.Token0Balance = uint64(bal0)
	r.Token1Balance = uint64(bal1)
	r.TradeFeeRate = uint64(feeRate)
	return r, nil
}

// ReadPools calls fn for each liquid CPMM pool (lp_supply > 0, both vault
// balances non-zero). Iteration stops early if fn returns false.
func ReadPools(db *sql.DB, fn func(*PoolRow) bool) error {
	rows, err := db.Query(liquidPoolQuery)
	if err != nil {
		return fmt.Errorf("cpmm ReadPools: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanPool(rows)
		if err != nil {
			return fmt.Errorf("cpmm ReadPools scan: %w", err)
		}
		if !fn(r) {
			return nil
		}
	}
	return rows.Err()
}

// TopPools returns the n liquid CPMM pools with the highest liquidity
// (token0_balance + token1_balance, raw token units), sorted descending.
func TopPools(db *sql.DB, n int) ([]*PoolRow, error) {
	rows, err := db.Query(liquidPoolQuery+`
	ORDER BY (p.token0_balance + p.token1_balance) DESC
	LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("cpmm TopPools: %w", err)
	}
	defer rows.Close()
	out := make([]*PoolRow, 0, n)
	for rows.Next() {
		r, err := scanPool(rows)
		if err != nil {
			return nil, fmt.Errorf("cpmm TopPools scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReadPoolsByMints calls fn for each liquid CPMM pool matching the given token
// pair in either mint order. Iteration stops early if fn returns false.
func ReadPoolsByMints(db *sql.DB, mintA, mintB sgo.PublicKey, fn func(*PoolRow) bool) error {
	rows, err := db.Query(
		liquidPoolQuery+`
	  AND ((p.token0_mint = ? AND p.token1_mint = ?)
	    OR (p.token0_mint = ? AND p.token1_mint = ?))`,
		mintA[:], mintB[:], mintB[:], mintA[:],
	)
	if err != nil {
		return fmt.Errorf("cpmm ReadPoolsByMints: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanPool(rows)
		if err != nil {
			return fmt.Errorf("cpmm ReadPoolsByMints scan: %w", err)
		}
		if !fn(r) {
			return nil
		}
	}
	return rows.Err()
}
