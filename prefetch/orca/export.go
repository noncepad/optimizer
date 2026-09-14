package orca

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	sgo "github.com/gagliardetto/solana-go"
)

// PoolJSON is one orca_whirlpool_pool row, JSON-shaped for
// catscope-rust-bot's build.rs to read instead of opening prefetch.db
// directly. Exported unfiltered (no WHERE clause) -- build.rs's three
// consumers (the router-graph BFS, the ORCA_WHIRLPOOL_POOLS embed
// re-query, and top_pools_data.rs) apply three different filters/orderings
// of their own, and unlike raydium_amm_pool this table has no nullable
// mint/vault columns and a manageable row count (~150K), so there's no
// reason to filter or cap on the Go side -- see amm.embedCap's doc comment
// for the contrasting case where that mattered.
type PoolJSON struct {
	Pubkey        sgo.PublicKey `json:"pubkey"`
	MintA         sgo.PublicKey `json:"mint_a"`
	MintB         sgo.PublicKey `json:"mint_b"`
	VaultA        sgo.PublicKey `json:"vault_a"`
	VaultB        sgo.PublicKey `json:"vault_b"`
	VaultABalance *int64        `json:"vault_a_balance,omitempty"`
	VaultBBalance *int64        `json:"vault_b_balance,omitempty"`
	SqrtPriceLo   int64         `json:"sqrt_price_lo"`
	SqrtPriceHi   int64         `json:"sqrt_price_hi"`
	LiquidityLo   int64         `json:"liquidity_lo"`
	LiquidityHi   int64         `json:"liquidity_hi"`
}

// ExportJSON writes every orca_whirlpool_pool row to path.
func ExportJSON(db *sql.DB, path string) error {
	rows, err := db.Query(`
		SELECT pubkey, mint_a, mint_b, vault_a, vault_b,
		       vault_a_balance, vault_b_balance,
		       sqrt_price_lo, sqrt_price_hi, liquidity_lo, liquidity_hi
		FROM orca_whirlpool_pool`)
	if err != nil {
		return fmt.Errorf("orca: export json: query: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	out := []PoolJSON{}
	for rows.Next() {
		var pubkey, mintA, mintB, vaultA, vaultB []byte
		var vaultABalance, vaultBBalance sql.NullInt64
		var sqrtLo, sqrtHi, liqLo, liqHi int64
		if err := rows.Scan(
			&pubkey, &mintA, &mintB, &vaultA, &vaultB,
			&vaultABalance, &vaultBBalance,
			&sqrtLo, &sqrtHi, &liqLo, &liqHi,
		); err != nil {
			return fmt.Errorf("orca: export json: scan: %w", err)
		}
		p := PoolJSON{
			Pubkey:      sgo.PublicKeyFromBytes(pubkey),
			MintA:       sgo.PublicKeyFromBytes(mintA),
			MintB:       sgo.PublicKeyFromBytes(mintB),
			VaultA:      sgo.PublicKeyFromBytes(vaultA),
			VaultB:      sgo.PublicKeyFromBytes(vaultB),
			SqrtPriceLo: sqrtLo,
			SqrtPriceHi: sqrtHi,
			LiquidityLo: liqLo,
			LiquidityHi: liqHi,
		}
		if vaultABalance.Valid {
			p.VaultABalance = &vaultABalance.Int64
		}
		if vaultBBalance.Valid {
			p.VaultBBalance = &vaultBBalance.Int64
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("orca: export json: iterate: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("orca: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("orca: encode %s: %w", path, err)
	}
	return nil
}
