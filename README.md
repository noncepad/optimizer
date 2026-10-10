# Catscope Optimizer

Run a web assembly bot that will run in [the Catscope runtime](https://catscope.io) over [Solpipe](https://solpipe.io). A local Solpipe bidder daemon sets up a gRPC tunnel to a validator. The Optimizer code base talks with the local Solpipe bidder daemon to access the target Validator (Pipeline=`2Gi87bD2MNKFREReqkJgcLodkBuQEcYGKaaWz45i2Nbo`). `./brain` is the generic, mode-agnostic bot-orchestration layer (see its own package doc comment); each trading/monitoring mode is one of its sibling packages -- currently `./brain/marketwatchv1`, `./brain/testperplatencyv1`, and `./brain/xstockshealthv1`.  The solpipe bidder proxy described in [the Catscope quick start tutorial](https://catscope.io/docs/tutorials/) must be running before the optimizer will work.

Build the CLI:

```sh
make
```

* the resuling binary will be in `./bin/optimizer`

Every command below takes `<parent-key>`, a file path to a Solana-CLI-format keypair JSON file (the fee payer/treasury parent -- not the bidder proxy's own fee payer). They all also expect a local Solpipe bidder daemon already running at its default sockets (`~/.solpipe.bidder.manage.sock`, `~/.solpipe.bidder.proxy.sock`) -- override via the `MANAGER_SOCKET`/`PROXY_SOCKET`/`STATE_URL`/`TXPROC_URL` environment variables if yours runs elsewhere. Set `PROGRAM_SOLPIPE` to the real on-chain Solpipe program id if the compiled-in default doesn't match the deployment you're pointed at. Any of these can go in a `.env` file in the working directory instead of being exported by hand.

## Populating prefetch.db

Every brain mode reads its market/lending data from a local SQLite database at `~/.optimizer/prefetch.db`, not live from chain -- populate (or refresh) it first:

```sh
./bin/optimizer download-arb <parent-key>
```

This walks every tracked DEX (Orca, Raydium AMM/CLMM/CPMM, Pumpswap, Pumpfun, Phoenix) and lending protocol (Solend, Kamino, Marginfi, Drift, Jet) via the bidder daemon's own state connection and writes the result into prefetch.db. Use `--exact=orca,raydium_amm,...` to scope it to specific sources, or `--force` to re-fetch everything even if already populated.

As its last step this command also compiles a release wasm bot image from the data it just wrote, as a smoke test -- see "Reaching each brain" below for what that needs (`REPO`, a `wasm32-wasip2` Rust toolchain). If that compile fails, the command reports an error, but prefetch.db itself has already been populated by that point regardless.

## Reaching each brain

Each brain mode needs two things beyond a populated prefetch.db:

* `REPO` (environment variable), pointing at a local checkout of `catscope-rust-bot` with a `wasm32-wasip2`-capable Rust toolchain (`rustup target add wasm32-wasip2`). Every mode compiles that repo into a release `.wasm` binary -- statically embedding the prefetch data above -- and uploads it to the validator pipeline before doing anything else.  Clone [the catscope-rust-bot repository from here](https://github.com/noncepad/catscope-rust-bot).
* Optionally `--working-dir` (defaults to `~/.optimizer`) and `--state-url` (defaults to the bidder daemon's own state connection).

```sh
# brain/marketwatchv1 -- read-only, never trades; live terminal dashboard
# of SOL/JUP/JTO/BONK/WIF/RAY volatility/correlation.
REPO=/path/to/catscope-rust-bot ./optimizer market-watch <parent-key>

# brain/testperplatencyv1 -- real-transaction, 100x-cycled deposit/withdraw
# latency test. Scope to one protocol with
# --protocol=solend|kamino|marginfi|native|native_lite, or leave unset
# for the original full unscoped sequence.
REPO=/path/to/catscope-rust-bot ./optimizer testperp-latency <parent-key>

# brain/xstockshealthv1 -- passive watcher: logs a health-factor board for
# Kamino xStocks obligations/reserves, sends no transactions. Serves a
# local page at --dashboard-addr (default 127.0.0.1:8091).
REPO=/path/to/catscope-rust-bot ./optimizer xstocks-health <parent-key>
```

Run `./bin/optimizer <command> --help` for every flag a given mode accepts, and see `./brain`'s own package doc comment for how a new mode plugs into this same upload/handshake flow.
