package alt

import (
	"database/sql"
	"fmt"
	"time"

	sgo "github.com/gagliardetto/solana-go"
)

// accountUsageEntrySize is the on-wire size of one (pubkey, count) entry
// in a MessageSend::CommonAddressUpdate report: 32-byte pubkey + 4-byte
// little-endian count. Must match catscope-rust-bot's src/message.rs
// (ACCOUNT_USAGE_ENTRY_SIZE).
const accountUsageEntrySize = 36

// AccountUsageEntry is one (account, usage count) pair from a bot's
// MessageSend::CommonAddressUpdate report.
type AccountUsageEntry struct {
	Pubkey sgo.PublicKey
	Count  uint32
}

// ParseAccountUsage decodes a KeyFlagCommonAccountUsage message value:
// [n_entries u16 LE][(pubkey 32B, count u32 LE) x n_entries], exactly what
// MessageOutbound::write's MessageSend::CommonAddressUpdate arm
// (src/message.rs) serializes.
func ParseAccountUsage(value []byte) ([]AccountUsageEntry, error) {
	if len(value) < 2 {
		return nil, fmt.Errorf("alt: account usage value too short: %d < 2", len(value))
	}
	n := int(value[0]) | int(value[1])<<8
	want := 2 + n*accountUsageEntrySize
	if len(value) != want {
		return nil, fmt.Errorf("alt: account usage value size mismatch: got %d bytes, want %d (n=%d)", len(value), want, n)
	}
	entries := make([]AccountUsageEntry, n)
	for i := 0; i < n; i++ {
		off := 2 + i*accountUsageEntrySize
		var pk sgo.PublicKey
		copy(pk[:], value[off:(off+32)])
		count := uint32(value[off+32]) | uint32(value[off+33])<<8 | uint32(value[off+34])<<16 | uint32(value[off+35])<<24
		entries[i] = AccountUsageEntry{Pubkey: pk, Count: count}
	}
	return entries, nil
}

// RecordUsage persists entries into account_usage. Each entry's Count is
// the reporting bot's own cumulative-since-process-start tally as of this
// report, not a delta -- so this upserts (overwrites), it does not sum
// across reports, which would double-count every account on every
// subsequent report from the same long-running bot.
func RecordUsage(db *sql.DB, entries []AccountUsageEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("alt: begin: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO account_usage (account_pubkey, count, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(account_pubkey) DO UPDATE SET count = excluded.count, updated_at = excluded.updated_at`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("alt: prepare upsert: %w", err)
	}
	now := time.Now().Unix()
	for _, e := range entries {
		if _, err = stmt.Exec(e.Pubkey[:], e.Count, now); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("alt: upsert usage for %s: %w", e.Pubkey, err)
		}
	}
	if err = stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("alt: close stmt: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("alt: commit: %w", err)
	}
	return nil
}

// TopUsedAccounts returns up to n accounts from account_usage, ranked by
// reported usage count descending (pubkey ascending as a deterministic
// tiebreak) -- the replacement ranking source for cmd/alt.go, in place of
// scanning transaction history via RPC.
func TopUsedAccounts(db *sql.DB, n int) ([]sgo.PublicKey, error) {
	rows, err := db.Query(`SELECT account_pubkey FROM account_usage ORDER BY count DESC, account_pubkey ASC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("alt: query top used accounts: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []sgo.PublicKey
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("alt: scan account_pubkey: %w", err)
		}
		if len(raw) != 32 {
			return nil, fmt.Errorf("alt: account_pubkey blob is not 32 bytes (got %d)", len(raw))
		}
		var pk sgo.PublicKey
		copy(pk[:], raw)
		out = append(out, pk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("alt: row iteration: %w", err)
	}
	return out, nil
}

// Replace persists accounts as tablePubkey's complete, authoritative
// address list, in order -- position i is on-chain lookup index i (see
// schema.sql). Any previous row set for tablePubkey is deleted first, so a
// re-run against a table that has since shrunk doesn't leave stale rows
// at higher positions. Rows for other lookup tables are untouched, so
// multiple tables accumulate across runs.
func Replace(db *sql.DB, tablePubkey sgo.PublicKey, accounts []sgo.PublicKey) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("alt: begin: %w", err)
	}
	if _, err = tx.Exec(`DELETE FROM address_lookup_table WHERE table_pubkey = ?`, tablePubkey[:]); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("alt: delete existing rows for %s: %w", tablePubkey, err)
	}
	stmt, err := tx.Prepare(`INSERT INTO address_lookup_table (table_pubkey, position, account_pubkey) VALUES (?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("alt: prepare insert: %w", err)
	}
	for i, acc := range accounts {
		if _, err = stmt.Exec(tablePubkey[:], i, acc[:]); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("alt: insert %s at position %d: %w", acc, i, err)
		}
	}
	if err = stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("alt: close stmt: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("alt: commit: %w", err)
	}
	return nil
}
