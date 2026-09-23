// Package chainstate is the one concrete api.ChainState -- every cmd
// command that only needs a prefetch.db handle (no live state.Client
// subscription) previously called store.Open directly and duplicated its
// own defer-close boilerplate; this collects that into one constructor.
//
// Real limitation, not papered over: Account (the live-account half of
// api.ChainState) is a strict subset of what most cmd commands actually
// need from a state.Client. It answers "what is this one account right
// now" (via util.FetchAccount's single depth-1 subscribe-then-cancel), not
// a live subscription (state.Client.Hook, used directly by
// cmd/watchobligations.go) or an aggregated multi-account read
// (util.FetchWalletBalance, used by cmd/watchpnl.go). Commands built
// around a raw state.Client passed into an external constructor
// (prefetch.Create, the per-DEX prefetch.*.Create functions) need that
// literal type, which api.ChainState does not expose -- those commands are
// deliberately left alone rather than forced through an interface that
// doesn't fit their real access pattern.
package chainstate

import (
	"context"
	"errors"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/api"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

type external struct {
	ctx         context.Context
	cancel      context.CancelCauseFunc
	db          *store.DB
	stateClient state.Client
}

// Create opens dbPath (via store.Open, applying its schema the same way
// every direct store.Open caller already relied on) and returns it as an
// api.ChainState. stateClient is optional -- pass the zero value
// (state.Client{}) for a command that only ever calls Database(), the same
// commands that never declared a stateClient variable at all before this.
// Closing the returned value closes the underlying db; callers must not
// also separately defer db.Close().
func Create(parentCtx context.Context, dbPath string, stateClient state.Client) (api.ChainState, error) {
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("chainstate: open %s: %w", dbPath, err)
	}
	ctx, cancel := context.WithCancelCause(parentCtx)
	return &external{ctx: ctx, cancel: cancel, db: db, stateClient: stateClient}, nil
}

func (e *external) Ctx() context.Context {
	return e.ctx
}

func (e *external) CloseSignal() <-chan error {
	signalC := make(chan error, 1)
	go func() {
		<-e.ctx.Done()
		signalC <- context.Cause(e.ctx)
	}()
	return signalC
}

func (e *external) Close() error {
	e.cancel(errors.New("chainstate: close"))
	return e.db.Close()
}

func (e *external) Database() *store.DB {
	return e.db
}

// Account fetches pubkey live via util.FetchAccount -- a single depth-1
// subscribe-then-cancel against this ChainState's own stateClient, not a
// database read. Returns nil (not an error -- api.ChainState.Account has
// no error return) if the account doesn't exist or the fetch itself
// failed; callers that need to distinguish those two cases, or need a live
// subscription rather than a one-shot read, should use util.FetchAccount
// or stateClient.Hook directly instead, same as cmd/watchobligations.go
// already does.
func (e *external) Account(pubkey sgo.PublicKey) graph.Account {
	account, found, err := util.FetchAccount(e.ctx, e.stateClient, pubkey)
	if err != nil || !found {
		return nil
	}
	return account
}
