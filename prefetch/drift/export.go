package drift

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// ExportJSON writes every tracked Drift spot market to path as a JSON
// array of SpotMarket values (its own existing json tags) -- the same
// real query loadSpotMarkets already runs for in-process use, reused
// verbatim, so catscope-rust-bot's build.rs can read this file instead of
// opening prefetch.db directly.
func ExportJSON(db *sql.DB, path string) error {
	markets, _, err := loadSpotMarkets(db)
	if err != nil {
		return fmt.Errorf("drift: export json: %w", err)
	}
	if markets == nil {
		markets = []*SpotMarket{}
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("drift: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "    ")
	if err := enc.Encode(markets); err != nil {
		return fmt.Errorf("drift: encode %s: %w", path, err)
	}
	return nil
}
