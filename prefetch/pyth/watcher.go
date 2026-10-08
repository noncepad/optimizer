package pyth

import (
	"context"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/solana-foundation/solana-go/v2"
)

// PriceUpdate is one live update delivered on Watch's channel.
type PriceUpdate struct {
	Feed [32]byte
	PriceUpdateV2
}

// Watch subscribes to feeds (each a Pyth feed ID, e.g. SOLUSDFeedID) via
// client's own real-time account graph stream and returns a channel
// delivering a PriceUpdate every time any of them changes on-chain --
// genuinely push-based, not polled: no per-update network round trip
// happens beyond the one long-lived stream client already holds open,
// so there's no equivalent of the Jupiter Price API's rate limit to run
// into (see optimizer/prefetch/price-feed's own doc comment on that
// limit -- this package exists as an alternative that avoids it
// entirely, for feeds Pyth actually publishes).
//
// The returned update channel is closed once the subscription ends
// (ctx cancelled, or the underlying state.Client.Hook call errors); the
// error channel receives the terminal error, if any (nil on a clean
// ctx-cancellation shutdown, matching state.Client.Hook's own
// convention of treating context.Canceled as no error).
func Watch(ctx context.Context, client state.Client, feeds ...[32]byte) (<-chan PriceUpdate, <-chan error, error) {
	if len(feeds) == 0 {
		return nil, nil, fmt.Errorf("pyth: at least one feed is required")
	}
	accounts := make(map[sgo.PublicKey][32]byte, len(feeds))
	for _, feed := range feeds {
		pda, err := FeedAccount(feed)
		if err != nil {
			return nil, nil, err
		}
		accounts[pda] = feed
	}

	// state.Client.Hook requires a logger already attached to the
	// hook's own ctx (errors with "missing logger" otherwise, see its
	// own source) -- carry whatever's already on ctx forward explicitly
	// rather than assume the caller did this themselves.
	hookCtx := logger.ToContext(ctx, logger.FromContext(ctx))

	updateC := make(chan PriceUpdate, len(feeds))
	errC := make(chan error, 1)
	h := &watchHook{ctx: hookCtx, accounts: accounts, updateC: updateC}
	go func() {
		defer close(updateC)
		errC <- client.Hook(h)
	}()
	return updateC, errC, nil
}

// watchHook is a plain graph.Hook (not eino/solpipe.SuperHook -- that
// wrapper decodes solpipe-market/cba-program accounts specifically, an
// unrelated concern to a single Pyth price account; see
// optimizer/harness's own treasuryHook for that other, market-aware use
// of the same state.Client.Hook entry point). Mirrors
// git.noncepad.com/pkg/bot/state's own query.go queryStreamHook shape:
// the minimal Hook needed to keep a subscription open indefinitely and
// react to account updates.
type watchHook struct {
	ctx      context.Context
	accounts map[sgo.PublicKey][32]byte // account pubkey -> feed ID
	updateC  chan<- PriceUpdate
}

func (h *watchHook) Ctx() context.Context { return h.ctx }

// Init subscribes to every tracked feed account, depth 1 (the account
// itself only -- see graph.Graph.Subscribe's own doc comment: "Depth==1
// fetches the root account only"), no descendants needed for a single
// price account.
func (h *watchHook) Init(g graph.Graph) error {
	for pubkey := range h.accounts {
		g.Subscribe(h.ctx, pubkey, graph.WeightDirect, 1)
	}
	return nil
}

func (h *watchHook) OnSlot(graph.Slot, graph.SlotStatus) {}

func (h *watchHook) CommitStart(graph.Slot) {}

func (h *watchHook) OnAccount(account graph.Account, isNew bool) {
	header := account.Header()
	feed, tracked := h.accounts[header.Pubkey]
	if !tracked {
		return
	}
	parsed, err := ParsePushOracle(account.Data())
	if err != nil {
		// Not (yet) a valid PriceUpdateV2 payload -- e.g. the account
		// hasn't been initialized on-chain yet. Skip silently; the next
		// update for this account gets the same chance.
		return
	}
	select {
	case h.updateC <- PriceUpdate{Feed: feed, PriceUpdateV2: parsed}:
	case <-h.ctx.Done():
	}
}

// CommitFinish returning false keeps the subscription open until ctx is
// cancelled -- see state.Client.Hook's own loop
// (isDone = hook.CommitFinish(); for !isDone { ... }): returning true
// would stop the whole subscription after the very first commit, which
// is exactly wrong for a feed meant to run for the life of ctx.
func (h *watchHook) CommitFinish() bool {
	return false
}
