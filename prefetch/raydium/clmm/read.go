package clmm

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// PoolRow is a flat view of a CLMM pool joined with its fee config. Json
// tags let this struct be exported directly (see export.go) instead of via
// a separate JSON-shaped type.
type PoolRow struct {
	Pubkey        sgo.PublicKey `json:"pubkey"`
	TokenMint0    sgo.PublicKey `json:"token_mint0"`
	TokenMint1    sgo.PublicKey `json:"token_mint1"`
	TokenVault0   sgo.PublicKey `json:"token_vault0"`
	TokenVault1   sgo.PublicKey `json:"token_vault1"`
	Token0Balance uint64        `json:"token0_balance"`
	Token1Balance uint64        `json:"token1_balance"`
	TradeFeeRate  uint32        `json:"trade_fee_rate"`
}

// poolCount returns how many pool rows exist (regardless of whether their
// balances have been fetched yet), so Download can skip re-fetching on
// repeat runs against an already-populated database.
func poolCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM raydium_clmm_pool`).Scan(&n); err != nil {
		return 0, fmt.Errorf("clmm: count pools: %w", err)
	}
	return n, nil
}

const liquidPoolQuery = `
	SELECT p.pubkey, p.token_mint0, p.token_mint1, p.token_vault0, p.token_vault1,
	       p.token0_balance, p.token1_balance, c.trade_fee_rate
	FROM raydium_clmm_pool p
	JOIN raydium_clmm_config c ON c.pubkey = p.amm_config
	WHERE p.token0_balance > 0
	  AND p.token1_balance > 0`

func scanPool(rows *sql.Rows) (*PoolRow, error) {
	r := new(PoolRow)
	var pubkey, mint0, mint1, vault0, vault1 []byte
	var bal0, bal1 int64
	var feeRate int64
	if err := rows.Scan(&pubkey, &mint0, &mint1, &vault0, &vault1, &bal0, &bal1, &feeRate); err != nil {
		return nil, err
	}
	copy(r.Pubkey[:], pubkey)
	copy(r.TokenMint0[:], mint0)
	copy(r.TokenMint1[:], mint1)
	copy(r.TokenVault0[:], vault0)
	copy(r.TokenVault1[:], vault1)
	r.Token0Balance = uint64(bal0)
	r.Token1Balance = uint64(bal1)
	r.TradeFeeRate = uint32(feeRate)
	return r, nil
}

// ReadPools calls fn for each liquid CLMM pool (both vault balances non-zero).
// Iteration stops early if fn returns false.
func ReadPools(db *sql.DB, fn func(*PoolRow) bool) error {
	rows, err := db.Query(liquidPoolQuery)
	if err != nil {
		return fmt.Errorf("clmm ReadPools: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanPool(rows)
		if err != nil {
			return fmt.Errorf("clmm ReadPools scan: %w", err)
		}
		if !fn(r) {
			return nil
		}
	}
	return rows.Err()
}

// TopPools returns the n liquid CLMM pools with the highest liquidity
// (token0_balance + token1_balance, raw token units), sorted descending.
func TopPools(db *sql.DB, n int) ([]*PoolRow, error) {
	rows, err := db.Query(liquidPoolQuery+`
	ORDER BY (token0_balance + token1_balance) DESC
	LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("clmm TopPools: %w", err)
	}
	defer rows.Close()
	out := make([]*PoolRow, 0, n)
	for rows.Next() {
		r, err := scanPool(rows)
		if err != nil {
			return nil, fmt.Errorf("clmm TopPools scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
