# hedgefund

A four-agent "hedge fund" system for the Solana trading bot in
`git.noncepad.com/pkg/optimizer` (specifically its `multimodelv1` mode --
see `optimizer/cmd/multimodel.go`), built on [eino](https://github.com/cloudwego/eino):
one agent for **risk**, one for **P&L**, one for **research**, and one
**fund manager** (`compose.Workflow`) that fans all three into a single
synthesized decision.

This is a standalone CLI, runnable today (see [Usage](#usage) below) --
not a library waiting on a caller. For the other, independently built
hedge-fund implementation (`gitlab.noncepad.com/eflam/wiki/client/hedgefund`,
`compose.Workflow`-based text-to-SQL risk/pnl agents, gated real trigger
access), see [Relationship to `go-wiki/client/hedgefund`](#relationship-to-go-wikiclienthedgefund)
below -- that one's README documents its own, different status.

## Status

All four agent objects are built and tested (29 passing tests,
`go build`/`go vet` clean):

| File | Agent | Shape |
|---|---|---|
| `risk.go` | risk | `react.NewAgent` ReAct loop over `optimizer/harness`'s live `state.Client` wallet tools |
| `pnl.go` | P&L | `react.NewAgent` ReAct loop over one tool wrapping `optimizer/prefetch/pnl.PositionsBetween` |
| `research.go` | research | `react.NewAgent` ReAct loop over local-file tools (PDF/txt/md) |
| `manager.go` | fund manager | `compose.Workflow` fanning risk/pnl/research (from `compose.START`) into one synthesis node |

**The fund manager never has real trigger access, at all, full stop.**
`FundDecision` mirrors `optimizer/cmd/multimodel.go`'s own CLI flags
field-for-field so a human can translate it directly into the real
command they'd run by hand -- but this package never calls that command
or holds a `multimodelv1.Hook`. There is no gate to bypass here because
there is no trigger tool in this package to begin with -- a stronger
guarantee than "gated" (compare `go-wiki/client/hedgefund`'s fund
manager, which does hold real trigger tools behind a `*TriggerGate`).
Every `manager` run prints a `PROPOSAL ONLY` banner ahead of whatever it
proposes.

`../contrib/hedgefund/Dockerfile` is an unfinished stub (`FROM
registry.noncepad.com/eflam/solpipe-terminal/run:dev`, nothing else yet)
-- there is no working container image for this today, only the `go run`
usage below. Go source lives here, in `hedgefund/`; `contrib/hedgefund/`
holds only the Dockerfile, per this repo's convention that `contrib/`
subdirectories are for Dockerfiles and other non-code files, never Go
source.

## Usage

```bash
cd optimizer/hedgefund
go run . -node risk     -prompt "What's our real risk exposure right now?"
go run . -node pnl      -prompt "How has our value changed over the last week?"
go run . -node research -papers-dir /path/to/papers -prompt "Any new model ideas?"
go run . -node manager  -papers-dir /path/to/papers -prompt "Should we do anything right now?"
```

`-prompt` is optional for every node -- each has a sensible default
question (see `main.go`) if left blank.

### Prerequisites

- **A reachable Ollama server** with a tool-calling-capable model pulled
  (default `qwen3-coder:30b` at `http://localhost:21434`). Override with
  `-model`/`-ollama-url`. A smaller model (e.g. `qwen2.5:7b`) will run,
  but live-verified this session to reason less reliably about the
  numbers its own tools hand it -- worth knowing before trusting a
  `manager` proposal from a weaker model.
- **`risk`/`manager`** additionally need a reachable catscope state
  endpoint (`-state-url`, required, `tcp://ip:port` or `unix:///path`) --
  the same internal gRPC/geyser-backed `state.Client` graph the live
  trading bot itself reads through (`git.noncepad.com/pkg/bot/state`),
  not the public Solana RPC endpoint -- plus Jupiter price API access
  (no flag needed, a plain HTTPS call) and a wallet to inspect (`-wallet`,
  defaults to `harness.DefaultWallet` -- the real trading child wallet
  this whole session's work centered on; public key only, no private key
  ever needed since every tool is read-only).
- **`pnl`/`manager`** additionally need a real `prefetch.db`
  (`-db`, defaults to `~/.optimizer/prefetch.db`) -- the same database
  `optimizer watch-pnl`/the live trading bot itself writes to. An empty
  or missing database isn't an error; `pnl` just reports "no positions
  recorded."
- **`research`/`manager`** additionally read a local directory
  (`-papers-dir`, defaults to `~/.optimizer/research-papers`) of
  `.pdf`/`.txt`/`.md` files, flat (not recursive). An empty or missing
  directory isn't an error either; `research` just reports nothing to
  propose.

All four flags above (`-state-url`, `-wallet`, `-db`, `-papers-dir`) plus
`-model`/`-ollama-url`/`-timeout` are shared across every `-node` value;
each node only actually uses the ones it needs.

### Example

```
$ go run . -node manager -papers-dir ~/research -prompt "Should we open any new positions?"
wallet: Hg2p3cfmg3dratEVy94JArTVM7KzEywgNdmFnrfhroh9
> Should we open any new positions?

=== PROPOSAL ONLY -- no real transaction has been sent ===

Rationale: <the model's reasoning over the real risk/pnl/research findings>

  PROPOSED: enable_directional_trading (target So1111...)
```

or, when the model decides nothing is warranted:

```
  (no action proposed)
```

## Testing

Every node has its own `_test.go` (29 tests total). Agent construction
and round-trip/error-propagation tests use `fakeToolCallingModel`
(`fake_model_test.go`) -- deterministic, no live Ollama/network needed.
Where real local infrastructure is cheap and worth exercising for real
rather than mocking, tests use it directly: `pnl_test.go` seeds a real
temp SQLite database (`newTestStore`, same pattern
`go-wiki/client/hedgefund`'s own tests use) with real
`pnl_position_snapshot` rows; `research_test.go` writes real temp files,
including a real path-traversal attempt against a real file placed just
outside the configured directory, to prove `resolvePaperPath`'s guard
actually blocks it rather than just asserting the string check fires in
isolation. `manager_test.go` builds `BuildFundManagerWorkflow` (the
testable graph-wiring function -- see its own doc comment) with three
stub agents and confirms the fan-in wiring is correct by giving each
stub a unique marker response and checking all three reach the
`fund_manager` node's prompt in the right order; this was verified to
actually catch a broken mapping (a field-mapping swap was tried by hand
once and confirmed to fail the test) before being reverted.

Live network/RPC/Ollama calls are never made in `go test` -- only via
`go run` per [Usage](#usage) above.

## Relationship to `go-wiki/client/hedgefund`

See that package's own README for the full comparison table. Short
version: same four-agent shape, different choices throughout (live
state.Client graph reads vs. persisted-snapshot data, ReAct-tool-calling
vs. text-to-SQL agent
shape, no trigger access at all here vs. hard-gated real trigger access
there). Neither supersedes the other; nobody has yet decided whether or
how to converge them.
