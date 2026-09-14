package clmm

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// ExportJSON writes every liquid raydium_clmm_pool row (joined with its fee
// config, same filter ReadPools/TopPools already apply) to path, so
// catscope-rust-bot's build.rs can read this file instead of opening
// prefetch.db directly. No cap: a real prefetch.db has ~17,900 rows passing
// this filter, small enough to export in full -- unlike raydium_amm_pool's
// ~602K (see amm.embedCap's doc comment).
func ExportJSON(db *sql.DB, path string) error {
	pools := make([]*PoolRow, 0)
	if err := ReadPools(db, func(p *PoolRow) bool {
		pools = append(pools, p)
		return true
	}); err != nil {
		return fmt.Errorf("clmm: export json: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("clmm: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	if err := enc.Encode(pools); err != nil {
		return fmt.Errorf("clmm: encode %s: %w", path, err)
	}
	return nil
}
