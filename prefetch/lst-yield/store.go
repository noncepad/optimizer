// Package lstyield estimates real Solana liquid-staking-token (LST)
// staking yield from a periodic SOL-per-LST exchange-rate timeseries, so
// an LST-collateral leverage-loop strategy (originally built as
// leveragedloopv1's Time-Expanded DAG, since removed) has a real
// profitability signal to work from. The WASM bot itself can't do this: it has no
// persistent storage across restarts and no way to compute a
// rate-of-change on its own. optimizer already reads real on-chain state
// independently and already has prefetch.db, so this is a Go-side-only
// poller (see cmd/watchlstyield.go) -- no new Rust<->Go wire message is
// needed for the sampling side, only the existing inbound message channel
// to hand the finished estimate back to the strategy.
//
// sol_per_lst values are decoded generically via the Sanctum S
// Controller's shared lst_state_list account (sol_value) divided by each
// LST's own pool-reserves ATA balance (reserve) -- see FetchRates in
// fetch.go. This replaced an earlier per-protocol-decoder approach
// (SPL Stake Pool's total_lamports/pool_token_supply, Marinade's own
// msol_price) that only covered 3 hand-picked LSTs; the Sanctum formula
// covers any LST Sanctum tracks with one shared account read plus a
// batched reserve-ATA read, verified live against jitoSOL's known real
// rate (~1.298) during development.
package lstyield

import (
	"database/sql"
	"fmt"
	"time"

	sgo "github.com/gagliardetto/solana-go"
)

// KnownLST identifies one tracked liquid-staking token by a real, verified
// friendly name.
type KnownLST struct {
	Symbol string
	Mint   sgo.PublicKey
}

// KnownLSTs is testperpv1's curated, named set -- the three named in the
// leveraged-yield-farming plan. testperpv1's own LstApy wire message is
// symbol-keyed, so it needs real, confident names, not the full mint-keyed
// TrackedLSTs universe below. Kept deliberately narrow and unchanged by
// the TrackedLSTs expansion -- see testperpv1/instance.go (still uses this).
var KnownLSTs = []KnownLST{
	{
		Symbol: "jitoSOL",
		Mint:   sgo.MustPublicKeyFromBase58("J1toso1uCk3RLmjorhTtrVwY9HJ7X8V9yYac6Y7kGCPn"),
	},
	{
		Symbol: "bSOL",
		Mint:   sgo.MustPublicKeyFromBase58("bSo13r4TkiE4KumL71LsHTPpL2euBYLFx6h9HP3piy1"),
	},
	{
		Symbol: "mSOL",
		Mint:   sgo.MustPublicKeyFromBase58("mSoLzYCxHdYgdzU16g5QSh3i5K3z3KZK7ytfqcJm7So"),
	},
}

// TrackedLST identifies one tracked liquid-staking token by mint --
// Symbol is a real, verified friendly name only for the handful this
// codebase is confident about (vanity mint prefixes that unambiguously
// match well-known real LSTs); every other real candidate deliberately
// has an empty Symbol rather than a guessed one, to avoid mislabeling
// real financial data. Use Label() for a safe display string.
type TrackedLST struct {
	Symbol string
	Mint   sgo.PublicKey
}

// Label returns Symbol if known, otherwise a short, non-misleading
// fallback derived from the mint itself.
func (t TrackedLST) Label() string {
	if t.Symbol != "" {
		return t.Symbol
	}
	s := t.Mint.String()
	if len(s) > 8 {
		s = s[:8]
	}
	return s + "…"
}

// TrackedLSTs is the real candidate universe an LST-collateral
// leverage-loop strategy would evaluate: every Sanctum-tracked LST (sanctum_lst
// table) that also has a real lending-protocol reserve on Kamino,
// Solend, or marginfi's main markets -- 37 of Sanctum's 128 tracked LSTs,
// found by cross-referencing the live prefetch.db (2026-08-29). Unlike
// the old 3-entry KnownLSTs list, membership here doesn't require a
// hand-verified per-protocol decode account -- FetchRates (fetch.go)
// derives every rate generically from Sanctum's own shared
// lst_state_list account, so adding a candidate here is now just adding
// a mint.
var TrackedLSTs = []TrackedLST{
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("BNso1VUJnh4zcfpZa6986Ea66P6TCp59hvtNJ8b1X85")},
	{Symbol: "bonkSOL", Mint: sgo.MustPublicKeyFromBase58("BonK1YhkXEGLZzwtcvRTip3gAL9nCeQD7ppZBLXhtTs")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("Dso1bDeDjCQxTrWHqUUi63oBvV7Mdm6WaobLbQ7gnPQ")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("LAinEtNLgpmCP9Rvsf5Hn8W6EhNiKLZQti1xfWMLy6X")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("LSTxxxnJzKDFSLr4dUkPcmCf5VyryEqzPLz5j4bpxFp")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("LnTRntk2kTfWEY6cVB8K9649pgJbt6dJLS1Ns1GZCWg")},
	{Symbol: "wSOL", Mint: sgo.MustPublicKeyFromBase58("So11111111111111111111111111111111111111112")},
	{Symbol: "bSOL", Mint: sgo.MustPublicKeyFromBase58("bSo13r4TkiE4KumL71LsHTPpL2euBYLFx6h9HP3piy1")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("cPQPBN7WubB3zyQDpzTK2ormx1BMdAym9xkrYUJsctm")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("haSo1Vz5aTsqEnz8nisfnEsipvbAAWpgzRDh2WhhMEh")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("he1iusmfkpAdwvxLNGV8Y1iSbj4rUy6yMhEA3fotn9A")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("hy1oXYgrBW6PVcJ4s6s2FKavRdwgWTXdfE69AxT7kPT")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("jucy5XJ76pHVvtPZb5TKRcGQExkwit2P5s4vY8UzmpC")},
	{Symbol: "jupSOL", Mint: sgo.MustPublicKeyFromBase58("jupSoLaHXQiZZTSfEWMTRRgpnyFm8f6sZdosWBjx93v")},
	{Symbol: "mSOL", Mint: sgo.MustPublicKeyFromBase58("mSoLzYCxHdYgdzU16g5QSh3i5K3z3KZK7ytfqcJm7So")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("pSo1f9nQXWgXibFtKf7NWYxb5enAM4qfP6UJSiXRQfL")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("phaseZSfPxTDBpiVb96H4XFSD8xHeHxZre5HerehBJG")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("picobAEvs6w7QEknPce34wAE4gknZA9v5tTonnmHYdX")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("rkubjTrZYioRSeXwDnhwGQzvW3qkcin72JSxUt3WMVp")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("sctmB7GPi5L2Q5G9tUSzXvhZ4YiDMEGcRov9KfArQpx")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("sctmTAsDn4tLUcemqoqYijfuRkiEfAMPi84PNq2EueR")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("sctmY8fJucsJatwHz6P48RuWBBkdBMNmSMuBYrWFdrw")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("stke7uu3fXHsGqKVVjKnkmj65LRPVrqr4bLG2SJg7rh")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("strng7mqqc1MBJJV6vMzYbEqnwVGvKKGKedeCvtktWA")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("vSoLxydx6akxyMD9XEcPvGYNGq6Nn66oqVb3UkGkei7")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("7Q2afV64in6N6SeZsAAB81TJzwDoD6zpqmHkzi9Dcavn")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("7dHbWXmci3dT8UFYWYZweBLXgycu7Y3iL6trKn1Y7ARj")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("BULKoNSGzxtCqzwTvg5hFJg8fx6dqZRScyXe5LYMfxrn")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("Bybit2vBJGhPF52GBdNaQfUJ6ZpThSgHBobjWZpLPb4B")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("CDCSoLckzozyktpAp9FWT3w92KFJVEUxAU7cNu2Jn3aX")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("CgnTSoL3DgY9SFHxcLj6CgCgKKoTBr6tp4CPAEWy25DE")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("Comp4ssDzXcLeu2MnLuGNNFC4cmLPMng8qWHPvzAMU1h")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("CorvuSSoLxPKLoXWXSfn8pFSMhCRHhe7Uwqe874cmwvg")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("EPCz5LK372vmvCkZH3HgSuGNKACJJwwxsofW6fypCPZL")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("Gekfj7SL2fVpTDxJZmeC46cTYxinjB6gkAnb6EGT6mnn")},
	{Symbol: "", Mint: sgo.MustPublicKeyFromBase58("HUBsveNpjo5pWqNkH57QzxjQASdTVXcSK7bVKTSZtcSX")},
	{Symbol: "jitoSOL", Mint: sgo.MustPublicKeyFromBase58("J1toso1uCk3RLmjorhTtrVwY9HJ7X8V9yYac6Y7kGCPn")},
}

// RecordSnapshot appends one (mint, time, sol_per_lst) row. Unlike
// optimizer/prefetch/pnl's RecordIfChanged, this always inserts -- the
// exchange rate moves in small continuous increments every snapshot, so
// skip-if-unchanged would just throw away the timeseries EstimateAPY
// needs. Callers (cmd/watchlstyield.go) control the insert cadence via
// their own poll interval instead.
func RecordSnapshot(db *sql.DB, mint sgo.PublicKey, t time.Time, solPerLst float64) error {
	if _, err := db.Exec(
		`INSERT INTO lst_yield_snapshot (time, lst_mint, sol_per_lst) VALUES (?, ?, ?)`,
		t.Unix(), mint.Bytes(), solPerLst,
	); err != nil {
		return fmt.Errorf("lstyield: insert snapshot: %w", err)
	}
	return nil
}

// EstimateAPY annualizes the exchange-rate change between the oldest and
// newest snapshot within the last `window` for mint -- the same
// first-vs-latest-snapshot method optimizer/prefetch/pnl.Positions uses
// for mark-to-market PnL, deliberately not a fitted curve or EWMA, for
// the same simplicity-over-precision reason. Returns nil (not zero, not a
// guess) when fewer than two snapshots exist in the window, or when they
// span too little real time to annualize meaningfully (minEstimateSpan)
// -- a freshly-started watch-lst-yield has no history yet and must say
// so, not fabricate a rate.
func EstimateAPY(db *sql.DB, mint sgo.PublicKey, window time.Duration) (*float64, error) {
	since := time.Now().Add(-window).Unix()

	var oldestTime, latestTime int64
	var oldestRate, latestRate float64
	err := db.QueryRow(
		`SELECT time, sol_per_lst FROM lst_yield_snapshot WHERE lst_mint = ? AND time >= ? ORDER BY time ASC LIMIT 1`,
		mint.Bytes(), since,
	).Scan(&oldestTime, &oldestRate)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lstyield: query oldest snapshot: %w", err)
	}
	if err := db.QueryRow(
		`SELECT time, sol_per_lst FROM lst_yield_snapshot WHERE lst_mint = ? AND time >= ? ORDER BY time DESC LIMIT 1`,
		mint.Bytes(), since,
	).Scan(&latestTime, &latestRate); err != nil {
		return nil, fmt.Errorf("lstyield: query latest snapshot: %w", err)
	}

	elapsed := time.Duration(latestTime-oldestTime) * time.Second
	if elapsed < minEstimateSpan || oldestRate <= 0 {
		return nil, nil
	}
	growth := latestRate/oldestRate - 1.0
	annualized := growth * (float64(365*24*time.Hour) / float64(elapsed))
	return &annualized, nil
}

// minEstimateSpan is the shortest real time span EstimateAPY will
// annualize from -- below this, exchange-rate noise (or two polls that
// landed in the same slot) dominates the signal and the annualized
// result would be meaningless, not just imprecise.
//
// **Temporarily lowered 6h -> 1h (2026-08-29, explicit user request)**
// to get an LST-collateral leverage-loop strategy's newly-expanded
// 37-candidate DAG (leveragedloopv1's Time-Expanded DAG, since removed) a
// real (but noisier -- a single ~1h sample of exchange-rate movement, not a
// smooth multi-hour trend) signal well before a real trigger-open trade
// would otherwise wait ~6h for one. This is a real, deliberate signal-quality
// tradeoff on the exact number that decides whether real money opens a
// position -- revert to 6h (or higher) once enough real snapshot history
// has accumulated that the shorter window isn't the only data available.
// Affects testperpv1's own (read-only, no real trigger) LST-loop
// projection logging too, since both share this same `EstimateAPY`.
const minEstimateSpan = 1 * time.Hour
