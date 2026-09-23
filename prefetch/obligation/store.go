// Package obligation records this bot's own Solend/Kamino lending
// obligation state (deposits/borrows, across multimodelv1's pair/
// directional/hawkes trade types) into prefetch.db, the same
// skip-if-unchanged shape optimizer/prefetch/pnl uses for wallet token
// balances -- see this package's schema.sql for the real motivation
// (there was previously no way to see this state without manually
// deriving each obligation's address and parsing its raw account bytes).
package obligation

import (
	"database/sql"
	"fmt"
	"time"

	sgo "github.com/gagliardetto/solana-go"
)

// Snapshot is one (protocol, trade_type, reserve, kind) observation.
type Snapshot struct {
	Time       time.Time
	Wallet     sgo.PublicKey
	Protocol   Protocol
	TradeType  TradeType
	ObligationID uint8
	Kind       string // "deposit" or "borrow"
	Reserve    sgo.PublicKey
	Amount     uint64
}

// RecordIfChanged inserts a new snapshot for (wallet, protocol,
// trade_type, reserve, kind) only if its amount differs from the most
// recently recorded one (or none exists yet). Returns whether a row was
// actually inserted.
func RecordIfChanged(db *sql.DB, s Snapshot) (bool, error) {
	var lastAmount sql.NullInt64
	err := db.QueryRow(
		`SELECT amount FROM obligation_position_snapshot
		 WHERE wallet = ? AND protocol = ? AND trade_type = ? AND reserve = ? AND kind = ?
		 ORDER BY time DESC, id DESC LIMIT 1`,
		s.Wallet.Bytes(), string(s.Protocol), string(s.TradeType), s.Reserve.Bytes(), s.Kind,
	).Scan(&lastAmount)
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("obligation: query last amount: %w", err)
	}
	if err == nil && lastAmount.Valid && uint64(lastAmount.Int64) == s.Amount {
		return false, nil
	}
	if _, err := db.Exec(
		`INSERT INTO obligation_position_snapshot
		 (time, wallet, protocol, trade_type, obligation_id, kind, reserve, amount)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.Time.Unix(), s.Wallet.Bytes(), string(s.Protocol), string(s.TradeType), int64(s.ObligationID),
		s.Kind, s.Reserve.Bytes(), int64(s.Amount),
	); err != nil {
		return false, fmt.Errorf("obligation: insert snapshot: %w", err)
	}
	return true, nil
}

// CurrentEntry is one (protocol, trade_type, reserve, kind)'s most
// recently recorded amount -- the "as of now" obligation state.
type CurrentEntry struct {
	Protocol  Protocol
	TradeType TradeType
	Kind      string
	Reserve   sgo.PublicKey
	Amount    uint64
	Time      time.Time
}

// Current returns wallet's most recent recorded amount for every
// (protocol, trade_type, reserve, kind) combination ever seen -- a flat
// "current obligation state" snapshot, not a timeseries. A reserve whose
// most recent recorded amount is 0 (fully withdrawn/repaid) is still
// included; callers that only want real open positions should filter
// Amount > 0 themselves.
func Current(db *sql.DB, wallet sgo.PublicKey) ([]CurrentEntry, error) {
	rows, err := db.Query(
		`SELECT protocol, trade_type, kind, reserve, amount, time FROM obligation_position_snapshot o
		 WHERE wallet = ? AND id = (
		     SELECT id FROM obligation_position_snapshot
		     WHERE wallet = o.wallet AND protocol = o.protocol AND trade_type = o.trade_type
		       AND reserve = o.reserve AND kind = o.kind
		     ORDER BY time DESC, id DESC LIMIT 1
		 )
		 ORDER BY trade_type, protocol, kind, reserve`,
		wallet.Bytes(),
	)
	if err != nil {
		return nil, fmt.Errorf("obligation: query current: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []CurrentEntry
	for rows.Next() {
		var protocol, tradeType, kind string
		var reserve []byte
		var amount, t int64
		if err := rows.Scan(&protocol, &tradeType, &kind, &reserve, &amount, &t); err != nil {
			return nil, err
		}
		out = append(out, CurrentEntry{
			Protocol:  Protocol(protocol),
			TradeType: TradeType(tradeType),
			Kind:      kind,
			Reserve:   sgo.PublicKeyFromBytes(reserve),
			Amount:    uint64(amount),
			Time:      time.Unix(t, 0),
		})
	}
	return out, rows.Err()
}
