package alt

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	sgo "github.com/gagliardetto/solana-go"
)

// LookupTableEntry is one address_lookup_table row, JSON-shaped for
// catscope-rust-bot's build.rs to read instead of opening prefetch.db
// directly. No full-table Go reader exists yet (Replace only writes,
// TopUsedAccounts reads the separate account_usage table) -- this is a
// fresh, minimal type for exactly this export.
type LookupTableEntry struct {
	TablePubkey   sgo.PublicKey `json:"table_pubkey"`
	Position      int           `json:"position"`
	AccountPubkey sgo.PublicKey `json:"account_pubkey"`
}

// ExportJSON writes every address_lookup_table row to path as a JSON
// array of LookupTableEntry, ordered by (table_pubkey, position) -- that
// order is load-bearing (see schema.sql's own doc comment: it's the real
// on-chain lookup index), and build.rs's grouping-by-adjacency
// reconstruction depends on rows arriving pre-sorted this way, so this
// export preserves the exact ORDER BY the old direct query used.
func ExportJSON(db *sql.DB, path string) error {
	rows, err := db.Query(`SELECT table_pubkey, position, account_pubkey FROM address_lookup_table ORDER BY table_pubkey, position`)
	if err != nil {
		return fmt.Errorf("alt: export json: query: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	out := []LookupTableEntry{}
	for rows.Next() {
		var tablePubkey, accountPubkey []byte
		var position int
		if err := rows.Scan(&tablePubkey, &position, &accountPubkey); err != nil {
			return fmt.Errorf("alt: export json: scan: %w", err)
		}
		out = append(out, LookupTableEntry{
			TablePubkey:   sgo.PublicKeyFromBytes(tablePubkey),
			Position:      position,
			AccountPubkey: sgo.PublicKeyFromBytes(accountPubkey),
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("alt: export json: iterate: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("alt: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "    ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("alt: encode %s: %w", path, err)
	}
	return nil
}
