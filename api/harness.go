// Command harness is a minimal tool-calling agent, built the same way as
// go-wiki/examples/eino-ollama-agent/main.go (github.com/cloudwego/eino's
// prebuilt ReAct agent: call the model -> if it asked for a tool, run the
// tool and feed the result back -> repeat until the model answers without
// a tool call), but with walletTools() (see tools.go) in place of that
// example's demoTools().
//
// walletTools() gives the agent read-only visibility into this repo's
// real trading wallet: its Solend/Kamino lending obligations, its raw
// token balances, live Jupiter USD prices, and total portfolio value --
// the exact queries run by hand, script by script, over the course of a
// live session unwinding real positions in this wallet. It does not
// expose any of that session's mutating actions (repay/withdraw/sweep);
// see walletTools' own doc comment for why.
//
// Usage:
//
//	go run . -prompt "What obligations are currently open, and what's the wallet worth right now?"
package api

import (
	"context"
	"fmt"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
)

// NewStateClient dials the same internal state.Client/gRPC graph
// endpoint every other read in this codebase uses (see
// cmd/multimodel.go's identical stateAddr/DefaultDialer construction),
// given a raw -state-url flag value. Exported so other standalone
// entrypoints building on this package (e.g. optimizer/hedgefund's CLI)
// can dial the same way without duplicating this parsing.
func NewStateClient(ctx context.Context, stateURL string) (state.Client, error) {
	if len(stateURL) == 0 {
		return state.Client{}, fmt.Errorf("-state-url is required (tcp://ip:port or unix:///path to a catscope state endpoint)")
	}
	stateAddr, err := bidder.ParseAddress(stateURL)
	if err != nil {
		return state.Client{}, fmt.Errorf("parse -state-url %q: %w", stateURL, err)
	}
	return state.New(ctx, state.DefaultDialer(stateAddr), 30*time.Second), nil
}

type Base interface {
	Ctx() context.Context
	CloseSignal() <-chan error
	Close() error
}
