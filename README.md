# Catscope Optimizer

Run a web assembly bot that will run in [the Catscope runtime](https://catscope.io) over [Solpipe](https://solpipe.io).
Solpipe sets up a gRPC tunnel.  The Optimizer code base talks with the
local Solpipe bidder daemon to access the target Validator (Pipeline=`2Gi87bD2MNKFREReqkJgcLodkBuQEcYGKaaWz45i2Nbo`).
The main event loop is in `./brain/simple/simple.go`.

The rest of this page describes how to run the optimizer.  This involves
setting up Safejar and Solpipe CLI tools, creating some onchain accounts,
and connecting to the validator.

## Download Solpipe

Download a Docker image with [Safejar](https://safejar.io) and [Solpipe](https://solpipe.io) command line tools.

### Linux (Debian Trixie)

```bash
sudo apt-get update \
  && sudo apt-get install -y wget gnupg build-essential curl \
    pkg-config \
    libudev-dev llvm libclang-dev \
    protobuf-compiler libssl-dev \
  && wget -O- https://noncepad.com/keys/nightly/trixie.sources > /tmp/sources \
  && sudo mv /tmp/sources /etc/apt/sources.list.d/noncepad.trixie.sources \
  && sudo apt-get update \
  && sudo apt-get install -y solpipe
```

### Docker

```bash
docker build -t solpipe:nightly https://noncepad.com/keys/nightly/Dockerfile
```

### Mac OS

```bash
brew install noncepad/homebrew-tap/noncepad-solpipe
```

To do an upgrade:

```bash
brew install noncepad-solpipe
```

## Prerequisite

We must separate the main custody of funds from the bidder daemon that automatically pays USD for API access.
Set up a Jar, an onchain Solana account that controls funds like a treasury.  
The *Jar owner* controls all funds, so owner signatures need to be done on a special PC.  For starting out,
we can ignore this requirement.  Once the Jar has been created, we will then delegate funds
to the Bidder daemon on a market by market basis.

```bash
WALLET=usb://ledger?key=10
JAR_OWNER=usb://ledger?key=20
safejar --fee-payer=$WALLET jar $WALLET
```

* fund `$WALLET` with 0.05 SOL to start

For small amount of funds, just use a file based private key:

```bash
solana-keygen new -o ./owner.json
solana-keygen new -o ./wallet.json
safejar --fee-payer=./wallet.json jar ./owner.json
```

* set the variable `JAR_ID` from the output of `safejar jar init`
* fund the system account `./authorizer.json` with SOL for transaction fees and account rent

## Run daemon

The bidder daemon is a background process that handles a soft wallet, pipeline auction budgets, and proxies
gRPC connections to pipelines.  This must be running for the commands below to work.

Run the daemon:

```bash
solpipe --fee-payer=./authorizer.json bidder proxy ./proxy.json -v
```

The daemon is accessible at:

* management socket: `$HOME/.solpipe.bidder.manage.sock`
* gRPC proxy socket: `$HOME/.solpipe.bidder.proxy.sock`

## Join Bot Market

For each market place, the user needs to:

1. create a Safejar delegation account using a rule set derived by the bidder daemon
1. instruct the bidder daemon to join the market place
1. set a budget cap and pipeline allocation.
   * to connect to pipelines for free idle capacity, just set the budget cap to 0
1. programmatically adjust the portfolio and budget

### Delegate funds

```bash
MARKET=6VQk8GA84p7zZSyL8XtX6oVd3Vp4EJ5hoUenKoC3fHSf
solpipe bidder propose-rule-set $MARKET
```

An example output is:

```
Jar: EV4AHRFZDjVwgytVj77xEtGgTvdNfePJdwPiJSBjMiTf
Authorizer: 8L3PJAJTZu2Gx54h7KHNGvpTwxq7TxZkZeSVxXAnCi7m
Market: 6VQk8GA84p7zZSyL8XtX6oVd3Vp4EJ5hoUenKoC3fHSf
PcMint: EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v
Decimal: 41
Proposed Rule Set: ( RateLimiter(EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v,3000,7200) && AuthorizationConstraint(5CnV9KB5qMkmXcU2hUMHshVCstrppqr7NXjRX1Wg6z7m) ) || SweepV2(EV4AHRFZDjVwgytVj77xEtGgTvdNfePJdwPiJSBjMiTf,EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v,100)
```

* copy `( RateLimiter(EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v,3000,7200) && AuthorizationConstraint(5CnV9KB5qMkmXcU2hUMHshVCstrppqr7NXjRX1Wg6z7m) ) || SweepV2(EV4AHRFZDjVwgytVj77xEtGgTvdNfePJdwPiJSBjMiTf,EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v,100)` as the rule set

```bash
safejar --fee-payer=$WALLET delegation init $JAR_OWNER 1 --rule="( RateLimiter(EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v,3000,7200) && AuthorizationConstraint(5CnV9KB5qMkmXcU2hUMHshVCstrppqr7NXjRX1Wg6z7m) ) || SweepV2(EV4AHRFZDjVwgytVj77xEtGgTvdNfePJdwPiJSBjMiTf,EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v,100)"
```

Sample output:

```
A delegation account has been created for jar: EV4AHRFZDjVwgytVj77xEtGgTvdNfePJdwPiJSBjMiTf
  delegation account: GwE9Yt9K5X31kTs6HXaMHwMMGBHrjoGxSAbXaFFh1ZKA
  jar token account:  CpWtZMR5us4FFm4CFLzQDz1wMFuJzguTS9XeFWtQ4XB3
  mint:               EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v
```

### Proxy to Market

```bash
solpipe bidder listen "( RateLimiter(EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v,3000,7200) && AuthorizationConstraint(5CnV9KB5qMkmXcU2hUMHshVCstrppqr7NXjRX1Wg6z7m) ) || SweepV2(EV4AHRFZDjVwgytVj77xEtGgTvdNfePJdwPiJSBjMiTf,EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v,100)" 6VQk8GA84p7zZSyL8XtX6oVd3Vp4EJ5hoUenKoC3fHSf
```

Once the command exits, the bidder daemon will automatically connect to the pipelines service the gRPC protobuf.

## TUI

Interact with the daemon with:

```bash
solpipe bidder tui
```

## Compile Web Assembly Bot

```bash
cd $HOME/work 
git clone https://github.com/noncepad/catscope-rust-bot
cd catscope-rust-bot
git checkout v0.3.3-catscope
cargo build --target wasm32-wasip2 --release
```

* please check if there is a later version than `v0.3.3` as there may be many frequent updates

## Run Optimizer

The optimizer is an example of how to programmatically:

1. set a budget allocation for different pipelines (in this example, no USD/hour and 1 pipeline)
1. connect to the single pipeline
1. upload a bot to this pipeline (which sits in front a validator bot runtime)
1. run the bot and see stderr output

Build optimizer:

```bash
echo $BOT_IMAGE
cd $HOME/work/optimizer
solana-keygen new -o ./fee-payer.json
go build -o ./optimizer ./cmd
```

* the fee payer does not require any funds and is unused in the code

Run the optimizer:

```bash
BOT_IMAGE=$HOME/work/catscope-rust-bot/target/wasm32-wasip2/release/catscope_rust_bot.was ./optimizer run ./fee-payer.json -v
```

* Set `BOT_IMAGE` to the file path of the compiled web assembly bot
