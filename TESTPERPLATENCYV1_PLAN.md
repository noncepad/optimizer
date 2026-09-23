# testperplatencyv1 — Go-side wiring + real-run notes

Status: Go-side wiring **confirmed built and registered**. A real,
end-to-end run has been done successfully for `testperpv1` (the *original*
smoke test) — real transactions, real confirmations, one real gap found
(see below). `testperplatencyv1` itself (the new 100x-cycle mode this doc
is nominally about) has **not yet been run for real** — that's still the
actual next step.

## Why this exists

`catscope-rust-bot`'s `src/brain/testperplatencyv1` (a copy of `testperpv1`)
runs a real-transaction latency test: for one protocol (Solend/Kamino/
Marginfi), it repeats a deposit→withdraw cycle up to 100 times, timing
send→low-latency-read latency each way, and reports p50/p99 once done. See
that Rust module's own doc comments (`mod.rs`, `state.rs`) for the full
design, including the still-unverified "leave 1 raw unit of dust so the
Solend/Kamino obligation never fully closes" assumption — **still
unverified**, not exercised by the real run described below (that run used
plain `testperpv1`, which doesn't have the cycling/dust logic at all).

`optimizer` (this repo, Go) is what actually builds the WASM, uploads it to
a validator via the Solpipe marketplace, and drives the handshake.
Originally it only knew about `testperpv1`; this doc covers the mirror-copy
that added `testperplatencyv1` support.

## Go-side wiring — DONE, confirmed working

1. **`brain/testperplatencyv1/`** (new package, mirrors `brain/testperpv1/`
   file-for-file: `testperplatencyv1.go`, `init.go`, `eval.go`,
   `instance.go`, `message.go` — deliberately did not copy
   `subscribe_diag_test.go`, an 865-line regression test for shared-library
   bugs already fixed upstream, not specific to this mode).
   - `Configuration` gained a `TargetProtocol string` field.
   - `init.go` sets `mEnv["MODE"] = "testperplatencyv1"` and, when
     `TargetProtocol` is non-empty, `mEnv["TEST_PROTOCOL"] = ...` —
     confirmed (by tracing `mgrbot.Load`/`Image.inputMEnv`) that this map
     becomes the WASM instance's default *runtime* env, same mechanism
     `MODE` already relies on.
2. **`cmd/testperplatency.go`** — mirrors `cmd/testperp.go`.
   `TestPerpLatencyCmd` adds a `--protocol` flag → `Configuration.TargetProtocol`.
   Bumped the run window from `testperp`'s 3 minutes to 60 minutes (100
   cycles will take far longer than testperpv1's ~16-action pass; 60 min
   is still just a guess, no real timing data exists for this mode yet).
3. **`cmd/main.go`** — registered. **Real invoked command name is
   `testperp-latency`** (with a hyphen) — despite the struct tag literally
   saying `cmd:"testperplatency"` (no hyphen). Traced why: kong in this
   codebase kebab-cases the Go *field* name (`TestperpLatency` →
   `testperp-latency`) for every multi-word command here regardless of the
   tag string's own spelling — same pattern already true for
   `DownloadArb`'s tag saying `cmd:"arb"` yet actually registering as
   `download-arb`. The tag value is cosmetically wrong but harmless; fix it
   to `cmd:"testperp-latency"` at some point for readability, not required.

**Confirmed via `go build ./... && ./optimizer --help`**: both `testperp`
and `testperp-latency` show up correctly, `testperp --help` and (by the
same mechanism) `testperp-latency --help` display their flags as expected.
`go build ./...` itself completes with no errors.

## Real run completed: `testperpv1` (not testperplatencyv1) — mainnet, real funds

Ran via `REPO=<catscope-rust-bot checkout> optimizer testperp <fee-payer-keyfile>`
(no `--state-url` needed — see below). Wallet: parent `c-wallet-5.json`
(`BTcfUPtC7sA9Nd7y9QNXvQ1BiUWpM3C9k733v7zRxRiP`), started with 0.172393042
SOL.

**Open questions from the old version of this doc, now resolved:**
- **No pipeline bot / own validator needed.** Upload targets
  `common.SampleBotPipeline()` — "the free Catscope non-voting validator,"
  a shared pipeline Catscope already runs. First upload attempt hit
  `"pipeline missing"` (transient), but the built-in 30s retry loop
  succeeded cleanly on the very next attempt. Confirmed twice (two separate
  real runs).
- **No `--state-url` needed.** Omitting it makes the code fall back to
  `dialer.State()` (the bidder daemon's own connection) — confirmed
  working in practice, not just in theory.
- **Bidder daemon requirement confirmed real**, and was already running
  for this user this session (`solpipe bidder proxy proxy.json
  --fee-payer=authorizer.json`, cwd `~/work/mainnet/catscope-run/proxy` —
  looks like real, intended infrastructure, not leftover).

**What actually happened, in order:**
1. WASM built successfully (`cargo build --target wasm32-wasip2 --release`,
   driven by `optimizer`).
2. Handshake succeeded (after one transient upload retry).
3. Go-side boot transfer moved 92,393,042 lamports (~0.0924 SOL) from
   parent to the derived child wallet (`3hqu56Yw1aL4MKdYER8hGnJmZ4Q9EfctvCCB9mWYPP6X`,
   `common.DeriveChildKeyFromIndex(parentKey, 1)`) — real, confirmed tx.
4. `[1/16]` swap: wrapped 0.05 SOL, swapped to USDC — real, confirmed tx.
5. `[2/16]`–`[4/16]` Solend: bootstrap, $1 deposit, withdraw — all real,
   all confirmed on-chain, fast (~0.5s deposit→confirm).
6. `[5/16]` Kamino bootstrap **failed repeatedly**, real on-chain error:
   ```
   Instruction: InitObligation
   Transfer: insufficient lamports 16162252, need 24165120
   custom program error: 0x1
   ```
   Root cause, precisely: **Kamino's bootstrap batches two account
   creations into one atomic transaction** — `InitUserMetadata` (~0.0081
   SOL rent) *and* `InitObligation` (~0.0242 SOL rent) — needing **~0.0323
   SOL total** in one shot. The whole tx reverts if either piece fails
   (Solana transactions are atomic), so `InitUserMetadata`'s rent payment
   was silently rolled back every retry, masking the real remaining-balance
   picture in the post-tx balance diff (`Account 0 balance:` only showed
   the ~5,800 lamport fee lost, not the ~8M lamports InitUserMetadata
   briefly spent before rollback).
   
   This is a **real funding-sizing gap**, not a code bug in Solend/Kamino
   instruction logic — the child wallet's ~0.092 SOL boot-transfer amount
   isn't generous enough to survive Solend's own bootstrap costs *and*
   still have ~0.0323 SOL free for Kamino's, in one run.
7. Run stopped manually at that point (would otherwise retry forever,
   burning a small fee each attempt with zero chance of success).

**USDC is not something you need to pre-fund** — `[1/16]` converts SOL to
USDC itself; it only skips that step if the wallet already holds ≥$2 USDC.

**Funding guidance for next real run** (measured, not guessed): parent
needs roughly **0.25–0.3 SOL** to clear all three protocols' bootstrap
costs in one run without running dry:
- 0.05 SOL — wrap+swap to USDC (becomes USDC value, not lost)
- ~0.018 SOL — Solend obligation rent + ATA rents + swap/deposit/withdraw fees
- ~0.032 SOL — Kamino bootstrap (`InitUserMetadata` + `InitObligation`, one atomic tx)
- Marginfi's bootstrap cost — **still unmeasured**, never reached yet
- Plus per-tx fees throughout, and the fixed 0.08 SOL the code always
  leaves untouched in the parent

## Reusable tooling built along the way

**`contrib/derive-child-key/`** (new, in this repo) — recomputes the
deterministic child wallet key from a parent keyfile + index (always `1`
for testperpv1/testperplatencyv1/perpfundingv1's trading child key), so
leftover child funds can be swept back after a run. There is **no
automatic sweep-back anywhere in the bot code** — checked, doesn't exist.
Usage:
```bash
go run ./contrib/derive-child-key <parent-keyfile> 1 /tmp/child.json
solana balance /tmp/child.json
solana transfer <destination-pubkey> ALL --from /tmp/child.json --fee-payer /tmp/child.json
rm /tmp/child.json   # real private key material, delete when done
```
If the child has 0 SOL (e.g. already swept) but still holds USDC/other SPL
tokens, use `spl-token transfer <mint> ALL <destination> --owner /tmp/child.json --fee-payer <a funded keyfile>` —
note the split `--owner`/`--fee-payer`, since the child can't pay its own
tx fee once it's out of SOL.

Demonstrated both sweeps for real this session: SOL (0.024195252 SOL back
to parent) and USDC (4.789816 USDC back to parent).

## NOT done yet — pick up here

- [ ] **Actually run `testperp-latency` for real.** Everything above
      validated the Go build, the upload/handshake path, and the
      shared-pipeline/no-state-url/bidder-daemon assumptions — but all of
      that was via plain `testperp`, not `testperp-latency`. The new
      cycling/dust logic itself remains completely unexercised against
      real chain state.
- [ ] Fund a wallet with ~0.25–0.3 SOL (more if starting with Marginfi
      specifically doesn't need Kamino's extra buffer, less might do —
      Marginfi's own bootstrap cost is still unmeasured).
- [ ] Recommended first invocation:
      ```bash
      REPO=/home/naomi/work/catscope-rust-bot \
        optimizer testperp-latency <fee-payer-keyfile> --protocol=marginfi
      ```
      (no `--state-url` needed). Marginfi first, per earlier discussion —
      its cycle logic needs no dust-remainder workaround, so it validates
      the harness/instrumentation itself before spending on Solend/Kamino
      where that assumption is still unverified.
- [ ] Cosmetic: fix `cmd/main.go`'s `cmd:"testperplatency"` tag to say
      `cmd:"testperp-latency"` to match what it actually registers as.
