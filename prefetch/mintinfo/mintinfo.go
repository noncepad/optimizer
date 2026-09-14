// Package mintinfo provides a small shared cache of SPL Mint decimals,
// populated opportunistically by every prefetcher that discovers a mint
// (Orca, Raydium AMM/CPMM/CLMM) so a mint referenced by many pools (USDC,
// wSOL) is only ever fetched once per run, not once per pool.
package mintinfo

import (
	"database/sql"
	"fmt"
	"sync"

	bin "github.com/gagliardetto/binary"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

// MintAccountSize is the fixed on-chain byte size of an SPL Token Mint
// account (mint_authority Option<Pubkey>(36) + supply u64(8) + decimals
// u8(1) + is_initialized bool(1) + freeze_authority Option<Pubkey>(36)).
const MintAccountSize = 82

// Tracker deduplicates mint-decimals discovery across concurrently running
// prefetchers and persists results to the mint_info table.
type Tracker struct {
	db *sql.DB
	mx sync.Mutex
	// true once decimals are actually saved; false while a fetch is
	// in-flight (present but not yet resolved).
	known map[sgo.PublicKey]bool
}

// New loads any already-known mints from db (so a resumed run doesn't
// re-fetch decimals for mints a previous run already discovered) and
// returns a ready-to-use Tracker.
func New(db *sql.DB) (*Tracker, error) {
	t := &Tracker{db: db, known: make(map[sgo.PublicKey]bool)}
	rows, err := db.Query(`SELECT mint FROM mint_info`)
	if err != nil {
		return nil, fmt.Errorf("mintinfo: query existing mints: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var mint []byte
		if err = rows.Scan(&mint); err != nil {
			return nil, fmt.Errorf("mintinfo: scan mint: %w", err)
		}
		t.known[sgo.PublicKeyFromBytes(mint)] = true
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("mintinfo: iterate mints: %w", err)
	}
	return t, nil
}

// Known reports whether mint's decimals are already saved.
func (t *Tracker) Known(mint sgo.PublicKey) bool {
	t.mx.Lock()
	defer t.mx.Unlock()
	return t.known[mint]
}

// MarkPending records that a decimals fetch for mint is about to be issued.
// It returns true only the first time it's called for a given mint (across
// every prefetcher sharing this Tracker), telling the caller to actually
// subscribe; it returns false if the mint is already known or another
// prefetcher already has a fetch in flight for it.
func (t *Tracker) MarkPending(mint sgo.PublicKey) bool {
	t.mx.Lock()
	defer t.mx.Unlock()
	if _, present := t.known[mint]; present {
		return false
	}
	t.known[mint] = false // present but not yet saved: pending
	return true
}

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Save persists mint's decimals using the tracker's own database handle.
// Only safe to call when the caller does NOT already hold an open
// transaction on the same database -- store.DB opens with a single-
// connection pool, so requesting a second connection from a goroutine that
// already holds the only one (via an open *sql.Tx) would deadlock. Callers
// that run inside their own CommitStart/CommitFinish transaction (Raydium
// AMM/CPMM/CLMM) must use SaveTx instead.
func (t *Tracker) Save(mint sgo.PublicKey, decimals uint8) error {
	return t.save(t.db, mint, decimals)
}

// SaveTx persists mint's decimals using the caller's own in-flight
// transaction, avoiding the single-connection deadlock described on Save.
func (t *Tracker) SaveTx(tx *sql.Tx, mint sgo.PublicKey, decimals uint8) error {
	return t.save(tx, mint, decimals)
}

// save is idempotent -- safe to call more than once for the same mint (e.g.
// Raydium CLMM calls this opportunistically for every pool, without ever
// calling MarkPending first, since it already has the decimals for free
// from the pool account itself).
func (t *Tracker) save(ex execer, mint sgo.PublicKey, decimals uint8) error {
	t.mx.Lock()
	alreadySaved := t.known[mint]
	t.known[mint] = true
	t.mx.Unlock()
	if alreadySaved {
		return nil
	}
	if _, err := ex.Exec(
		`INSERT OR REPLACE INTO mint_info (mint, decimals) VALUES (?, ?)`,
		mint[:], int(decimals),
	); err != nil {
		return fmt.Errorf("mintinfo: save %s: %w", mint, err)
	}
	return nil
}

// DecodeDecimals parses an SPL Mint account body and returns its decimals
// field. body must be exactly MintAccountSize bytes.
func DecodeDecimals(body []byte) (uint8, error) {
	var mint sgotkn.Mint
	if err := bin.NewBorshDecoder(body).Decode(&mint); err != nil {
		return 0, fmt.Errorf("mintinfo: decode mint account: %w", err)
	}
	return mint.Decimals, nil
}
