package kamino

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// ExportJSON writes every tracked Kamino reserve to path as a JSON array
// of Reserve values (its own existing json tags) -- the same real query
// loadReserves already runs for in-process use, reused verbatim, so
// catscope-rust-bot's build.rs can read this file instead of opening
// prefetch.db directly.
func ExportJSON(db *sql.DB, path string) error {
	reserves, _, err := loadReserves(db)
	if err != nil {
		return fmt.Errorf("kamino: export json: %w", err)
	}
	if reserves == nil {
		reserves = []*Reserve{}
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("kamino: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "    ")
	if err := enc.Encode(reserves); err != nil {
		return fmt.Errorf("kamino: encode %s: %w", path, err)
	}
	return nil
}
