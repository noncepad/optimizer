// Package multimodelv1 is the Go-side orchestrator for multimodelv1 --
// catscope-rust-bot's src/brain/multimodelv1/PLAN-1.md Phase 5. A direct
// copy of package leveragedloopv1's orchestration shape (same
// allocation/upload/handshake/wallet-init flow, only MODE=multimodelv1
// selected at upload time).
//
// It implements the brain.Brain interface and is responsible for:
//   - Allocating a slot on a Catscope/Solpipe validator pipeline (bid = 0, free tier).
//   - Uploading a WASM bot image to the validator with MODE=multimodelv1.
//   - Completing the handshake with the running bot instance.
//   - Sending a child keypair to the bot via stdin -- this mode's Rust
//     side (catscope-rust-bot/src/brain/multimodelv1) uses it to
//     subscribe to its own wallet balances; it does not sign or send any
//     real transaction yet.
//   - Sending TriggerEnableFactorLogging on request (sub-phase 5b) --
//     opts the bot into a real-but-read-only periodic factor-graph
//     resync/logging cycle; opens/closes nothing.
//
// Sub-phase 5a shipped with zero SendTrigger* methods on Hook (nothing
// to trigger yet); sub-phase 5b adds the first one,
// SendTriggerEnableFactorLogging. Add more alongside the corresponding
// Rust-side CustomMessageInbound variant as PLAN-1.md's later sub-phases
// land real decision/execution logic.
package multimodelv1

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/optimizer/bundler"
	"git.noncepad.com/pkg/optimizer/prefetch/obligation"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

type eventHook struct {
	ctx         context.Context
	cancel      context.CancelCauseFunc
	logger      *slog.Logger
	graph       graph.Graph
	bidmgr      *bidder.BidderManager
	addressBook common.BotClientDialer
	builder     *txbuilder.BuildManager
	authorizer  sgo.PublicKey
	botMarketID sgo.PublicKey
	parentKey   sgo.PrivateKey
	childKey    sgo.PrivateKey
	handshake   *mgrbot.Handshake
	instance    *mgrbot.Bot
	config      *Configuration
	db          *sql.DB
	// didBootTransfer gates the one-time parent->child SOL transfer in
	// Evaluate (see eval.go) -- Evaluate fires on every solpipe state
	// update, not just once, so this is what stops it from resending the
	// transfer repeatedly.
	didBootTransfer bool
	// lastTokenSnapshot/lastObligationSnapshot are evaluatePositions' own
	// change-detection state (see position.go) -- Evaluate fires on every
	// solpipe state update, so these are what stop it from logging the
	// same unchanged balances/obligations every single call.
	lastTokenSnapshot      map[sgo.PublicKey]uint64
	lastObligationSnapshot map[string]*obligation.Parsed
}

type Configuration struct {
	BotImage string
}

// Hook is brain.Brain plus the cross-strategy sender every real bot
// mode's Go package carries (SendBundlerTipUpdate, wired to
// startBundlerTipBroadcaster the same way every other mode's cmd file
// does it) plus Phase 5's real triggers: SendTriggerEnableFactorLogging
// (sub-phase 5b, read-only) and SendTriggerEnablePairTrading (sub-phase
// 5c, REAL execution -- real Kamino deposits/borrows, real spot swaps,
// once a real candidate clears the real z-score and borrow-cost gates).
type Hook interface {
	brain.Brain
	SendBundlerTipUpdate(update bundler.TipUpdate) error
	SendTriggerEnableFactorLogging() error
	SendTriggerEnablePairTrading() error
	// SendTriggerEnableDirectionalTrading carries the human-specified
	// long target for trade type 1 (directional factor-neutral) -- real
	// execution once a real hedge basket clears the real borrow gates,
	// same real-money caveat as SendTriggerEnablePairTrading.
	SendTriggerEnableDirectionalTrading(mint sgo.PublicKey) error
	// SendTriggerCloseDirectionalPosition requests an explicit close of
	// whatever directional position is currently open (or pending open)
	// -- the human "I'm satisfied, take profit" half trade type 1's
	// otherwise-automatic close-pass doesn't have.
	SendTriggerCloseDirectionalPosition() error
	// SendTriggerEnableDispersionTrading arms trade type 3 (dispersion) --
	// unlike SendTriggerEnableDirectionalTrading, entry itself is
	// automated (a real, computed idiosyncratic-volatility signal), so
	// this carries no target; real execution (real spot buys, a real
	// Phoenix short) begins once the real signal clears on its own.
	SendTriggerEnableDispersionTrading() error
	// SendTriggerCloseDispersionPosition requests an explicit close of
	// whatever dispersion position is currently open -- same human
	// override role as SendTriggerCloseDirectionalPosition.
	SendTriggerCloseDispersionPosition() error
	// SendTriggerEnableHawkesTrading arms trade type 5 (Hawkes-on
	// -eigenfactor momentum, see docs/HAWKES_FACTOR_TRADE_PLAN.md) --
	// same no-target shape as SendTriggerEnableDispersionTrading: entry
	// itself is a real, computed signal (a discrete-time self-exciting
	// jump intensity crossing its own threshold), not a human-specified
	// target.
	SendTriggerEnableHawkesTrading() error
	// SendTriggerCloseHawkesPosition requests an explicit close of
	// whatever Hawkes momentum basket is currently open -- same human
	// override role as SendTriggerCloseDispersionPosition.
	SendTriggerCloseHawkesPosition() error
	// SendReplayResidualSnapshot pushes one persisted mint's residual/
	// z-score warm-up snapshot back to a freshly connected bot -- see
	// cmd/multimodel.go's boot-time pushResidualSnapshots, which calls
	// this once per mint returned by
	// prefetch/multimodel.LoadResidualSnapshots.
	SendReplayResidualSnapshot(payload []byte) error
	// SendTriggerSweepMint is a temporary, standalone manual-cleanup tool
	// (2026-09-03) -- not tied to any of the four trade types' own
	// gates. Sweeps the bot's entire real balance of mint to destMint.
	// See KeyFlagTriggerSweepMint's doc comment for the real incident
	// this exists to clean up and why destMint isn't hardcoded to
	// mint_usdc.
	SendTriggerSweepMint(mint, destMint sgo.PublicKey) error
}

// Create creates a multimodelv1 gRPC event hook. db is the already-open
// prefetch.db connection (see store.DB.Raw()) -- used by instance.go's
// loopInstance to persist real residual/z-score snapshots
// (prefetch/multimodel.SaveResidualSnapshot) and by cmd/multimodel.go's
// boot-time pushResidualSnapshots to replay them back. Phase 4's own
// Go-side persistence (the OpenIntentReport handler / boot-time
// ReplayOpenIntent replay) is a separate, still-unbuilt concern -- see
// that mechanism's own doc comments.
func Create(ctx context.Context, cancel context.CancelCauseFunc, parentKey sgo.PrivateKey, config *Configuration, db *sql.DB) (Hook, error) {
	entry := util.LoggerBrainSimple.Fields(logger.FromContext(ctx))
	return &eventHook{
		ctx:       logger.ToContext(ctx, entry),
		cancel:    cancel,
		logger:    entry,
		parentKey: parentKey,
		config:    config,
		db:        db,
	}, nil
}

func (hs *eventHook) useLocalImage(ctx context.Context, fp string, mEnv map[string]string) (mgrbot.Image, error) {
	image, err := mgrbot.Load(ctx, hs.parentKey, hs.botMarketID, fp, hs.addressBook, hs.builder, [2]int{1, 2}, mEnv)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("failed to load image: %s", err)
	}
	return image, nil
}

func (hs *eventHook) downloadDefaultImage(ctx context.Context) (mgrbot.Image, error) {
	fp, err := util.DownloadCatscopeRustBotDemonstrator(ctx)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("bot download failed: %s", err)
	}
	image, err := mgrbot.Load(ctx, hs.parentKey, hs.botMarketID, fp, hs.addressBook, hs.builder, [2]int{1, 2}, nil)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("failed to load image: %s", err)
	}
	return image, nil
}
