package util

import (
	"context"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

// FetchAccount fetches one already-known account address as a one-shot
// snapshot: subscribe at depth 1 (graph.Graph.Subscribe's own doc comment:
// "Depth==1 fetches the root account only" -- no neighbor expansion, which
// is exactly right for a single leaf address) and cancel the subscription's
// own context the moment its ack channel fires.
//
// This is deliberately NOT the pattern FetchWalletBalance/
// cmd/watchobligations.go's PendingSubscriptionStatus uses. That helper's
// own doc comment (util/subscribe.go) explains why it leaves a
// subscription's context alive until the whole fetch is done: an ack only
// means "registered with the server", not "data has arrived" --
// cancelling on ack was tried in this exact codebase before and confirmed
// (server-side: "grpc client is prematurely dropping") to sometimes cut a
// subscription off before its account data had actually streamed back.
// FetchAccount accepts that same tradeoff on purpose, scoped narrowly to a
// caller that only wants one value and is genuinely done with the
// subscription immediately after (e.g. answering a single synchronous
// tool call) -- anything that needs to keep observing an account for
// further updates should use the quiet-slot pattern instead of this.
func FetchAccount(ctx context.Context, stateClient state.Client, pubkey sgo.PublicKey) (graph.Account, bool, error) {
	hookCtx, cancel := context.WithCancelCause(ctx)
	handler := &singleAccountHandler{ctx: hookCtx, cancel: cancel, root: pubkey}
	if err := stateClient.Hook(handler); err != nil {
		return nil, false, fmt.Errorf("hook failed: %w", err)
	}
	em := handler.g.EdgeManager()
	em.Lock()
	defer em.Unlock()
	account := em.UnsafeAccount(pubkey)
	if account == nil {
		return nil, false, nil
	}
	return account, true, nil
}

// singleAccountHandler is a minimal graph.Hook: subscribe once to root at
// depth 1 (root account only) in Init, then cancel its own context -- with
// context.Canceled as the cause, so state.Client.Hook's own "cancellation
// is not an error" handling treats this as a normal, successful exit, not
// a failure -- as soon as the subscription's ack channel fires. See
// FetchAccount's doc comment for why this is safe here but not the
// pattern this codebase generally uses for account fetches.
type singleAccountHandler struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	root   sgo.PublicKey
	g      graph.Graph
}

func (h *singleAccountHandler) Ctx() context.Context { return h.ctx }

func (h *singleAccountHandler) Init(g graph.Graph) error {
	h.g = g
	ackC := g.Subscribe(h.ctx, h.root, graph.WeightAll, 1)
	doneC := h.ctx.Done()
	go func() {
		select {
		case <-doneC:
		case <-ackC:
			h.cancel(context.Canceled)
		}
	}()
	return nil
}

func (h *singleAccountHandler) OnSlot(slot graph.Slot, status graph.SlotStatus) {}

func (h *singleAccountHandler) CommitStart(slot graph.Slot) {}

func (h *singleAccountHandler) OnAccount(a graph.Account, isNew bool) {}

func (h *singleAccountHandler) CommitFinish() bool { return false }
