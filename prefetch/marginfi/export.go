package marginfi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// ExportJSON writes every tracked marginfi bank to path as a JSON array
// of Bank values (its own existing json tags) -- the same real query
// loadBanks already runs for in-process use, reused verbatim, so
// catscope-rust-bot's build.rs can read this file instead of opening
// prefetch.db directly. build.rs's old 3-tier fallback for pre-oracle-
// column schemas has no equivalent here -- store.DB always migrates the
// current schema, so every exported row always has oracle_setup/
// oracle_key populated.
func ExportJSON(db *sql.DB, path string) error {
	banks, _, err := loadBanks(db)
	if err != nil {
		return fmt.Errorf("marginfi: export json: %w", err)
	}
	if banks == nil {
		banks = []*Bank{}
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("marginfi: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "    ")
	if err := enc.Encode(banks); err != nil {
		return fmt.Errorf("marginfi: encode %s: %w", path, err)
	}
	return nil
}
