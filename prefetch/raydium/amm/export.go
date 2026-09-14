package amm

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// embedCap is a safety ceiling, not a real ranking cap -- see
// knownDecimalsPoolQuery's own doc comment for why raw/decimals-normalized
// balance is NOT a safe way to rank-and-truncate this table (verified
// against a real prefetch.db: even restricted to pools where both mints
// have a known mint_info decimals row, the real SOL/USDC pool ranked
// ~115,296th out of 183,984 by decimals-normalized balance -- spam mints
// with a genuinely huge minted raw supply, despite a real decimals value,
// still dominate any such ranking). 250_000 sits comfortably above that
// real known-decimals row count (183,984), so in practice this LIMIT does
// not bind at all -- it exists only as a hard ceiling against unbounded
// future growth, not as the mechanism that keeps this export small.
const embedCap = 250_000

// knownDecimalsPoolQuery restricts to pools where BOTH mints have a real
// mint_info decimals row -- unlike embedCap, this filter IS the real
// size-reduction mechanism (602,072 pool rows pass raydium_amm_pool's own
// balance/mint-not-null filter on a real prefetch.db; only 183,984 of those
// also have both mints in mint_info). This also happens to be a
// defensible, non-arbitrary filter on its own terms: a pool whose mint(s)
// have never been decimals-looked-up is almost by definition obscure/never
// traded through any path this codebase already tracks, so excluding it
// costs little real coverage -- unlike raw-balance-based ranking, which
// verifiably discards real, deep pools (SOL/USDC included) in favor of
// spam.
const knownDecimalsPoolQuery = `
	SELECT p.pubkey, p.coin_vault, p.pc_vault, p.coin_mint, p.pc_mint, p.coin_balance, p.pc_balance,
	       p.market_bids, p.market_asks, p.market_event_queue, p.market_coin_vault, p.market_pc_vault, p.market_vault_signer
	FROM raydium_amm_pool p
	JOIN mint_info mc ON mc.mint = p.coin_mint
	JOIN mint_info mp ON mp.mint = p.pc_mint
	WHERE p.coin_balance > 0
	  AND p.pc_balance > 0
	ORDER BY (p.coin_balance + p.pc_balance) DESC
	LIMIT ?`

// ExportJSON writes every raydium_amm_pool row whose mints both have a
// known mint_info decimals row (capped at embedCap, a non-binding safety
// ceiling -- see that constant's and knownDecimalsPoolQuery's own doc
// comments) to path, so catscope-rust-bot's build.rs can read this file
// instead of opening prefetch.db directly. Reuses PoolRow's own json tags
// verbatim (same scanPool shape TopPools/ReadPools use, just a different
// query).
func ExportJSON(db *sql.DB, path string) error {
	rows, err := db.Query(knownDecimalsPoolQuery, embedCap)
	if err != nil {
		return fmt.Errorf("amm: export json: query: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	pools := make([]*PoolRow, 0)
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return fmt.Errorf("amm: export json: scan: %w", err)
		}
		pools = append(pools, p)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("amm: export json: iterate: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("amm: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	if err := enc.Encode(pools); err != nil {
		return fmt.Errorf("amm: encode %s: %w", path, err)
	}
	return nil
}
