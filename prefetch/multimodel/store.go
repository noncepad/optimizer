// Package multimodel persists multimodelv1's cross-restart residual/
// z-score warm-up snapshot into prefetch.db, one row per mint -- see
// schema.sql's doc comment for why one row/message per mint, not one big
// batch. This package never decodes the *residual* data inside a
// payload; it only reads the fixed-offset mint field needed to key each
// row, via mintFromPayload -- see that function's doc comment for the
// exact wire layout it depends on (must stay in sync with
// catscope-rust-bot's src/trader/residual_snapshot.rs).
package multimodel

import (
	"database/sql"
	"fmt"
	"time"
)

// residualSnapshotMintOffset/residualSnapshotHeaderSize describe just
// enough of trader::residual_snapshot::encode's real wire layout
// (`[saved_at_secs i64][n_entries u16][mint [u8;32]]...`) to extract the
// mint from a real single-entry payload without a full parse -- this
// package deliberately stays ignorant of everything past the mint (the
// samples/last_price_usd bytes), which is Rust's concern alone.
const (
	residualSnapshotHeaderSize = 8 + 2 // saved_at_secs (i64) + n_entries (u16)
	residualSnapshotMintSize   = 32
)

// mintFromPayload extracts the mint from a real, single-entry residual
// snapshot payload (n_entries must be exactly 1 -- this package only
// ever receives single-mint payloads, see send_residual_snapshot's doc
// comment on the Rust side). ok is false for anything that doesn't match
// that exact shape -- never guesses or extracts from a malformed/
// multi-entry payload.
func mintFromPayload(payload []byte) (mint [32]byte, ok bool) {
	if len(payload) < residualSnapshotHeaderSize+residualSnapshotMintSize {
		return mint, false
	}
	nEntries := uint16(payload[8]) | uint16(payload[9])<<8
	if nEntries != 1 {
		return mint, false
	}
	copy(mint[:], payload[residualSnapshotHeaderSize:residualSnapshotHeaderSize+residualSnapshotMintSize])
	return mint, true
}

// SaveResidualSnapshot upserts payload's real residual data as its own
// mint's row -- overwrites that mint's previous entry, never accumulates
// history (only the latest per mint is ever useful; the Rust side itself
// discards anything too old on replay, trader::residual_snapshot::
// is_fresh). A payload that doesn't look like a real single-mint
// snapshot is rejected, not silently dropped -- callers should surface
// this as a real error, not treat it the same as "no data yet."
func SaveResidualSnapshot(db *sql.DB, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	mint, ok := mintFromPayload(payload)
	if !ok {
		return fmt.Errorf("multimodel: save residual snapshot: payload doesn't look like a real single-mint snapshot (%d bytes)", len(payload))
	}
	_, err := db.Exec(
		`INSERT INTO multimodel_residual_snapshot (mint, payload, updated_at_unix) VALUES (?, ?, ?)
		 ON CONFLICT(mint) DO UPDATE SET payload = excluded.payload, updated_at_unix = excluded.updated_at_unix`,
		mint[:], payload, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("multimodel: save residual snapshot: %w", err)
	}
	return nil
}

// LoadResidualSnapshots returns every persisted mint's own payload, or
// (nil, nil) if nothing has ever been saved. Each returned payload is
// exactly what a prior process's SaveResidualSnapshot call received for
// that mint -- ready to send back out as-is, one per
// KeyFlagReplayResidualSnapshot message (see cmd/multimodel.go's
// boot-time push -- one message per mint, same reasoning as the send
// side, so no single reply message can ever approach the real transport
// size cap regardless of how many mints are persisted).
func LoadResidualSnapshots(db *sql.DB) ([][]byte, error) {
	rows, err := db.Query(`SELECT payload FROM multimodel_residual_snapshot`)
	if err != nil {
		return nil, fmt.Errorf("multimodel: load residual snapshots: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out [][]byte
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("multimodel: load residual snapshots: scan: %w", err)
		}
		out = append(out, payload)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("multimodel: load residual snapshots: row iteration: %w", err)
	}
	return out, nil
}
