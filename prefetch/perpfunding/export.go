package perpfunding

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// ExportJSON writes every persisted target allocation to path as a JSON
// object of symbol -> allocation_pct -- reusing GetAllTargetAllocations
// (already exported, already returns exactly this shape) verbatim, so
// catscope-rust-bot's build.rs can read this file instead of opening
// prefetch.db directly.
func ExportJSON(db *sql.DB, path string) error {
	allocations, err := GetAllTargetAllocations(db)
	if err != nil {
		return fmt.Errorf("perpfunding: export json: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("perpfunding: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "    ")
	if err := enc.Encode(allocations); err != nil {
		return fmt.Errorf("perpfunding: encode %s: %w", path, err)
	}
	return nil
}
