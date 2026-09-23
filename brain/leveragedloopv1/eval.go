package leveragedloopv1

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
	// The child key is what the Rust side actually signs Kamino
	// deposit/borrow/withdraw/repay transactions with -- same subscribe
	// pattern testperpv1/arbv1 already use so the WASM bot can see its
	// own wallet's token/SOL balances.
	_ = hs.graph.Subscribe(hs.ctx, hs.childKey.PublicKey(), graph.WeightAll, 2)
	// See testperpv1's initWallet doc comment for why the parent must
	// also be subscribed (Evaluate's boot transfer below reads its
	// balance via solpipeState.System, which only returns real data for
	// a subscribed pubkey).
	_ = hs.graph.Subscribe(hs.ctx, hs.parentKey.PublicKey(), graph.WeightAll, 2)
	hs.builder.AppendKey(hs.parentKey)
	hs.builder.AppendKey(hs.childKey)
	return hs.instance.CustomStdin(DoWallet(hs.childKey))
}

// targetParentRemainingLamports is 0.08 SOL -- same value/reasoning as
// testperpv1's own constant of the same name. The child starts at zero
// balance and needs real SOL to pay its own transaction fees and the
// rent for the Kamino accounts this mode's Rust-side state machine
// creates (obligation, user_metadata, farmer PDAs) -- nothing else
// funds it. This mode does NOT fund the child with USDC the way
// testperpv1 does (no SwapToUsdc-equivalent phase) -- the operator is
// expected to ensure the child wallet already holds the USDC a
// TriggerOpen will need before sending one; see this package's doc
// comment.
const targetParentRemainingLamports = 80_000_000

// Evaluate fires on every solpipe state update. Same one-time boot
// transfer as testperpv1's Evaluate -- see that file for the full
// explanation of the didBootTransfer/parentSOL==0 handling.
func (hs *eventHook) Evaluate(solpipeState brain.SolpipeState, bidderState brain.BidderState) error {
	if hs.didBootTransfer {
		return nil
	}
	parentSOL := solpipeState.System(hs.parentKey.PublicKey())
	if parentSOL == 0 {
		return nil
	}
	hs.didBootTransfer = true
	if parentSOL <= targetParentRemainingLamports {
		hs.logger.Info(fmt.Sprintf(
			"leveragedloopv1: boot transfer skipped -- parent balance %d lamports already at or below the %d lamport target",
			parentSOL, targetParentRemainingLamports,
		))
		return nil
	}
	transferLamports := parentSOL - targetParentRemainingLamports
	ctx, cancel := context.WithTimeout(hs.ctx, 60*time.Second)
	helper, err := hs.builder.Helper(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("leveragedloopv1: boot transfer: failed to create helper: %s", err)
	}
	helper.TransferSOL(hs.parentKey.PublicKey(), hs.childKey.PublicKey(), transferLamports)
	go func() {
		defer cancel()
		sig, slot, err := helper.FinishTx()
		if err != nil {
			hs.logger.Info(fmt.Sprintf("leveragedloopv1: boot transfer failed: %s", err))
			return
		}
		hs.logger.Info(fmt.Sprintf(
			"leveragedloopv1: boot transfer sent %d lamports parent->child (leaving ~%d lamports in parent): %s @ slot %d",
			transferLamports, targetParentRemainingLamports, sig, slot,
		))
	}()
	return nil
}

// ErrBotNotConnectedYet is returned by the Send* trigger methods below
// when `hs.instance` hasn't been set yet -- see their shared doc comment
// on `nilInstance` for why this check exists and how callers should
// react to it.
var ErrBotNotConnectedYet = errors.New("leveragedloopv1: bot not connected yet")

// nilInstance reports whether the bot's `instance` handle is still nil --
// real, live-confirmed gap (2026-08-27): `hs.instance` is only set once
// `init.go`'s handshake flow completes (after a real `botImage.Upload`
// succeeds); a real pipeline reconnect can take up to ~5 minutes to
// self-recover (same known Solpipe-allocation quirk documented in
// testperpv1/leveragedloopv1's other doc comments), and the trigger-send
// goroutines in cmd/leveragedloop.go fire on a fixed delay regardless of
// whether that handshake has actually finished. Before this check, a
// trigger arriving during that window dereferenced a nil `*mgrbot.Bot`
// and crashed the whole process with a SIGSEGV -- observed for real: a
// `--open 50` retry hit "upload failed err=EOF" then panicked on
// `SendTriggerOpen` ~17s later when the 20s trigger-at timer fired,
// before the reconnect had finished. No transaction was ever sent in
// that case (the crash happened before `CustomStdin` could run), so this
// is a pure reliability fix, not a real-money one.
func (hs *eventHook) nilInstance() bool {
	return hs.instance == nil
}

// SendTriggerOpen pushes a real TriggerOpen to the already-running bot
// -- the only way a real leveraged position ever opens (see this
// package's doc comment: manual/explicit trigger only, nothing here
// fires automatically). notionalUSD is the starting USD notional for
// the single loop step; the caller decides it, this function does not.
func (hs *eventHook) SendTriggerOpen(notionalUSD float64) error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerOpen(notionalUSD)); err != nil {
		return fmt.Errorf("leveragedloopv1: send trigger open: %w", err)
	}
	return nil
}

// SendTriggerOpenAuto pushes a real TriggerOpenAuto to the already-running
// bot -- same notional-USD payload as SendTriggerOpen, but the bot picks
// the LST candidate itself via its own Time-Expanded DAG instead of always
// jitoSOL.
func (hs *eventHook) SendTriggerOpenAuto(notionalUSD float64) error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerOpenAuto(notionalUSD)); err != nil {
		return fmt.Errorf("leveragedloopv1: send trigger open auto: %w", err)
	}
	return nil
}

// SendTriggerEnableBasisTrading pushes a real TriggerEnableBasisTrading to
// the already-running bot -- one-time opt-in for the Phoenix-perp-funding-
// vs-Kamino-rate basis trade, a second strategy fully independent of the
// jitoSOL leverage loop. Runs autonomously every real funding epoch once
// this has fired.
func (hs *eventHook) SendTriggerEnableBasisTrading() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerEnableBasisTrading()); err != nil {
		return fmt.Errorf("leveragedloopv1: send trigger enable basis trading: %w", err)
	}
	return nil
}

// SendTriggerCloseAllBasisPositions pushes a real
// TriggerCloseAllBasisPositions to the already-running bot -- force-closes
// every currently-open basis position, independent of the cycle's own
// logic. Manual safety valve.
func (hs *eventHook) SendTriggerCloseAllBasisPositions() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerCloseAllBasisPositions()); err != nil {
		return fmt.Errorf("leveragedloopv1: send trigger close all basis positions: %w", err)
	}
	return nil
}

// SendTriggerClose pushes a real TriggerClose to the already-running
// bot, starting the deleverage/unwind sequence.
func (hs *eventHook) SendTriggerClose() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerClose()); err != nil {
		return fmt.Errorf("leveragedloopv1: send trigger close: %w", err)
	}
	return nil
}

// SendTriggerRecoverToken pushes a real TriggerRecoverToken to the
// already-running bot -- a one-shot recovery action, independent of the
// loop's phase, that swaps the wallet's entire real balance of mint back
// to USDC. See DoTriggerRecoverToken's doc comment for why this exists.
func (hs *eventHook) SendTriggerRecoverToken(mint sgo.PublicKey) error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerRecoverToken(mint)); err != nil {
		return fmt.Errorf("leveragedloopv1: send trigger recover token: %w", err)
	}
	return nil
}

// SendTriggerRedepositUsdc pushes a real TriggerRedepositUsdc to the
// already-running bot -- a one-shot action, independent of the loop's
// phase, that swaps notionalUSD of USDC to jitoSOL and deposits it as
// additional Kamino collateral. See DoTriggerRedepositUsdc's doc comment
// for why this exists.
func (hs *eventHook) SendTriggerRedepositUsdc(notionalUSD float64) error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerRedepositUsdc(notionalUSD)); err != nil {
		return fmt.Errorf("leveragedloopv1: send trigger redeposit usdc: %w", err)
	}
	return nil
}

// SendTriggerTestBundler sends a real, one-shot request that proves the
// Astralane dual-transaction bundler pipeline end-to-end with a trivial,
// inert instruction -- see DoTriggerTestBundler's doc comment.
func (hs *eventHook) SendTriggerTestBundler() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerTestBundler()); err != nil {
		return fmt.Errorf("leveragedloopv1: send trigger test bundler: %w", err)
	}
	return nil
}

// SendTriggerTestBatch sends a real, one-shot request that forces the
// generic transactionprocessor::batch host import with two separate,
// deliberately inert transactions -- see DoTriggerTestBatch's doc
// comment.
func (hs *eventHook) SendTriggerTestBatch() error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(DoTriggerTestBatch()); err != nil {
		return fmt.Errorf("leveragedloopv1: send trigger test batch: %w", err)
	}
	return nil
}

// SendBundlerTipUpdate pushes a live bundler tip update to the running
// bot -- see bundler.RunTipBroadcaster, the periodic poller that calls
// this. Unlike the SendTrigger* methods above, a not-yet-connected bot is
// expected and not logged as an error: RunTipBroadcaster just retries on
// its own next tick.
func (hs *eventHook) SendBundlerTipUpdate(update bundler.TipUpdate) error {
	if hs.nilInstance() {
		return ErrBotNotConnectedYet
	}
	if err := hs.instance.CustomStdin(bundler.DoBundlerTipUpdate(update)); err != nil {
		return fmt.Errorf("leveragedloopv1: send bundler tip update: %w", err)
	}
	return nil
}
