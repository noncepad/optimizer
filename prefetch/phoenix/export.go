package phoenix

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// ExportJSON writes every tracked Phoenix market to path as a JSON array
// of {market_account, symbol, asset_id} objects (Market's own existing
// json tags, market_account base58-encoded via sgo.PublicKey's
// MarshalJSON) -- the same real query loadMarkets already runs for
// in-process use, reused verbatim, just so catscope-rust-bot's build.rs
// can read it from a file instead of opening prefetch.db directly (see
// that repo's build.rs doc comment on phoenix_market for why).
//
// store.DB always migrates phoenix.Schema before any query runs, so
// (unlike build.rs's own defensive "table not found" fallback, which
// exists for older prefetch.db files that predate this schema) there is
// no "missing table" case to soften here -- an error from loadMarkets
// means something is actually wrong, not just "phoenix hasn't been
// fetched yet". An empty result (table exists, zero rows) still writes a
// valid empty JSON array, not an error.
func ExportJSON(db *sql.DB, path string) error {
	markets, err := loadMarkets(db)
	if err != nil {
		return fmt.Errorf("phoenix: export json: %w", err)
	}
	if markets == nil {
		markets = []*Market{}
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("phoenix: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "    ")
	if err := enc.Encode(markets); err != nil {
		return fmt.Errorf("phoenix: encode %s: %w", path, err)
	}
	return nil
}
