// Package pnl records a trading wallet's token position history into
// prefetch.db and computes mark-to-market PnL from it: the market value
// of a position at its earliest known snapshot vs. its latest, not a sum
// of realized per-trade gains/losses -- see schema.sql's doc comment for
// why. Unlike optimizer/portfolio (which tracks Solpipe bidder/pipeline
// balance events -- infrastructure spend, not trading positions), this
// package is fed directly by whatever calls RecordIfChanged with a real
// wallet balance read (e.g. util.FetchWalletBalance), and only grows the
// table when a position's balance actually moves.
package pnl

import (
	"database/sql"
	"fmt"
	"time"

	sgo "github.com/gagliardetto/solana-go"
)

// Snapshot is one position observation, optionally priced.
type Snapshot struct {
	Time     time.Time
	Wallet   sgo.PublicKey
	Mint     sgo.PublicKey
	Balance  uint64
	Decimals *uint8   // nil if not yet known
	USDPrice *float64 // nil if not yet available
}

// RecordIfChanged inserts a new snapshot for (wallet, mint) only if its
// balance differs from the most recently recorded one (or none exists
// yet) -- the caller doesn't need to track trades or debounce polling
// itself, just call this on whatever cadence it already reads balances
// at. Returns whether a row was actually inserted.
func RecordIfChanged(db *sql.DB, s Snapshot) (bool, error) {
	var lastBalance sql.NullInt64
	err := db.QueryRow(
		`SELECT balance FROM pnl_position_snapshot WHERE wallet = ? AND mint = ? ORDER BY time DESC, id DESC LIMIT 1`,
		s.Wallet.Bytes(), s.Mint.Bytes(),
	).Scan(&lastBalance)
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("pnl: query last balance: %w", err)
	}
	if err == nil && lastBalance.Valid && uint64(lastBalance.Int64) == s.Balance {
		return false, nil
	}
	if _, err := db.Exec(
		`INSERT INTO pnl_position_snapshot (time, wallet, mint, balance, decimals, usd_price)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		s.Time.Unix(), s.Wallet.Bytes(), s.Mint.Bytes(), int64(s.Balance),
		nullableUint8(s.Decimals), nullableFloat64(s.USDPrice),
	); err != nil {
		return false, fmt.Errorf("pnl: insert snapshot: %w", err)
	}
	return true, nil
}

func nullableUint8(v *uint8) interface{} {
	if v == nil {
		return nil
	}
	return int64(*v)
}

func nullableFloat64(v *float64) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

// MintPosition is one mint's starting vs. current mark-to-market value
// within a single wallet's recorded history.
type MintPosition struct {
	Mint           sgo.PublicKey
	StartTime      time.Time
	StartBalance   uint64
	StartDecimals  *uint8
	StartUSDPrice  *float64
	StartUSDValue  *float64 // nil if StartUSDPrice unknown
	LatestTime     time.Time
	LatestBalance  uint64
	LatestDecimals *uint8
	LatestUSDPrice *float64
	LatestUSDValue *float64
	DeltaUSDValue  *float64 // LatestUSDValue - StartUSDValue; nil unless both known
}

// Wallets returns every distinct wallet that has at least one recorded snapshot.
func Wallets(db *sql.DB) ([]sgo.PublicKey, error) {
	rows, err := db.Query(`SELECT DISTINCT wallet FROM pnl_position_snapshot ORDER BY wallet`)
	if err != nil {
		return nil, fmt.Errorf("pnl: list wallets: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []sgo.PublicKey
	for rows.Next() {
		var w []byte
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		out = append(out, sgo.PublicKeyFromBytes(w))
	}
	return out, rows.Err()
}

// Positions returns one MintPosition per mint ever recorded for wallet,
// comparing that mint's earliest snapshot to its latest.
func Positions(db *sql.DB, wallet sgo.PublicKey) ([]MintPosition, error) {
	mints, err := listMints(db, wallet)
	if err != nil {
		return nil, err
	}
	out := make([]MintPosition, 0, len(mints))
	for _, m := range mints {
		start, err := snapshotAt(db, wallet, m, "ASC")
		if err != nil {
			return nil, err
		}
		latest, err := snapshotAt(db, wallet, m, "DESC")
		if err != nil {
			return nil, err
		}
		pos, ok := buildMintPosition(m, start, latest)
		if !ok {
			continue
		}
		out = append(out, pos)
	}
	return out, nil
}

// PositionsBetween returns one MintPosition per mint recorded for wallet,
// comparing each mint's most recent snapshot at or before start against
// its most recent snapshot at or before end -- unlike Positions
// (unconditional earliest vs. latest ever recorded), this lets the
// caller ask for PnL over an arbitrary window. A mint with no snapshot at
// or before start, or none at or before end, is skipped (its value at
// that end of the window is unknown -- e.g. the position didn't exist
// yet, or wasn't recorded until later).
func PositionsBetween(db *sql.DB, wallet sgo.PublicKey, start, end time.Time) ([]MintPosition, error) {
	mints, err := listMints(db, wallet)
	if err != nil {
		return nil, err
	}
	out := make([]MintPosition, 0, len(mints))
	for _, m := range mints {
		startSnap, err := snapshotAtOrBefore(db, wallet, m, start)
		if err != nil {
			return nil, err
		}
		endSnap, err := snapshotAtOrBefore(db, wallet, m, end)
		if err != nil {
			return nil, err
		}
		pos, ok := buildMintPosition(m, startSnap, endSnap)
		if !ok {
			continue
		}
		out = append(out, pos)
	}
	return out, nil
}

// listMints returns every distinct mint ever recorded for wallet.
func listMints(db *sql.DB, wallet sgo.PublicKey) ([][]byte, error) {
	rows, err := db.Query(`SELECT DISTINCT mint FROM pnl_position_snapshot WHERE wallet = ? ORDER BY mint`, wallet.Bytes())
	if err != nil {
		return nil, fmt.Errorf("pnl: list mints: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var mints [][]byte
	for rows.Next() {
		var m []byte
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		mints = append(mints, m)
	}
	return mints, rows.Err()
}

// buildMintPosition assembles a MintPosition from a mint's start/end
// snapshots, computing USD values and their delta. Returns ok=false if
// either snapshot is missing (nothing recorded at that end of the range).
func buildMintPosition(mint []byte, start, end *Snapshot) (MintPosition, bool) {
	if start == nil || end == nil {
		return MintPosition{}, false
	}
	pos := MintPosition{
		Mint:           sgo.PublicKeyFromBytes(mint),
		StartTime:      start.Time,
		StartBalance:   start.Balance,
		StartDecimals:  start.Decimals,
		StartUSDPrice:  start.USDPrice,
		LatestTime:     end.Time,
		LatestBalance:  end.Balance,
		LatestDecimals: end.Decimals,
		LatestUSDPrice: end.USDPrice,
	}
	pos.StartUSDValue = usdValue(start.Balance, start.Decimals, start.USDPrice)
	pos.LatestUSDValue = usdValue(end.Balance, end.Decimals, end.USDPrice)
	if pos.StartUSDValue != nil && pos.LatestUSDValue != nil {
		delta := *pos.LatestUSDValue - *pos.StartUSDValue
		pos.DeltaUSDValue = &delta
	}
	return pos, true
}

func usdValue(balance uint64, decimals *uint8, price *float64) *float64 {
	if decimals == nil || price == nil {
		return nil
	}
	whole := float64(balance) / pow10(*decimals)
	v := whole * *price
	return &v
}

func pow10(n uint8) float64 {
	v := 1.0
	for range n {
		v *= 10
	}
	return v
}

// snapshotAt fetches the earliest (order="ASC") or latest (order="DESC")
// snapshot for (wallet, mint). Rows with a known usd_price are preferred
// over unpriced ones within that end of the timeline -- a position could
// otherwise get permanently stuck showing an unknown starting/current
// value if a balance was recorded slightly before its price became
// available, even once pricing catches up on a later snapshot.
func snapshotAt(db *sql.DB, wallet sgo.PublicKey, mint []byte, order string) (*Snapshot, error) {
	var q string
	switch order {
	case "ASC":
		q = `SELECT time, balance, decimals, usd_price FROM pnl_position_snapshot
		     WHERE wallet = ? AND mint = ? ORDER BY (usd_price IS NULL) ASC, time ASC LIMIT 1`
	case "DESC":
		q = `SELECT time, balance, decimals, usd_price FROM pnl_position_snapshot
		     WHERE wallet = ? AND mint = ? ORDER BY (usd_price IS NULL) ASC, time DESC LIMIT 1`
	default:
		return nil, fmt.Errorf("pnl: invalid order %q", order)
	}
	var t int64
	var balance int64
	var decimals sql.NullInt64
	var usdPrice sql.NullFloat64
	err := db.QueryRow(q, wallet.Bytes(), mint).Scan(&t, &balance, &decimals, &usdPrice)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pnl: query snapshot: %w", err)
	}
	s := &Snapshot{
		Time:    time.Unix(t, 0),
		Wallet:  wallet,
		Mint:    sgo.PublicKeyFromBytes(mint),
		Balance: uint64(balance),
	}
	if decimals.Valid {
		dv := uint8(decimals.Int64)
		s.Decimals = &dv
	}
	if usdPrice.Valid {
		pv := usdPrice.Float64
		s.USDPrice = &pv
	}
	return s, nil
}

// snapshotAtOrBefore fetches the most recent snapshot for (wallet, mint)
// at or before cutoff -- the position's mark-to-market value "as of"
// that time. Same usd_price-preference tie-break as snapshotAt (an
// unpriced row exactly at cutoff shouldn't shadow a priced one just
// before it). Returns nil if the mint has no snapshot at or before cutoff.
func snapshotAtOrBefore(db *sql.DB, wallet sgo.PublicKey, mint []byte, cutoff time.Time) (*Snapshot, error) {
	var t int64
	var balance int64
	var decimals sql.NullInt64
	var usdPrice sql.NullFloat64
	err := db.QueryRow(
		`SELECT time, balance, decimals, usd_price FROM pnl_position_snapshot
		 WHERE wallet = ? AND mint = ? AND time <= ?
		 ORDER BY (usd_price IS NULL) ASC, time DESC LIMIT 1`,
		wallet.Bytes(), mint, cutoff.Unix(),
	).Scan(&t, &balance, &decimals, &usdPrice)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pnl: query snapshot: %w", err)
	}
	s := &Snapshot{
		Time:    time.Unix(t, 0),
		Wallet:  wallet,
		Mint:    sgo.PublicKeyFromBytes(mint),
		Balance: uint64(balance),
	}
	if decimals.Valid {
		dv := uint8(decimals.Int64)
		s.Decimals = &dv
	}
	if usdPrice.Valid {
		pv := usdPrice.Float64
		s.USDPrice = &pv
	}
	return s, nil
}
