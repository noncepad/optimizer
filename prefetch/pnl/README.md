# `pnl` — trading wallet mark-to-market PnL

## What this is (and isn't)

This package tracks profit/loss for the bot's **trading wallet** (the
child key every bot mode derives via
`common.DeriveChildKeyFromIndex(parentKey, 1)` — the key that actually
signs deposits/borrows/swaps, not the parent fee-payer). It answers "is
this wallet worth more or less USD than it used to be?"

It is **not**:

- `optimizer/portfolio` — that package tracks Solpipe bidder/pipeline
  balance events (`ActionLogBalance` from the bidder daemon), which is
  about the cost of running on validator pipelines/infrastructure, not
  the trading wallet's own positions. Different data source, different
  question, unrelated to this package.
- A trade ledger. There is no table of individual buys/sells/deposits/
  borrows here, and no attempt to compute *realized* gain per trade.
  Nothing here decodes transactions.

## The method: mark-to-market, not trade accounting

PnL is defined as: **(current USD value of a position) − (USD value of
that same position the first time it was ever recorded)**, per mint,
summed across every mint the wallet holds. That's it. No trade history
is consulted or needed.

This only works because the underlying table (`pnl_position_snapshot`)
stores periodic *balance* observations, not trade events:

```sql
CREATE TABLE pnl_position_snapshot (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    time      INTEGER NOT NULL, -- unix seconds
    wallet    BLOB    NOT NULL, -- 32 raw bytes
    mint      BLOB    NOT NULL, -- 32 raw bytes
    balance   INTEGER NOT NULL, -- raw token amount (u64, NOT decimal-adjusted)
    decimals  INTEGER,          -- mint decimals at capture time; NULL if unknown
    usd_price REAL              -- USD price per whole token at capture time; NULL if unavailable
);
```

Each row is one snapshot: "at time T, wallet W held `balance` raw units
of `mint`, worth `usd_price` per whole token." Native SOL is recorded
under the wrapped-SOL mint address
(`So11111111111111111111111111111111111111112`) so it sits in the same
table as every SPL token, no special-casing needed downstream.

### Why "no trade tracking" is a deliberate choice, not a gap

A trade-by-trade ledger would need to decode every transaction the
wallet signs (swaps, deposits, borrows, repays, across every protocol
this bot touches) and correctly attribute realized gains/losses,
including fees, slippage, and interest accrual, per protocol. That's a
lot of protocol-specific parsing for a number ("PnL") that a simple
before/after balance comparison already answers correctly, as long as
you sample balances often enough that you don't care about the path
between two snapshots — only the start and the end.

The tradeoff: this can't tell you *why* PnL moved (which trade caused
it), only *that* it moved and by how much. If you need per-trade
attribution later, that's a different, separate feature — this package
was intentionally kept to "position value over time," per an explicit
choice not to also track trades.

## How a row gets written: `RecordIfChanged`

```go
func RecordIfChanged(db *sql.DB, s Snapshot) (bool, error)
```

This is the **only** writer. It looks up the most recent snapshot for
`(s.Wallet, s.Mint)` and skips the insert if the balance is identical to
last time — so calling it on every poll tick, regardless of how often
that is, only actually grows the table when a position's size really
changed (a deposit, withdrawal, swap, trade, interest accrual large
enough to move the raw balance, etc.). This is what "records position
changes" means in practice: the *caller* doesn't need to know or care
whether anything happened between two polls, `RecordIfChanged` figures
that out from the data itself.

## How it gets fed: `optimizer watch-pnl`

`cmd/watchpnl.go` (in `optimizer/cmd`) is the feeder. On a timer
(`--poll`, default 30s) it:

1. Reads the trading (child) wallet's live SOL + every SPL token balance
   via `util.FetchWalletBalance` (a direct on-chain read through the
   Solpipe graph subscription, not an event stream).
2. Looks up each mint's current USD price via a Jupiter price poller
   (same mechanism `watch-balances` uses, duplicated rather than shared
   since the two commands are otherwise unrelated).
3. Calls `RecordIfChanged` once per mint (including SOL).

Run it as: `optimizer watch-pnl <fee-payer-keypair-path> --jupiter-key <key>`
(or set `JUPITER_API_KEY`, including via `.env`). It needs the same
local Solpipe bidder-proxy socket every other real bot command in this
repo depends on (`~/.solpipe.bidder.proxy.sock`) — if that's not
running, every poll fails with a dial error and nothing gets recorded,
but the command keeps retrying rather than exiting.

Nothing else in this codebase calls `RecordIfChanged` yet — `watch-pnl`
is currently the only feeder. Anything else that wants to contribute
snapshots (a different wallet, a different cadence, a different data
source) can call `pnl.RecordIfChanged` directly; the table doesn't care
who wrote a row, only `(wallet, mint, time)`.

## How to read it back: `Positions` / `Wallets`

```go
func Wallets(db *sql.DB) ([]sgo.PublicKey, error)
func Positions(db *sql.DB, wallet sgo.PublicKey) ([]MintPosition, error)
```

`Positions` returns one `MintPosition` per mint the wallet has ever had a
snapshot for, each with `StartBalance`/`StartUSDValue` (earliest
snapshot) and `LatestBalance`/`LatestUSDValue` (most recent), plus
`DeltaUSDValue = LatestUSDValue - StartUSDValue`. `DeltaUSDValue` is
`nil` unless *both* ends have a known price — a balance recorded before
the price poller ever priced that mint won't silently show as a $0
start (which would fabricate a fake gain), it shows as "unknown" and
stays that way until a priced snapshot exists.

To get a wallet's total PnL: sum `DeltaUSDValue` across every
`MintPosition` where it's non-nil (mirroring exactly what
`optimizer/cmd/pnl.go` already does for the unrelated `portfolio`
package — same aggregation shape, different data source).

## Worked example

```
t0: wallet holds 2.0 SOL @ $150/SOL  -> $300
t1: wallet holds 2.0 SOL @ $180/SOL  -> RecordIfChanged is a no-op (balance unchanged, 2.0 SOL both times)
t2: wallet swaps into 1.0 SOL + 50 USDC @ $180/SOL, $1/USDC -> new row for SOL (balance changed: 2.0 -> 1.0), new row for USDC (first time seen)
t3: wallet holds 1.0 SOL @ $190/SOL, 50 USDC @ $1/USDC -> no balance change either mint, no rows

Positions(wallet):
  SOL:  start=2.0 SOL @ $150 = $300   latest=1.0 SOL @ $190 = $190   delta = -$110
  USDC: start=50 USDC @ $1 = $50      latest=50 USDC @ $1 = $50      delta = $0

Total PnL = -$110 + $0 = -$110
```

Note this is mark-to-market on the *first-ever* and *most-recent*
snapshot specifically — the swap at t2 (SOL going down, USDC appearing)
isn't itself "seen" as an event by anything reading `Positions`; it's
just the reason the SOL balance's start and end differ. That's the
whole method, and the whole reason a trade ledger was skipped.
