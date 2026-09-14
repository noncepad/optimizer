package amm

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// PoolRow is a flat view of an AMM v4 pool. CoinMint/PcMint (and thus a spot
// price of PcBalance/CoinBalance raw-unit tokens) are only populated once
// both vault accounts have been fetched — see liquidPoolQuery. The Market*
// fields (OpenBook market account children, populated once the pool's
// `market` account has been fetched) are nullable independently of
// CoinMint/PcMint -- catscope-rust-bot's build.rs needs them for its
// RAYDIUM_AMM_POOLS embed, which only requires MarketVaultSigner to be set,
// not CoinMint/PcMint. Json tags let this struct be exported directly (see
// export.go) instead of via a separate JSON-shaped type.
type PoolRow struct {
	Pubkey            sgo.PublicKey  `json:"pubkey"`
	CoinVault         sgo.PublicKey  `json:"coin_vault"`
	PcVault           sgo.PublicKey  `json:"pc_vault"`
	CoinMint          sgo.PublicKey  `json:"coin_mint"`
	PcMint            sgo.PublicKey  `json:"pc_mint"`
	CoinBalance       uint64         `json:"coin_balance"`
	PcBalance         uint64         `json:"pc_balance"`
	MarketBids        *sgo.PublicKey `json:"market_bids,omitempty"`
	MarketAsks        *sgo.PublicKey `json:"market_asks,omitempty"`
	MarketEventQueue  *sgo.PublicKey `json:"market_event_queue,omitempty"`
	MarketCoinVault   *sgo.PublicKey `json:"market_coin_vault,omitempty"`
	MarketPcVault     *sgo.PublicKey `json:"market_pc_vault,omitempty"`
	MarketVaultSigner *sgo.PublicKey `json:"market_vault_signer,omitempty"`
}

// poolCount returns how many pool rows exist (regardless of whether their
// balances/mints have been fetched yet), so Download can skip re-fetching
// on repeat runs against an already-populated database.
func poolCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM raydium_amm_pool`).Scan(&n); err != nil {
		return 0, fmt.Errorf("amm: count pools: %w", err)
	}
	return n, nil
}

const liquidPoolQuery = `
	SELECT pubkey, coin_vault, pc_vault, coin_mint, pc_mint, coin_balance, pc_balance,
	       market_bids, market_asks, market_event_queue, market_coin_vault, market_pc_vault, market_vault_signer
	FROM raydium_amm_pool
	WHERE coin_balance > 0
	  AND pc_balance > 0
	  AND coin_mint IS NOT NULL
	  AND pc_mint IS NOT NULL`

// nullablePubkey converts a possibly-nil BLOB scan target into a *sgo.PublicKey
// -- database/sql leaves the destination nil (rather than a 32-byte slice of
// zeros) when the source column is SQL NULL, so nil-check is sufficient.
func nullablePubkey(b []byte) *sgo.PublicKey {
	if b == nil {
		return nil
	}
	pk := sgo.PublicKeyFromBytes(b)
	return &pk
}

func scanPool(rows *sql.Rows) (*PoolRow, error) {
	r := new(PoolRow)
	var pubkey, coinVault, pcVault, coinMint, pcMint []byte
	var coinBal, pcBal int64
	var marketBids, marketAsks, marketEventQueue, marketCoinVault, marketPcVault, marketVaultSigner []byte
	if err := rows.Scan(
		&pubkey, &coinVault, &pcVault, &coinMint, &pcMint, &coinBal, &pcBal,
		&marketBids, &marketAsks, &marketEventQueue, &marketCoinVault, &marketPcVault, &marketVaultSigner,
	); err != nil {
		return nil, err
	}
	copy(r.Pubkey[:], pubkey)
	copy(r.CoinVault[:], coinVault)
	copy(r.PcVault[:], pcVault)
	copy(r.CoinMint[:], coinMint)
	copy(r.PcMint[:], pcMint)
	r.CoinBalance = uint64(coinBal)
	r.PcBalance = uint64(pcBal)
	r.MarketBids = nullablePubkey(marketBids)
	r.MarketAsks = nullablePubkey(marketAsks)
	r.MarketEventQueue = nullablePubkey(marketEventQueue)
	r.MarketCoinVault = nullablePubkey(marketCoinVault)
	r.MarketPcVault = nullablePubkey(marketPcVault)
	r.MarketVaultSigner = nullablePubkey(marketVaultSigner)
	return r, nil
}

// ReadPools calls fn for each liquid AMM pool (both vault balances non-zero).
// Iteration stops early if fn returns false.
func ReadPools(db *sql.DB, fn func(*PoolRow) bool) error {
	rows, err := db.Query(liquidPoolQuery)
	if err != nil {
		return fmt.Errorf("amm ReadPools: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanPool(rows)
		if err != nil {
			return fmt.Errorf("amm ReadPools scan: %w", err)
		}
		if !fn(r) {
			return nil
		}
	}
	return rows.Err()
}

// TopPools returns the n liquid AMM pools with the highest liquidity
// (coin_balance + pc_balance, raw token units), sorted descending.
func TopPools(db *sql.DB, n int) ([]*PoolRow, error) {
	rows, err := db.Query(liquidPoolQuery+`
	ORDER BY (coin_balance + pc_balance) DESC
	LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("amm TopPools: %w", err)
	}
	defer rows.Close()
	out := make([]*PoolRow, 0, n)
	for rows.Next() {
		r, err := scanPool(rows)
		if err != nil {
			return nil, fmt.Errorf("amm TopPools scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
