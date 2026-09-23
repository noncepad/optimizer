package perpfundingv1

import (
	"context"
	"errors"
	"fmt"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/optimizer/bundler"
	"git.noncepad.com/pkg/optimizer/prefetch/perpfunding"
	"git.noncepad.com/pkg/solpipe-util/common"
	"git.noncepad.com/pkg/solpipe-util/graph"
)

// ErrBotNotConnectedYet is returned by SendBundlerTipUpdate while the bot
// hasn't finished its handshake yet -- mirrors leveragedloopv1's own
// sentinel of the same name. Not an error condition RunTipBroadcaster
// treats specially; it just logs and retries on its next tick.
var ErrBotNotConnectedYet = errors.New("perpfundingv1: bot not connected yet")

// SendBundlerTipUpdate pushes a live bundler tip update to the running
// bot -- see bundler.RunTipBroadcaster, the periodic poller that calls
// this.
func (hs *eventHook) SendBundlerTipUpdate(update bundler.TipUpdate) error {
	if hs.instance == nil {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(bundler.DoBundlerTipUpdate(update)); err != nil {
		return fmt.Errorf("perpfundingv1: send bundler tip update: %w", err)
	}
	return nil
}

func (hs *eventHook) initWallet() error {
	hs.childKey = common.DeriveChildKeyFromIndex(hs.parentKey, 1)
	// perpfundingv1's Rust side has a spot-market execution hook
	// (execute_spot_leg) and Solend/Kamino obligation bootstrap
	// (bootstrap_solend_obligation/bootstrap_kamino_obligation) that build
	// and sign real transactions with this child key -- subscribe/register
	// the same way arbv1 does so the WASM bot can see its own wallet's
	// token/SOL balances.
	_ = hs.graph.Subscribe(hs.ctx, hs.childKey.PublicKey(), graph.WeightAll, 2)
	// Evaluate's boot transfer (below) reads the parent's balance via
	// solpipeState.System(hs.parentKey.PublicKey()) -- that only ever
	// returns real data for a pubkey the graph is actually subscribed to.
	// Without this, System() silently reads 0 for the parent forever, and
	// the boot transfer thinks it's already at or below target and skips
	// itself. Ported from testperpv1/eval.go, where this exact pattern
	// was live-verified after two bugs in the shared
	// bot/solpipe/bidder/manager package were fixed (commit.go's
	// CommitFinish was unconditionally returning true, which tells
	// state.Client.Hook to stop the whole graph subscription after the
	// very first commit; manager.go's Create never wrapped
	// mothershipSolpipe through bot/solpipe.New, so OnMarket/OnPipeline/
	// OnSol/etc were never dispatched to at all). Both fixed; see
	// optimizer/brain/testperpv1/subscribe_diag_test.go's
	// TestSubscribeViaManagerCreate for the regression test.
	_ = hs.graph.Subscribe(hs.ctx, hs.parentKey.PublicKey(), graph.WeightAll, 2)
	hs.builder.AppendKey(hs.parentKey)
	hs.builder.AppendKey(hs.childKey)
	return hs.instance.CustomStdin(DoWallet(hs.childKey))
}

// targetParentRemainingLamports is 0.08 SOL -- what should be left in the
// parent fee-payer wallet after Evaluate's one-time boot transfer below
// moves the rest to the child key. The child starts at zero balance (a
// fresh HKDF-derived key, see common.DeriveChildKeyFromIndex) and needs
// real SOL to pay its own transaction fees and the rent for the
// Solend/Kamino obligation accounts perpfundingv1's Rust-side bootstrap
// creates (obligation, user_metadata) -- nothing else funds it. Same
// value as testperpv1, which this was ported from.
const targetParentRemainingLamports = 80_000_000

// Evaluate fires on every solpipe state update. The only real thing it
// does is a single, one-time "boot transfer": move everything above
// targetParentRemainingLamports from the parent fee-payer to the child
// key, gated by hs.didBootTransfer so it only ever fires once. Ported
// from testperpv1/eval.go -- see initWallet's doc comment for why this
// depends on both of this session's bot/solpipe/bidder/manager fixes.
func (hs *eventHook) Evaluate(solpipeState brain.SolpipeState, bidderState brain.BidderState) error {
	if hs.didBootTransfer {
		return nil
	}
	parentSOL := solpipeState.System(hs.parentKey.PublicKey())
	if parentSOL == 0 {
		// solpipeState.System returns 0 both for "confirmed empty" and
		// "no account update delivered yet" (the graph subscription in
		// initWallet is async and doesn't block Init on its first
		// update) -- there's no way to tell those apart from this call
		// alone. Don't latch didBootTransfer here, or a real nonzero
		// balance that just hasn't arrived yet gets skipped for good.
		return nil
	}
	hs.didBootTransfer = true
	if parentSOL <= targetParentRemainingLamports {
		hs.logger.Info(fmt.Sprintf(
			"perpfundingv1: boot transfer skipped -- parent balance %d lamports already at or below the %d lamport target",
			parentSOL, targetParentRemainingLamports,
		))
		return nil
	}
	transferLamports := parentSOL - targetParentRemainingLamports
	ctx, cancel := context.WithTimeout(hs.ctx, 60*time.Second)
	helper, err := hs.builder.Helper(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("perpfundingv1: boot transfer: failed to create helper: %s", err)
	}
	helper.TransferSOL(hs.parentKey.PublicKey(), hs.childKey.PublicKey(), transferLamports)
	go func() {
		defer cancel()
		sig, slot, err := helper.FinishTx()
		if err != nil {
			hs.logger.Info(fmt.Sprintf("perpfundingv1: boot transfer failed: %s", err))
			return
		}
		hs.logger.Info(fmt.Sprintf(
			"perpfundingv1: boot transfer sent %d lamports parent->child (leaving ~%d lamports in parent): %s @ slot %d",
			transferLamports, targetParentRemainingLamports, sig, slot,
		))
	}()
	return nil
}

// computeTargetAllocation is a placeholder for the optimizer's own
// target-allocation decision -- what fraction of the portfolio should
// be in each symbol, the input SendTargetAllocation actually pushes to
// the bot. Real logic (whatever decides those values, and on what
// cadence) is intentionally not implemented here yet -- this just
// establishes the entry point so the rest of the pipeline (persistence
// + wire send, see SendTargetAllocation) has a real place to pull from
// once that logic exists.
//
// Placeholder value: 100% USDC. There's no explicit "USDC" entry to
// set -- per the convention established on the Rust side
// (target_allocation_pct's doc, catscope-rust-bot's
// src/brain/perpfundingv1/state.rs), USDC is always the implicit
// remainder, 1.0 minus every tracked symbol's allocation. An empty map
// means every tracked symbol is 0%, so the full 1.0 falls to USDC --
// deliberate, not an unset/error state.
func (hs *eventHook) computeTargetAllocation() map[string]float64 {
	return map[string]float64{}
}

// SendTargetAllocation persists symbol's new target portfolio allocation
// (fraction of total portfolio value, 0.0-1.0) to prefetch.db (the same
// table build.rs reads for the bot's compile-time default), then pushes
// it to the already-running bot via CustomStdin so it takes effect
// immediately, without a restart. Nothing calls this automatically yet
// -- it's the plumbing a future allocation-computation loop will call
// into. Rebalancing toward this target is what realizes profit/loss.
func (hs *eventHook) SendTargetAllocation(symbol string, allocationPct float64) error {
	if err := perpfunding.SetTargetAllocation(hs.db, symbol, allocationPct); err != nil {
		return fmt.Errorf("perpfundingv1: persist target allocation: %w", err)
	}
	if err := hs.instance.CustomStdin(DoTargetAllocation(symbol, allocationPct)); err != nil {
		return fmt.Errorf("perpfundingv1: send target allocation: %w", err)
	}
	return nil
}
