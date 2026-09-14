package bundler

import (
	"database/sql"
	"fmt"
	"time"

	sgo "github.com/gagliardetto/solana-go"
)

// RecordTipUpdate persists update into bundler_tip as the latest known-good
// tip state for update.Bundler -- only called for a real, live update
// (Up == true, both Tip() and Distribution() succeeded), so a later
// LoadTipUpdate always returns the most recent value the network actually
// reported, never a synthetic "down" state.
func RecordTipUpdate(db *sql.DB, update TipUpdate) error {
	addresses := make([]byte, 0, len(update.Addresses)*32)
	for _, a := range update.Addresses {
		addresses = append(addresses, a[:]...)
	}
	_, err := db.Exec(`INSERT INTO bundler_tip (bundler_code, addresses, p25, p50, p75, p95, p99, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bundler_code) DO UPDATE SET
			addresses = excluded.addresses,
			p25 = excluded.p25, p50 = excluded.p50, p75 = excluded.p75,
			p95 = excluded.p95, p99 = excluded.p99,
			updated_at = excluded.updated_at`,
		update.Bundler, addresses,
		update.Distribution[0], update.Distribution[1], update.Distribution[2],
		update.Distribution[3], update.Distribution[4],
		time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("bundler: record tip update for bundler %d: %w", update.Bundler, err)
	}
	return nil
}

// LoadTipUpdate returns the last real tip update RecordTipUpdate persisted
// for bundlerCode, if any -- the fallback RunTipBroadcaster uses whenever
// a live Tip()/Distribution() poll fails (no websocket snapshot yet, or
// the bundler is otherwise unreachable this cycle). ok is false if
// nothing has ever been recorded for this bundler.
func LoadTipUpdate(db *sql.DB, bundlerCode BundlerCode) (TipUpdate, bool, error) {
	row := db.QueryRow(`SELECT addresses, p25, p50, p75, p95, p99 FROM bundler_tip WHERE bundler_code = ?`, bundlerCode)
	var addressesRaw []byte
	var p25, p50, p75, p95, p99 uint64
	err := row.Scan(&addressesRaw, &p25, &p50, &p75, &p95, &p99)
	if err == sql.ErrNoRows {
		return TipUpdate{}, false, nil
	}
	if err != nil {
		return TipUpdate{}, false, fmt.Errorf("bundler: load tip update for bundler %d: %w", bundlerCode, err)
	}
	if len(addressesRaw)%32 != 0 {
		return TipUpdate{}, false, fmt.Errorf("bundler: stored addresses for bundler %d are not a multiple of 32 bytes (%d)", bundlerCode, len(addressesRaw))
	}
	addresses := make([]sgo.PublicKey, 0, len(addressesRaw)/32)
	for i := 0; i < len(addressesRaw); i += 32 {
		var pk sgo.PublicKey
		copy(pk[:], addressesRaw[i:i+32])
		addresses = append(addresses, pk)
	}
	return TipUpdate{
		Bundler:      bundlerCode,
		Up:           true,
		Addresses:    addresses,
		Distribution: [5]uint64{p25, p50, p75, p95, p99},
	}, true, nil
}
