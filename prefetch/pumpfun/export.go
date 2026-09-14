package pumpfun

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	sgo "github.com/gagliardetto/solana-go"
)

// BondingCurveJSON is one pumpfun_bonding_curve row, JSON-shaped for
// catscope-rust-bot's build.rs to read instead of opening prefetch.db
// directly. Exported unfiltered (every column is NOT NULL, table is tiny --
// ~400 rows on a real prefetch.db); build.rs applies its own
// complete/quote_mint/real_sol_reserves filter for the router graph.
type BondingCurveJSON struct {
	Mint            sgo.PublicKey `json:"mint"`
	QuoteMint       sgo.PublicKey `json:"quote_mint"`
	RealSolReserves int64         `json:"real_sol_reserves"`
	Complete        bool          `json:"complete"`
}

// ExportJSON writes every pumpfun_bonding_curve row to path. If the table
// doesn't exist yet (an older prefetch.db that predates pumpfun support),
// writes an empty JSON array instead of failing -- matches build.rs's own
// existing "table not found -> degrade gracefully" convention for this
// table.
func ExportJSON(db *sql.DB, path string) error {
	out := []BondingCurveJSON{}
	rows, err := db.Query(`SELECT mint, quote_mint, real_sol_reserves, complete FROM pumpfun_bonding_curve`)
	if err == nil {
		defer func() {
			_ = rows.Close()
		}()
		for rows.Next() {
			var mint, quoteMint []byte
			var realSolReserves int64
			var complete int64
			if err := rows.Scan(&mint, &quoteMint, &realSolReserves, &complete); err != nil {
				return fmt.Errorf("pumpfun: export json: scan: %w", err)
			}
			out = append(out, BondingCurveJSON{
				Mint:            sgo.PublicKeyFromBytes(mint),
				QuoteMint:       sgo.PublicKeyFromBytes(quoteMint),
				RealSolReserves: realSolReserves,
				Complete:        complete != 0,
			})
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("pumpfun: export json: iterate: %w", err)
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("pumpfun: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("pumpfun: encode %s: %w", path, err)
	}
	return nil
}
