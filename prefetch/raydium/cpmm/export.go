package cpmm

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// ExportJSON writes every liquid raydium_cpmm_pool row (joined with its fee
// config, same filter ReadPools/TopPools already apply) to path, so
// catscope-rust-bot's build.rs can read this file instead of opening
// prefetch.db directly. No cap: a real prefetch.db has ~1,200 rows passing
// this filter.
func ExportJSON(db *sql.DB, path string) error {
	pools := make([]*PoolRow, 0)
	if err := ReadPools(db, func(p *PoolRow) bool {
		pools = append(pools, p)
		return true
	}); err != nil {
		return fmt.Errorf("cpmm: export json: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("cpmm: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	if err := enc.Encode(pools); err != nil {
		return fmt.Errorf("cpmm: encode %s: %w", path, err)
	}
	return nil
}
