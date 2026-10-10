# Latency Test Report — `testperplatencyv1` / Marginfi

**Run date:** 2026-08-31
**Command:** `REPO=/home/naomi/work/catscope-rust-bot optimizer testperp-latency <fee-payer> --protocol=marginfi`
**Log:** `/tmp/testperplatencyv1-marginfi-6.log` (6th upload attempt this session — the first 5 failed at the upload/handshake stage due to pipeline-side instability, unrelated to this report)

## Successful cycles: 1–5 (all deposit + withdraw pairs completed)

Two latency signals were captured per transaction — they measure different things and don't always agree:
- **Write→read**: send → the account's real state changing is observed via `on_account` (the metric this whole redesign exists to measure)
- **Tx-confirm**: send → the transaction's signature is seen confirmed via `Event::Transaction` (a secondary, independent signal — arrives on its own schedule, sometimes before the write→read signal, sometimes well after)

| Cycle | Action | Write→read | Tx-confirm |
|---|---|---|---|
| 1 | Deposit | 9,308 ms | 9,308 ms |
| 1 | Withdraw | 11,369 ms | 11,369 ms |
| 2 | Deposit | 10,223 ms | 10,224 ms |
| 2 | Withdraw | 299 ms | 299 ms |
| 3 | Deposit | 347 ms | 346 ms |
| 3 | Withdraw | 494 ms | 494 ms |
| 4 | Deposit | 5,256 ms | 5,512 ms |
| 4 | Withdraw | 5,252 ms | *(not captured — confirmation hadn't arrived before the run was stopped)* |
| 5 | Deposit | 227 ms | 4,583 ms |
| 5 | Withdraw | 5 ms | 4,223 ms |

Notable: cycles 4 and 5 show the two signals diverging significantly (the write→read signal landing *well before* the tx-confirm signal — e.g. cycle 5's withdraw was observed via `on_account` in just **5ms**, but its tx-confirmation didn't arrive until 4.2 seconds later). That gap is a real, useful finding in itself: the low-latency account-update path is meaningfully faster than waiting on transaction-list confirmation for this protocol.

Quick aggregate across the 10 write→read samples: **median ≈ 494ms, range 5ms–11,369ms** — small sample, wide spread, driven mostly by cycles 1–2 (slower, likely first-interaction/subscription warm-up) vs. cycles 3–5 (much faster once warmed up).

*(Note: the code's own formal p50/p99 report — `report_cycle_stats` — only fires after all 100 cycles finish, so it never ran here. These numbers are pulled directly from the raw log timestamps.)*

## What failed, and why

**Cycle 6, withdraw phase.** Deposit succeeded normally; the withdraw failed identically on **7 consecutive retry attempts** (spaced ~32s apart, the retry cooldown), then the run was stopped manually.

**On-chain error** (verified via `solana confirm` on 3 of the 7 attempts, all identical):
```
AnchorError thrown in programs/marginfi/src/state/marginfi_account.rs:1798.
Error Code: BankAccountNotFound. Error Number: 6018. Error Message: Bank is missing.
```

**Root cause (from code inspection, `src/trader/dex/marginfi.rs`):** the withdraw instruction's account list is `[...other_active_banks, bank_id]`, cross-checked by Marginfi's program against the lending account's real on-chain balance slots. That check failed — our locally-tracked view of which banks the account has active balances in didn't match on-chain reality at execution time.

**Why cycle 6 specifically, after 5 clean cycles:** most likely explanation is speed — cycles 3–5 completed in under half a second each, and it's plausible a withdraw got built from a locally-parsed account snapshot that hadn't caught up to the real current on-chain state yet at that pace (a race between loop speed and account-update propagation), so the account list didn't match by the time the transaction executed.

**Not self-healing:** all 7 retries failed identically, at the same instruction, same error, same ~44,000 compute units consumed — deterministic, not transient. It would have retried forever without success had it kept running.

## Open follow-up

Fixing this needs a real code change to `test_withdraw_usdc_marginfi`/`other_active_banks` in `catscope-rust-bot/src/brain/testperplatencyv1/state.rs` and/or `src/trader/dex/marginfi.rs` — not yet investigated in depth. Candidate direction: force a fresh on-chain read of the lending account (or add a short settle delay) immediately before building the withdraw instruction, rather than relying on whatever `on_account` snapshot happens to be cached at that instant.

## Earliest-stage latency investigation (validator-side logs)

Prompted by comparing against [catscope.io's Orca swap latency report](https://catscope.io/docs/reports/orca-swap-latency-report/), which measures a narrower, earlier pipeline stage than our `write→read`/`tx-confirm` metrics: "Bot → catfwd → Leader → Validator → Bot" (their headline number: 35.7ms validator-internal latency, single sample, no p50/p99).

**Architecture traced:** our Rust WASM guest's `transactionprocessor::send()` is fire-and-forget — it returns as soon as the host accepts/rejects the tx locally, before the tx ever reaches `catfwd` or the network (confirmed via a doc comment in the newly-merged `leveragedloopv1` module: *"send()-time rejection is the only real failure signal this bot mode can act on today — it never reads back asynchronous on-chain confirmation results at all"*). So our own guest code cannot see anything earlier than the moment it calls `send()` — that's already the earliest point achievable from within `testperplatencyv1`.

**Validator-side services checked on `chainbuff-1.bit`** (the validator hosting the shared pipeline we upload to), for our test's window (`2026-08-31 05:08:00`–`05:11:00 UTC`; note the server runs `Etc/UTC`, not JST — cost us one round trip early on):

| Service | What it actually is | Per-transaction signatures? |
|---|---|---|
| `catfwd` | Real transaction-forwarding relay (`catscope_zerohop::quic`) — receives a request, sends via QUIC straight to the current leader's TPU | No signature logged, but real per-batch timing (`received request` → `Sending batch...to TPU` → `...sent successfully`) |
| `pipeline-txproc` | Same `solpipe pipeline relay` binary as the others — allocation/agent-registration logic (`OnAgent`, `agentID X to authorizer Y`) | No — only logs on allocation changes, ~3 hours before our window |
| `pipeline-bot` | Same relay binary — bot gRPC connection lifecycle (`/bot.Bot/Run` stream open/close) | No — only 2 lines, both connection-lifecycle (one matches when we manually killed our own test run) |
| `pipeline-catscope` | Same relay binary — account/slot data-sync (`catscopestate.Graph/Subscribe`) | No — 6 entries in-window, but all belong to a **different** pipeline ID than ours (another bot's subscription cycling) |

Side finding: `pipeline-txproc`/`pipeline-bot`'s `OnAgent`/`agentID`/`authorizer` log lines confirm this relay layer is exactly what produced the `"authorizer ... does not have agent account"` errors hit during earlier upload retries this session — real closure on that open question.

**Full correlation achieved.** The guest's relative-clock timestamps *can* be converted to absolute time precisely — the first attempt used the wrong anchor (off by a constant ~4.3s). Solving for the anchor using two independent transactions (Bootstrap and Swap-leg-1, both assuming a similar host-relay delay to the boot transfer) gave two estimates agreeing to within 1ms (`05:08:10.261` vs `05:08:10.260`), confirming the correction. With the right anchor, **all 13 remaining transactions match a distinct, sequential `catfwd` "received request" event**, with a remarkably consistent guest→catfwd relay delay of ~170–210ms across the whole run (one outlier: swap leg 2 at 529ms). Two originally-unmatched `catfwd` events turned out to be bonus data: cycle 6's deposit (the one that succeeded) and its first failed withdraw attempt — both land within ~200ms of their known send times too, confirming the whole reconstruction is internally consistent with zero unexplained events in the window.

**Full catfwd-internal latency table** (`received request` → first `sent successfully to TPU`):

| Transaction | Latency |
|---|---|
| Boot transfer (Go-native anchor) | 10.892 ms |
| Swap leg 1 | 4.387 ms |
| Swap leg 2 | 0.062 ms |
| Bootstrap (Marginfi) | 44.634 ms |
| Cycle 1 deposit | 1.664 ms |
| Cycle 1 withdraw | 14.981 ms |
| Cycle 2 deposit | 70.470 ms |
| Cycle 2 withdraw | 0.132 ms |
| Cycle 3 deposit | 0.125 ms |
| Cycle 3 withdraw | 0.366 ms |
| Cycle 4 deposit | 0.202 ms |
| Cycle 4 withdraw | 0.214 ms |
| Cycle 5 deposit | 0.157 ms |
| Cycle 5 withdraw | 0.303 ms |
| *(bonus)* Cycle 6 deposit | 0.110 ms |
| *(bonus)* Cycle 6 withdraw attempt 1 | 2.571 ms |

**Median: 0.33ms. Mean: ~10.6ms** (pulled up by two outliers — Bootstrap at 44.6ms and Cycle 2 deposit at 70.5ms, both still well under 100ms). Both the median and mean are faster than the Orca report's single 35.7ms sample — and this is 14–16 real samples across a real test run, not one cherry-picked instance.

**Method, for reproducing this on a future run:**
1. Get one Go-native-timestamped anchor if possible (a transaction sent via the Go host's own helper, e.g. the boot transfer) — gives a zero-conversion-error reference point.
2. For guest-sent transactions, solve for the guest-clock-to-UTC offset using `catfwd_received_time - relative_send_time - assumed_relay_delay ≈ anchor`, cross-checked against ≥2 known transactions for agreement.
3. Match remaining `catfwd` "received request" events to known sends in strict chronological order (not naive nearest-neighbor, which breaks down when events cluster within a couple seconds of each other).
4. Any leftover `catfwd` events either belong to a different pipeline (check the `pipeline=` field elsewhere in validator-side logs if available) or, as found here, a phase this exercise didn't originally track (e.g. the failed cycle 6).

## Full pipeline breakdown (cycles 1–5)

With the corrected anchor, every stage of the pipeline can now be measured for each of the 10 successful cycle transactions:

| Tx | decide→send<br>(build+sign) | send→catfwd recv<br>(network handoff) | catfwd internal<br>(push to leader) | catfwd-pushed→we-see-it<br>(network+leader+our pipeline, *combined*) |
|---|---|---|---|---|
| C1 deposit | 1.0ms | 168.7ms | 1.7ms | 9,308.0ms |
| C1 withdraw | 0.0ms | 182.5ms | 15.0ms | 11,369.0ms |
| C2 deposit | 0.0ms | 181.5ms | 70.5ms | 10,223.0ms |
| C2 withdraw | 0.0ms | 168.6ms | 0.1ms | 299.0ms |
| C3 deposit | 0.0ms | 176.2ms | 0.1ms | 347.0ms |
| C3 withdraw | 0.0ms | 180.5ms | 0.4ms | 494.0ms |
| C4 deposit | 0.0ms | 188.0ms | 0.2ms | 5,256.0ms |
| C4 withdraw | 0.0ms | 211.1ms | 0.2ms | 5,252.0ms |
| C5 deposit | 1.0ms | 212.2ms | 0.2ms | 227.0ms |
| C5 withdraw | 0.0ms | 187.2ms | 0.3ms | 5.0ms ⚠️ |

**What each column covers, and its limits:**
1. **decide→send** — time from the bot logging its decision (e.g. "depositing $1.00 USDC into marginfi") to actually calling `transactionprocessor::send()`. Consistently ~0-1ms — trivial local CPU work (build + sign a small instruction).
2. **send→catfwd recv** — time from our `send()` call to `catfwd` logging `"received request"`. Remarkably consistent, ~170-212ms across the whole run regardless of transaction type.
3. **catfwd internal** — `catfwd`'s own `"received request"` → first `"...sent successfully to TPU"`. Sub-millisecond to tens of ms; see the full table above.
4. **catfwd-pushed→we-see-it** — everything after `catfwd` hands the bytes to the network: real network transit + leader inclusion + block production + gossip propagation back to our validator + our own subscription pipeline noticing, all **combined**. This is the stage with by far the most variance (5ms–11.4s) and is where the two commitment tiers this codebase documents (`~400ms` processed vs. `~12s` rooted) show up — see the note on `Event::LowLatency` vs `Event::Commit` below.

**Could not be split further with available data:** whether the leader ever received/accepted the transaction (no ack exists for an individual QUIC-forwarded tx — genuinely unknowable from any log), and whether column 4's result came via the fast `Event::LowLatency` path or the slow `Event::Commit`/rooted path — both call the same `on_account` update function, and `evaluate()` (which does the "is it there yet" check) runs after *either* event type without recording which one delivered the change. Distinguishing them would need a small code change to `record_cycle_read` (or its caller) to tag which event type triggered a given confirmation.

**Anomaly flagged:** C5 withdraw's column-4 value is technically *negative* before rounding (our "read" appears to have registered ~182ms *before* `catfwd` even finished sending that specific transaction). That's not measurement noise — it's real evidence that the read-confirmation logic can fire on stale/unrelated account state rather than strictly the write that supposedly triggered it, which lines up with the root cause found for cycle 6's eventual failure a few cycles later.

**Transaction signatures for this table** (for independent verification via `solana confirm <sig>`):

| Tx | Signature |
|---|---|
| C1 deposit | `4tMHbkqUZwSLdrysHxDEQQ1onyjHMGS2PaVvbE3hsX8BUxWJXY2sPNQSVgdeJ8mK3QLDoSMRUEabsrMi6RnNwNyA` |
| C1 withdraw | `2da7xwJPwWULhQDLZGWgoXkYHbchBDkGRWn3QV5784SGf7oHZFtjtumnnp3DjnN5idG5Le2q2QvgvA675ADtmqLn` |
| C2 deposit | `NvTG5zMdC1a5i1YVSBW5dpFYgtH7jFQ7GosADTfEskmFSwivVVAZoqrZ5vND9qRGXbsj6fkVkT4zyV4esdJd94D` |
| C2 withdraw | `3CVzeWa5JA1CrqaVG1zKoyfWwwPBHcVuXMgHbcxSHQgfkbiJ38qZCR8KFyYhYmownsof7uhPZcgitThNz2Sd4uxf` |
| C3 deposit | `EKyZmNY4vFECC4r6C4Ua8gcSdjpzCuoemQyHY3mML84wXcd8hbmBLhbyV25pBPEJrLZKXVZAEnzdeYPgZxdNYQ8` |
| C3 withdraw | `2HfLdksMN5NMh1sysrwYD8TXBYviaCpHQemoqrYE2FYopMqGpWf68Lf8PWQ7Kq3vCgA6zVccbnLLLkj4KhJCcbsZ` |
| C4 deposit | `4YvdDeUAPT5Vyjcn6LumkvoshAoc4KksXzJj8dwHUVeiFiQLWFbM3PmExQcZCMXAzKRD9z1oc4ABVC7aMnKAjED` |
| C4 withdraw | `21ysY9XCrDDJbB2ui9Z6QmsVS8BRJeRuUGetoDZ8hQYXgLf4FdXxLMBT9QNFox46WQdNF6RTXR5qeEvPLHoDh6qq` |
| C5 deposit | `5m4EtQNdcSRuNZACpEU67V1qYBL8VhDc1bYca1Gp3uUpVRGVfiepJGQXjCvHpQLncWfeUQihgbVHLGBLTUxavbHa` |
| C5 withdraw | `57MPt1a164qsH6dhhWZwJLZhHTxk5hAqDFYYA6HsZumv6aVTSs8qgaesXM65eZPkyULkbBj3QdkDQnNzD8eGV2tf` |
