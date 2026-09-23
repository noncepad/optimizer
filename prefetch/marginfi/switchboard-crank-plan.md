# Plan: crank marginfi's Switchboard On-Demand oracle from `optimizer`

Status: not started. Written up for a later session to pick up.

## Problem

marginfi's real SOL bank uses a Switchboard **On-Demand** (pull) oracle
feed. marginfi rejects a borrow if that feed's `last_update_timestamp` is
older than the bank's `oracle_max_age` (70s for this bank). Nothing in
this system currently refreshes ("cranks") that feed — it only gets
updated when some unrelated external party happens to request a fresh
price for their own reasons. Live-observed this session: the real feed
sat **45+ minutes stale** with zero external cranks in that entire
window.

Net effect: `catscope-rust-bot/src/brain/testperpv1/state.rs`'s `[15/16]
skipping marginfi SOL borrow-hedge` phase is permanently skipped in the
test bot (deliberately, see that function's doc comment), and — more
importantly — `perpfundingv1`'s real `open_marginfi_borrow_leg` basis-trade
logic can silently fail to open a SOL borrow-hedge at exactly the moment
the strategy wants to, for the same reason. That's the actual motivation
for this work: not the test, the live trading path.

## Why this belongs in `optimizer` (Go), not the WASM bot

Checked `catscope-rust-bot/wit/component.wit`: the WASM guest's only host
interfaces are `transactionprocessor` (build/sign/send Solana txs),
`shooter` (account streaming), and `general` (stdin/stdout to the host).
**No networking capability at all.** Switchboard On-Demand's crank isn't
a plain on-chain instruction you can construct from on-chain data alone —
it requires an off-chain round trip to Switchboard's oracle
network/Crossbar gateway to get a freshly *signed* price attestation,
then a transaction carrying that signed payload. That means:

- Doing this from inside the WASM sandbox would require extending the WIT
  contract itself *and* whatever host runtime implements it (a different
  repo from `catscope-rust-bot` — wherever the wasmtime host process
  lives) — a much bigger architectural change than this is worth.
- `optimizer` already has full network access and already plays the
  "orchestration/infra" role relative to the WASM guest everywhere else
  in this codebase (allocation, bot image upload, wallet funding — see
  `brain/testperpv1/eval.go` and `brain/perpfundingv1/eval.go`'s boot
  transfer). Cranking the oracle fits that same pattern.

## What's already known (reverse-engineered this session, Rust side)

`catscope-rust-bot/src/trader/dex/pyth.rs`:

- Switchboard On-Demand program: `SBondMDrcV3K4kxZR1HNVT7osZxAHVHgYXL5Ze1oMUv`
- The specific feed marginfi's real SOL bank reads:
  `4Hmd6PdjVA9auCoScE12iaBogfwS4ZXQ6VZoBeqanwWW`
- `PullFeedAccountData` layout (Anchor, discriminator
  `[196,27,108,196,10,215,219,40]`): price at byte offset 56 (i128, scale
  1e18), `last_update_timestamp` at byte offset 2216 (i64, unix seconds).
  Both offsets were found empirically (no on-chain program source was
  available -- only Switchboard's TS SDK, which decodes via Anchor/Borsh
  rather than exposing fixed offsets), then cross-validated two
  independent ways. See that file's doc comments for the full derivation
  if the offsets ever need re-verifying against a different account/feed.
- marginfi's staleness check itself (`SwitchboardStalePrice`):
  `current_timestamp.saturating_sub(last_updated) > bank.oracle_max_age`
  — confirmed against marginfi-v2's real `programs/marginfi/src/state/price.rs`.

`optimizer/prefetch/marginfi/bank.go` already parses `OracleSetup`
(enum ordinal, Switchboard is one of several possible values --
Pyth/Fixed/others also occur across real banks) and `OracleKey` from real
Bank accounts, but does **not** yet parse `oracle_max_age` -- that'll
likely be needed here too, to decide whether a crank is actually
necessary before spending the tx fee.

## Open questions to resolve before writing code

1. **Does a usable Go SDK/client exist for Switchboard On-Demand's crank
   flow**, or does this need to be built by calling their Crossbar
   gateway's HTTP API directly? Check `switchboard-xyz`'s repos for a Go
   package before assuming TS/Rust-only.
2. **What does the Crossbar gateway actually require** — is it a public,
   unauthenticated HTTP endpoint, or does it need an API key/account?
   What's the actual on-chain tx cost (base fee only, or a fee paid to
   oracle operators too)? See prior conversation turn for the "push vs.
   pull fee model" distinction -- needs confirming against the real
   Switchboard docs/SDK, not assumed.
3. **Is this feed actually under-cranked in normal trading conditions**,
   or was the 45-minute staleness window observed this session an
   artifact of low real-world usage on this specific bank/feed? Worth
   checking the feed's real historical update cadence (e.g. via
   `getSignaturesForAddress` on `4Hmd6PdjVA9auCoScE12iaBogfwS4ZXQ6VZoBeqanwWW`
   over a longer window) before building anything -- if it's naturally
   fresh most of the time in real trading conditions, a full cranker may
   be unnecessary engineering for a rare edge case.

## Proposed shape (pending the above)

Two designs, not yet decided between:

- **Reactive**: right before `perpfundingv1` attempts to open a marginfi
  SOL borrow-hedge, check the feed's on-chain staleness; if stale, crank
  it (one-off), then proceed. Minimal ongoing cost, but adds latency
  (and a failure mode) to the borrow path itself.
- **Periodic background cranker**: a small loop in `optimizer` (or a
  standalone process) that keeps this specific feed fresh on some cadence
  regardless of whether a borrow is imminent. Removes latency from the
  borrow path, costs a small amount of SOL continuously whether or not
  it's ever used.

Recommend resolving open question 3 first -- it directly determines
whether either of these is worth building at all.

## Where this would likely live

- Feed-staleness reads/parsing: probably `optimizer/prefetch/marginfi/`
  (this directory), alongside `bank.go`'s existing oracle-field parsing.
- The actual crank-submission logic: wherever `optimizer` already builds
  and sends transactions for wallet-funding-style one-off actions (see
  `brain/perpfundingv1/eval.go`'s boot transfer for the existing
  pattern: `hs.builder.Helper(ctx)` + `helper.FinishTx()`).
