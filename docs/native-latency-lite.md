# testlatencylitev1: native-transfer latency test + Astralane investigation

`testlatencylitev1` (`src/brain/testlatencylitev1/`) sends 20 real,
back-and-forth SOL transfers between a wallet and a derived second wallet,
timing how long each takes to be observed as confirmed via three independent
channels (`LowLatency`, `Commit`, `Transaction`). It's the same idea as the
real `testperplatencyv1` module, stripped down to just the native-transfer
loop -- no Solend/Kamino/Marginfi phases -- so a run is cheap and fast (real
runs land 20/20 in under 20 seconds).

## Running it

```
REPO=/path/to/catscope-rust-bot optimizer test-latency <fee-payer-keyfile> --protocol=native_lite
```

`--protocol=native_lite` is a Go-side-only sentinel (see
`brain/testperplatencyv1/init.go`'s `Init`) that selects this module instead
of the full `testperplatencyv1`. `REPO` must point at a `catscope-rust-bot`
checkout with the `testlatencylitev1` module present -- a separate repo from
this one, on the matching branch.

The wallet this test actually spends from is the same index-1 child every
other bot mode derives (`common.DeriveChildKeyFromIndex(parentKey, 1)`, see
`optimizer`'s `contrib/derive-child-key`) -- fund it with a small amount of
SOL (~0.05 is comfortable for 20 transfers + fees + Astralane tips) before
running, or the test will just log `waiting for <id> to be funded` every
cooldown window and never send anything.

## Generating a report

```
optimizer latencyreport <log-file>
```

Turns the run's log output into a self-contained HTML report (per-lane
percentiles, write/read bounds, tx.index-based estimates, Astralane bundling
status) -- see `optimizer`'s `cmd/latencyreport.go` for the full column
glossary rendered right in the report itself.

Output defaults to `<log-file>.html`, or pass `--out <path>` to name it
yourself. The tool never generates the "Latency floor and assumptions"
section on its own -- that's deployment-specific commentary, not something a
general-purpose report should assert by default. It only belongs in a report
someone is actually publishing/sharing, added by hand from the reference
prose at the bottom of this doc.

Past runs are saved in [`latency-reports/`](../latency-reports/) in this
repo.

## The Astralane investigation (2026-09-18 through 2026-09-21)

Astralane bundling in this module was disabled on 2026-09-14 (`fixing
transfer problem`) on the diagnosis that Astralane's `sendIdeal` hard-requires
exactly 2 transactions and this module's old `send_native_transfer_via_astralane`
only ever built one. That diagnosis turned out to be incomplete: a real,
independently-confirmed mainnet run from 2026-09-09 (`native_run_lucky.log`,
`native_run_lucky3.log`) showed that exact single-tx code landing real,
tipped Astralane bundles successfully (`solana confirm -v` on two of its
signatures showed the tip transfer and the real transfer in the same
transaction, both confirmed).

What actually made Astralane fail in between was traced to the real cause via
`catfwd`'s own systemd journal on val1: every attempt was met with

```
catfwd server - received request
catfwd server - responding with error - 6
```

Error code 6 maps to `CatscopeZerohopError::ConfigError` in
`catscope-zerohop/src/err.rs` -- and reading `tiprouter.rs`'s real
`TxBundler::Astralane` handler on that repo's `t-11-astralane` branch showed
`ConfigError` is returned specifically when `catfwd`'s own Astralane backend
isn't configured at all (`self.astralane` is `None`), *before* the real
`l_tx.len() != 2` transaction-count check ever runs. So the 2026-09-14 fix's
diagnosis (transaction count) and this session's own first attempt at fixing
it (rewriting onto `Wallet::send_bundler_pair`'s 2-transaction durable-nonce
pair) were both solving a problem that wasn't the one actually happening --
`catfwd`'s Astralane backend was simply unconfigured on val1 at the time.

Once `catfwd` was relaunched with the correct binary/config and the
validator was rebooted, this exact single-tx code (unchanged since before
2026-09-14) landed 20/20 real Astralane-bundled transfers -- see
[`native_lite_astralane_run20_2026-09-21.html`](../latency-reports/native_lite_astralane_run20_2026-09-21.html).

## "Latency floor and assumptions" -- for hand-adding to a published report

`optimizer latencyreport` never generates this section itself -- it's
deployment-specific commentary, not something a general-purpose report tool
should assert by default. Add it by hand only when actually publishing a
polished report for this deployment:

> **Two structural facts can set the floor under every number above,
> depending on how and where this was run:**
>
> **If this was run using a non-voting/unstaked Catscope validator, note
> that** Solana's networking layer uses stake-weighted quality-of-service in
> both directions: a leader's QUIC connections favor higher-staked peers when
> accepting transactions under load, and a node's position in the
> stake-weighted Turbine tree governs how quickly it receives a new block's
> shreds via gossip and repair. With zero stake, an unstaked validator gets
> no priority TPU connection for sending and no shortened propagation path
> for receiving -- it's served after every higher-stake participant on the
> network, not because of anything about the transaction itself. That would
> be the real cause behind the write/read split above: the guest is timing
> not just when a transfer landed, but when *this specific validator* found
> out about it.
>
> **Solana's own block cadence sets a hard floor regardless of stake.** A
> transaction can only land once a leader actually produces a block
> containing it (~400ms slots), and this guest can only observe that landing
> once the resulting shreds physically propagate across the network to it.
> Neither step can be instantaneous for any validator, staked or not -- lack
> of stake only adds delay on top of that floor, it doesn't create it.
>
> **This is also why a run like this typically routes transfers through
> Astralane.** On a validator with no stake-weighted advantage of its own, a
> plain, unassisted transaction sent directly from it competes for a
> leader's attention on equal footing with every other unstaked sender -- no
> priority, and easily deprioritized or dropped outright under any real
> load. Astralane is a third-party bundler/relay that maintains its own
> direct routes to leaders and lands transactions against a paid tip rather
> than against stake, so paying that tip buys back the same kind of priority
> a well-staked validator already gets for free (see the Route column
> above). It isn't chasing speed for its own sake -- it's compensating for
> that same structural disadvantage when it applies.
