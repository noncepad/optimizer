package mintinfo

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	sgo "github.com/gagliardetto/solana-go"
)

// MintDecimals is one mint_info row, JSON-shaped for catscope-rust-bot's
// build.rs to read instead of opening prefetch.db directly. mint_info has
// no dedicated Go struct today (Tracker only tracks which mints are
// known, not their decimals, in memory) -- this is a fresh, minimal type
// for exactly this export.
type MintDecimals struct {
	Mint     sgo.PublicKey `json:"mint"`
	Decimals uint8         `json:"decimals"`
}

// ExportJSON writes every mint_info row to path as a JSON array of
// MintDecimals -- the same table build.rs currently reads three separate
// times (a soft-degrade decimals lookup for the router graph, a hard-
// panic lookup for curated symbol mints, and a soft-skip lookup for the
// trade-universe filter); this is one full snapshot all three can build
// their own in-memory map from.
func ExportJSON(db *sql.DB, path string) error {
	rows, err := db.Query(`SELECT mint, decimals FROM mint_info`)
	if err != nil {
		return fmt.Errorf("mintinfo: export json: query: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	out := []MintDecimals{}
	for rows.Next() {
		var mint []byte
		var decimals int
		if err := rows.Scan(&mint, &decimals); err != nil {
			return fmt.Errorf("mintinfo: export json: scan: %w", err)
		}
		out = append(out, MintDecimals{Mint: sgo.PublicKeyFromBytes(mint), Decimals: uint8(decimals)})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("mintinfo: export json: iterate: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("mintinfo: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("mintinfo: encode %s: %w", path, err)
	}
	return nil
}
