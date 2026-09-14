package pumpswap

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	sgo "github.com/gagliardetto/solana-go"
)

// PoolJSON is one pumpswap_pool row, JSON-shaped for catscope-rust-bot's
// build.rs to read instead of opening prefetch.db directly. Exported
// unfiltered -- every column here is NOT NULL (base_balance/quote_balance
// default to 0), and the table is small (~4K rows), so there's nothing to
// filter or cap on the Go side; build.rs applies its own
// base_balance/quote_balance > 0 filter for the router graph.
type PoolJSON struct {
	Pool         sgo.PublicKey `json:"pool"`
	BaseMint     sgo.PublicKey `json:"base_mint"`
	QuoteMint    sgo.PublicKey `json:"quote_mint"`
	BaseVault    sgo.PublicKey `json:"base_vault"`
	QuoteVault   sgo.PublicKey `json:"quote_vault"`
	BaseBalance  int64         `json:"base_balance"`
	QuoteBalance int64         `json:"quote_balance"`
}

// readPools returns every pumpswap_pool row. If the table doesn't exist yet
// (an older prefetch.db that predates pumpswap support), returns an empty
// slice instead of an error -- matches build.rs's own existing "table not
// found -> degrade gracefully" convention for this table.
func readPools(db *sql.DB) ([]PoolJSON, error) {
	out := []PoolJSON{}
	rows, err := db.Query(`SELECT pool, base_mint, quote_mint, base_vault, quote_vault, base_balance, quote_balance FROM pumpswap_pool`)
	if err != nil {
		return out, nil
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var pool, baseMint, quoteMint, baseVault, quoteVault []byte
		var baseBalance, quoteBalance int64
		if err := rows.Scan(&pool, &baseMint, &quoteMint, &baseVault, &quoteVault, &baseBalance, &quoteBalance); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, PoolJSON{
			Pool:         sgo.PublicKeyFromBytes(pool),
			BaseMint:     sgo.PublicKeyFromBytes(baseMint),
			QuoteMint:    sgo.PublicKeyFromBytes(quoteMint),
			BaseVault:    sgo.PublicKeyFromBytes(baseVault),
			QuoteVault:   sgo.PublicKeyFromBytes(quoteVault),
			BaseBalance:  baseBalance,
			QuoteBalance: quoteBalance,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate: %w", err)
	}
	return out, nil
}

// ExportJSON writes every pumpswap_pool row to path.
func ExportJSON(db *sql.DB, path string) error {
	out, err := readPools(db)
	if err != nil {
		return fmt.Errorf("pumpswap: export json: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("pumpswap: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("pumpswap: encode %s: %w", path, err)
	}
	return nil
}
