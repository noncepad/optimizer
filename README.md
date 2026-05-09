# Catscope Optimizer

Manage multiple bots from a single code base.

## Download Solpipe

Download a Docker image with [Safejar](https://safejar.io) and [Solpipe](https://solpipe.io) command line tools.

```bash
 docker build -t solpipe:nightly https://noncepad.com/dev/nightly.Dockerfile
```

## Prerequisite

Set up a Jar.  The owner controls all funds, so run this on a special PC.

```bash
safejar --fee-payer=usb://ledger?key=1 jar usb://ledger?key=10
```

For small amount of funds, just use a file based private key:

```bash
solana-keygen new -o ./owner.json
solana-keygen new -o ./authorizer.json
safejar --fee-payer=./authorizer.json jar ./owner.json
```

* fund the system account `./authorizer.json` with SOL for transaction fees and account rent

## Join a Market Place

For each market place, the user needs to:

1. create a Safejar delegation account using a rule set derived by the bidder daemon
1. instruct the bidder daemon to join the market place
1. set a budget cap and pipeline allocation.
   * to connect to pipelines for free idle capacity, just set the budget cap to 0
1. programmatically adjust the portfolio and budget

### Delegate funds

```bash
solpipe bidder gen-rule-set
```

```bash
safejar delegate
```

Option: fund the token account owned by the Delegation account.

### Join Market

```bash
solpipe bidder listen to some market
```

From this point, control the proxy daemon remotely using a local connection.
