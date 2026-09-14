// Package portfolio persists bot balance snapshots (each tagged with the
// token's USD price at capture time) to a local SQLite database, and
// computes PnL from them.
//
// There is no per-trade ledger available from the bidder daemon -- only
// balance snapshots (see ActionLogBalance in the bidder manager package) --
// so PnL here is defined as a mark-to-market comparison: the market value
// of the portfolio at its earliest known snapshot vs. its latest, not a sum
// of realized trade gains/losses.
package portfolio

import (
	"database/sql"
	"fmt"
	"time"

	sgo "github.com/gagliardetto/solana-go"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS balance_snapshot (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    time      INTEGER NOT NULL, -- unix seconds
    market    BLOB    NOT NULL, -- 32 raw bytes
    pipeline  BLOB    NOT NULL, -- 32 raw bytes
    account   BLOB    NOT NULL, -- 32 raw bytes: the token account
    mint      BLOB    NOT NULL, -- 32 raw bytes
    balance   INTEGER NOT NULL, -- raw token amount (u64, as observed on the bidder log)
    decimals  INTEGER,          -- mint decimals at capture time; NULL if unknown
    usd_price REAL              -- USD price per whole token at capture time; NULL if unavailable
);
CREATE INDEX IF NOT EXISTS idx_balance_snapshot_account_mint_time ON balance_snapshot(account, mint, time);
CREATE INDEX IF NOT EXISTS idx_balance_snapshot_mint_time ON balance_snapshot(mint, time);
`

// DB wraps a SQLite connection holding balance_snapshot history.
type DB struct {
	path string
	db   *sql.DB
}

// Open opens (or creates) the portfolio database at path and applies its schema.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("portfolio: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("portfolio: migrate: %w", err)
	}
	return &DB{path: path, db: db}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) FilePath() string {
	return d.path
}

// DB returns the underlying *sql.DB, for passing to generic SQL tooling
// (e.g. the dashboard's table explorer) that shouldn't need its own
// separately-pooled connection to the same file.
func (d *DB) DB() *sql.DB {
	return d.db
}

// Snapshot is one balance observation, optionally priced.
type Snapshot struct {
	Time     time.Time
	Market   sgo.PublicKey
	Pipeline sgo.PublicKey
	Account  sgo.PublicKey
	Mint     sgo.PublicKey
	Balance  uint64
	Decimals *uint8   // nil if not yet known
	USDPrice *float64 // nil if not yet available
}

// Insert records one balance snapshot.
func (d *DB) Insert(s Snapshot) error {
	_, err := d.db.Exec(
		`INSERT INTO balance_snapshot (time, market, pipeline, account, mint, balance, decimals, usd_price)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.Time.Unix(), s.Market.Bytes(), s.Pipeline.Bytes(), s.Account.Bytes(), s.Mint.Bytes(),
		int64(s.Balance), nullableUint8(s.Decimals), nullableFloat64(s.USDPrice),
	)
	if err != nil {
		return fmt.Errorf("portfolio: insert snapshot: %w", err)
	}
	return nil
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
// within a single account's history. Market/Pipeline identify which bot
// (a bot is exactly one market+pipeline pair) the position's latest
// snapshot was reported under.
type MintPosition struct {
	Mint           sgo.PublicKey
	Market         sgo.PublicKey
	Pipeline       sgo.PublicKey
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

// BotKey identifies one bot -- a bot is exactly one market+pipeline pair,
// which is how the bidder daemon itself tags every balance log entry.
type BotKey struct {
	Market   sgo.PublicKey
	Pipeline sgo.PublicKey
}

// Bots returns every distinct (market, pipeline) pair seen across all
// balance snapshots -- one entry per bot that has ever reported a balance.
func (d *DB) Bots() ([]BotKey, error) {
	rows, err := d.db.Query(`SELECT DISTINCT market, pipeline FROM balance_snapshot ORDER BY market, pipeline`)
	if err != nil {
		return nil, fmt.Errorf("portfolio: list bots: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []BotKey
	for rows.Next() {
		var market, pipeline []byte
		if err := rows.Scan(&market, &pipeline); err != nil {
			return nil, err
		}
		out = append(out, BotKey{Market: sgo.PublicKeyFromBytes(market), Pipeline: sgo.PublicKeyFromBytes(pipeline)})
	}
	return out, rows.Err()
}

// AccountsForBot returns every distinct account that has reported a
// balance under the given bot.
func (d *DB) AccountsForBot(bot BotKey) ([]sgo.PublicKey, error) {
	rows, err := d.db.Query(
		`SELECT DISTINCT account FROM balance_snapshot WHERE market = ? AND pipeline = ? ORDER BY account`,
		bot.Market.Bytes(), bot.Pipeline.Bytes(),
	)
	if err != nil {
		return nil, fmt.Errorf("portfolio: list accounts for bot: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []sgo.PublicKey
	for rows.Next() {
		var a []byte
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, sgo.PublicKeyFromBytes(a))
	}
	return out, rows.Err()
}

// Positions returns one MintPosition per mint ever seen for account,
// comparing that mint's earliest snapshot to its latest.
func (d *DB) Positions(account sgo.PublicKey) ([]MintPosition, error) {
	rows, err := d.db.Query(`SELECT DISTINCT mint FROM balance_snapshot WHERE account = ? ORDER BY mint`, account.Bytes())
	if err != nil {
		return nil, fmt.Errorf("portfolio: list mints: %w", err)
	}
	var mints [][]byte
	for rows.Next() {
		var m []byte
		if err := rows.Scan(&m); err != nil {
			_ = rows.Close()
			return nil, err
		}
		mints = append(mints, m)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	out := make([]MintPosition, 0, len(mints))
	for _, m := range mints {
		start, err := d.snapshotAt(account, m, "ASC")
		if err != nil {
			return nil, err
		}
		latest, err := d.snapshotAt(account, m, "DESC")
		if err != nil {
			return nil, err
		}
		if start == nil || latest == nil {
			continue
		}
		pos := MintPosition{
			Mint:           sgo.PublicKeyFromBytes(m),
			Market:         latest.Market,
			Pipeline:       latest.Pipeline,
			StartTime:      start.Time,
			StartBalance:   start.Balance,
			StartDecimals:  start.Decimals,
			StartUSDPrice:  start.USDPrice,
			LatestTime:     latest.Time,
			LatestBalance:  latest.Balance,
			LatestDecimals: latest.Decimals,
			LatestUSDPrice: latest.USDPrice,
		}
		pos.StartUSDValue = usdValue(start.Balance, start.Decimals, start.USDPrice)
		pos.LatestUSDValue = usdValue(latest.Balance, latest.Decimals, latest.USDPrice)
		if pos.StartUSDValue != nil && pos.LatestUSDValue != nil {
			delta := *pos.LatestUSDValue - *pos.StartUSDValue
			pos.DeltaUSDValue = &delta
		}
		out = append(out, pos)
	}
	return out, nil
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
// snapshot for (account, mint). Rows with a known usd_price are preferred
// over unpriced ones within that end of the timeline -- balance events can
// arrive slightly before the price poller has fetched their mint's first
// price, and without this a position could get permanently stuck showing
// an unknown starting/current value even once pricing catches up.
func (d *DB) snapshotAt(account sgo.PublicKey, mint []byte, order string) (*Snapshot, error) {
	var q string
	switch order {
	case "ASC":
		q = `SELECT time, market, pipeline, balance, decimals, usd_price FROM balance_snapshot
		     WHERE account = ? AND mint = ? ORDER BY (usd_price IS NULL) ASC, time ASC LIMIT 1`
	case "DESC":
		q = `SELECT time, market, pipeline, balance, decimals, usd_price FROM balance_snapshot
		     WHERE account = ? AND mint = ? ORDER BY (usd_price IS NULL) ASC, time DESC LIMIT 1`
	default:
		return nil, fmt.Errorf("portfolio: invalid order %q", order)
	}
	var t int64
	var market, pipeline []byte
	var balance int64
	var decimals sql.NullInt64
	var usdPrice sql.NullFloat64
	err := d.db.QueryRow(q, account.Bytes(), mint).Scan(&t, &market, &pipeline, &balance, &decimals, &usdPrice)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("portfolio: query snapshot: %w", err)
	}
	s := &Snapshot{
		Time:     time.Unix(t, 0),
		Market:   sgo.PublicKeyFromBytes(market),
		Pipeline: sgo.PublicKeyFromBytes(pipeline),
		Account:  account,
		Mint:     sgo.PublicKeyFromBytes(mint),
		Balance:  uint64(balance),
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

// Accounts returns every distinct account that has at least one snapshot.
func (d *DB) Accounts() ([]sgo.PublicKey, error) {
	rows, err := d.db.Query(`SELECT DISTINCT account FROM balance_snapshot ORDER BY account`)
	if err != nil {
		return nil, fmt.Errorf("portfolio: list accounts: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []sgo.PublicKey
	for rows.Next() {
		var a []byte
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, sgo.PublicKeyFromBytes(a))
	}
	return out, rows.Err()
}
