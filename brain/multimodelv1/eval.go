package multimodelv1

import (
	"context"
	"errors"
	"fmt"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/optimizer/bundler"
	"git.noncepad.com/pkg/solpipe-util/common"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

func (hs *eventHook) initWallet() error {
	hs.childKey = common.DeriveChildKeyFromIndex(hs.parentKey, 1)
	// The child key is what the Rust side subscribes its own wallet
	// balances against -- same subscribe pattern every other mode uses
	// so the WASM bot can see its own wallet's token/SOL balances. It
	// signs/sends no real transaction yet (sub-phase 5a is idle-only).
	_ = hs.graph.Subscribe(hs.ctx, hs.childKey.PublicKey(), graph.WeightAll, 2)
	// See testperpv1/leveragedloopv1's initWallet doc comment for why the
	// parent must also be subscribed (Evaluate's boot transfer below
	// reads its balance via solpipeState.System, which only returns real
	// data for a subscribed pubkey).
	_ = hs.graph.Subscribe(hs.ctx, hs.parentKey.PublicKey(), graph.WeightAll, 2)
	// See position.go's subscribeObligations doc comment -- gives
	// evaluatePositions (called from Evaluate below) a live, in-memory
	// view of the child's real Solend/Kamino obligation state, same as
	// the wallet subscribe above already does for token/SOL balances.
	hs.subscribeObligations()
	hs.builder.AppendKey(hs.parentKey)
	hs.builder.AppendKey(hs.childKey)
	return hs.instance.CustomStdin(DoWallet(hs.childKey))
}

// targetParentRemainingLamports is 0.08 SOL -- same value/reasoning as
// every other mode's constant of the same name. The child starts at zero
// balance and needs real SOL to pay its own transaction fees and any
// account rent once sub-phase 5b+ actually sends transactions -- nothing
// spends it yet (sub-phase 5a is idle-only), but funding the child at
// boot, not at first-trigger time, matches every other mode's own
// pattern.
const targetParentRemainingLamports = 80_000_000

// Evaluate fires on every solpipe state update. Runs the one-time boot
// transfer (didBootTransfer-gated, see evaluateBootTransfer) and, on
// every call regardless, evaluatePositions -- see position.go -- so this
// bot's own real token balances and Solend/Kamino obligation state stay
// visible here as they change, not just at boot.
func (hs *eventHook) Evaluate(solpipeState brain.SolpipeState, bidderState brain.BidderState) error {
	if !hs.didBootTransfer {
		if err := hs.evaluateBootTransfer(solpipeState); err != nil {
			return err
		}
	}
	hs.evaluatePositions(solpipeState)
	return nil
}

// evaluateBootTransfer is Evaluate's one-time parent->child SOL transfer
// -- same shape as every other mode's Evaluate -- see
// leveragedloopv1/eval.go for the full explanation of the
// didBootTransfer/parentSOL==0 handling. Split out of Evaluate so the
// per-call position visibility below (evaluatePositions) keeps running
// after this fires once.
func (hs *eventHook) evaluateBootTransfer(solpipeState brain.SolpipeState) error {
	parentSOL := solpipeState.System(hs.parentKey.PublicKey())
	if parentSOL == 0 {
		return nil
	}
	hs.didBootTransfer = true
	if parentSOL <= targetParentRemainingLamports {
		hs.logger.Info(fmt.Sprintf(
			"multimodelv1: boot transfer skipped -- parent balance %d lamports already at or below the %d lamport target",
			parentSOL, targetParentRemainingLamports,
		))
		return nil
	}
	transferLamports := parentSOL - targetParentRemainingLamports
	ctx, cancel := context.WithTimeout(hs.ctx, 60*time.Second)
	helper, err := hs.builder.Helper(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("multimodelv1: boot transfer: failed to create helper: %s", err)
	}
	helper.TransferSOL(hs.parentKey.PublicKey(), hs.childKey.PublicKey(), transferLamports)
	go func() {
		defer cancel()
		sig, slot, err := helper.FinishTx()
		if err != nil {
			hs.logger.Info(fmt.Sprintf("multimodelv1: boot transfer failed: %s", err))
			return
		}
		hs.logger.Info(fmt.Sprintf(
			"multimodelv1: boot transfer sent %d lamports parent->child (leaving ~%d lamports in parent): %s @ slot %d",
			transferLamports, targetParentRemainingLamports, sig, slot,
		))
	}()
	return nil
}

// ErrBotNotConnectedYet is returned by SendBundlerTipUpdate below when
// hs.instance hasn't been set yet -- see nilInstance's doc comment on
// every other mode's identically-named sentinel for the real,
// live-confirmed reliability gap this guards against.
var ErrBotNotConnectedYet = errors.New("multimodelv1: bot not connected yet")

// nilInstance reports whether the bot's `instance` handle is still nil --
// same real gap every other bot mode's identically-named method guards
// (see leveragedloopv1/eval.go's doc comment for the live-confirmed
// SIGSEGV incident this class of check prevents).
func (hs *eventHook) nilInstance() bool {
	return hs.instance == nil
}

// SendBundlerTipUpdate pushes a live bundler tip update to the running
// bot -- see bundler.RunTipBroadcaster, the periodic poller that calls
// this. A not-yet-connected bot is expected and not logged as an error:
// RunTipBroadcaster just retries on its own next tick.
func (hs *eventHook) SendBundlerTipUpdate(update bundler.TipUpdate) error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(bundler.DoBundlerTipUpdate(update)); err != nil {
		return fmt.Errorf("multimodelv1: send bundler tip update: %w", err)
	}
	return nil
}

// SendTriggerEnableFactorLogging pushes a real TriggerEnableFactorLogging
// to the already-running bot -- Phase 5 sub-phase 5b's one-time opt-in
// for a real-but-read-only periodic factor-graph resync/logging cycle
// (see catscope-rust-bot's src/brain/multimodelv1/state.rs's
// run_factor_resync). Opens/closes nothing -- the same
// "nothing new happens without an explicit trigger" ethos every other
// real bot mode's own triggers follow.
func (hs *eventHook) SendTriggerEnableFactorLogging() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerEnableFactorLogging()); err != nil {
		return fmt.Errorf("multimodelv1: send trigger enable factor logging: %w", err)
	}
	return nil
}

// SendTriggerEnablePairTrading pushes a real TriggerEnablePairTrading to
// the already-running bot -- Phase 5 sub-phase 5c's one-time opt-in for
// the REAL, executing pure-Kamino pair/stat-arb trade (see
// catscope-rust-bot's src/brain/multimodelv1/state.rs's
// run_pair_trade_cycle). Unlike SendTriggerEnableFactorLogging, this one
// sends real transactions once a real candidate clears the real gates.
func (hs *eventHook) SendTriggerEnablePairTrading() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerEnablePairTrading()); err != nil {
		return fmt.Errorf("multimodelv1: send trigger enable pair trading: %w", err)
	}
	return nil
}

// SendTriggerEnableDirectionalTrading pushes a real
// TriggerEnableDirectionalTrading carrying the human-specified long
// target to the already-running bot -- trade type 1's own one-time
// opt-in (see catscope-rust-bot's src/brain/multimodelv1/state.rs's
// run_directional_trade_cycle). Unlike SendTriggerEnableFactorLogging/
// SendTriggerEnablePairTrading, this one carries a real payload (the
// target mint), since entry itself is a human decision, not an automated
// signal.
func (hs *eventHook) SendTriggerEnableDirectionalTrading(mint sgo.PublicKey) error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerEnableDirectionalTrading(mint)); err != nil {
		return fmt.Errorf("multimodelv1: send trigger enable directional trading: %w", err)
	}
	return nil
}

// SendTriggerCloseDirectionalPosition pushes a real
// TriggerCloseDirectionalPosition -- the human "I'm satisfied, take
// profit" half of trade type 1's otherwise-automatic close-pass (stop
// -loss/borrow-gate closes need no human input).
func (hs *eventHook) SendTriggerCloseDirectionalPosition() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerCloseDirectionalPosition()); err != nil {
		return fmt.Errorf("multimodelv1: send trigger close directional position: %w", err)
	}
	return nil
}

// SendTriggerEnableDispersionTrading arms trade type 3's (dispersion)
// automated decision loop -- unlike SendTriggerEnableDirectionalTrading,
// this carries no payload (see catscope-rust-bot's
// src/brain/multimodelv1/state.rs's run_dispersion_trade_cycle): entry
// itself is a real, computed signal, not a human-specified target.
func (hs *eventHook) SendTriggerEnableDispersionTrading() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerEnableDispersionTrading()); err != nil {
		return fmt.Errorf("multimodelv1: send trigger enable dispersion trading: %w", err)
	}
	return nil
}

// SendTriggerCloseDispersionPosition pushes a real
// TriggerCloseDispersionPosition -- the human "I'm satisfied, take
// profit" override for trade type 3's otherwise-automatic exit signal.
func (hs *eventHook) SendTriggerCloseDispersionPosition() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerCloseDispersionPosition()); err != nil {
		return fmt.Errorf("multimodelv1: send trigger close dispersion position: %w", err)
	}
	return nil
}

// SendTriggerEnableHawkesTrading arms trade type 5's (Hawkes-on
// -eigenfactor momentum) automated decision loop -- unlike
// SendTriggerEnableDirectionalTrading, this carries no payload (see
// catscope-rust-bot's src/brain/multimodelv1/state.rs's
// run_hawkes_trade_cycle): entry itself is a real, computed signal, not
// a human-specified target.
func (hs *eventHook) SendTriggerEnableHawkesTrading() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerEnableHawkesTrading()); err != nil {
		return fmt.Errorf("multimodelv1: send trigger enable hawkes trading: %w", err)
	}
	return nil
}

// SendTriggerCloseHawkesPosition pushes a real TriggerCloseHawkesPosition
// -- the human "I'm satisfied, take profit" override for trade type 5's
// otherwise-automatic close-pass (intensity decay, max-holding-cycles
// cap, borrow-gate re-check).
func (hs *eventHook) SendTriggerCloseHawkesPosition() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerCloseHawkesPosition()); err != nil {
		return fmt.Errorf("multimodelv1: send trigger close hawkes position: %w", err)
	}
	return nil
}

// SendReplayResidualSnapshot pushes one previously persisted mint's
// residual/z-score snapshot to the freshly connected bot -- see
// cmd/multimodel.go's boot-time pushResidualSnapshots (plural: called
// once per persisted mint), and catscope-rust-bot's state.rs's
// apply_residual_snapshot for why this must arrive before
// SendTriggerEnablePairTrading's first real resync to actually take
// effect.
func (hs *eventHook) SendReplayResidualSnapshot(payload []byte) error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoReplayResidualSnapshot(payload)); err != nil {
		return fmt.Errorf("multimodelv1: send replay residual snapshot: %w", err)
	}
	return nil
}

// SendTriggerSweepMint pushes a real TriggerSweepMint to the already
// -running bot -- temporary, standalone manual-cleanup tool, see
// KeyFlagTriggerSweepMint's doc comment.
func (hs *eventHook) SendTriggerSweepMint(mint, destMint sgo.PublicKey) error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerSweepMint(mint, destMint)); err != nil {
		return fmt.Errorf("multimodelv1: send trigger sweep mint: %w", err)
	}
	return nil
}
