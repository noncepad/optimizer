// Package leveragedloopv1 is the Go-side orchestrator for leveragedloopv1
// -- Phase 2 of catscope-rust-bot's leveraged_yield_farming_plan.md: a
// single, conservative jitoSOL-collateral/USDC-debt leverage loop on
// Kamino, manual/explicit trigger only. A direct copy of package
// testperpv1's orchestration shape (same allocation/upload/handshake/
// wallet-init flow, only MODE=leveragedloopv1 selected at upload time),
// minus the target-allocation plumbing testperpv1 carries for the
// basis-trade strategy -- this mode has no use for it.
//
// It implements the brain.Brain interface and is responsible for:
//   - Allocating a slot on a Catscope/Solpipe validator pipeline (bid = 0, free tier).
//   - Uploading a WASM bot image to the validator with MODE=leveragedloopv1.
//   - Completing the handshake with the running bot instance.
//   - Sending a child keypair to the bot via stdin -- this mode's Rust
//     side (catscope-rust-bot/src/brain/leveragedloopv1) signs the real
//     Kamino deposit/borrow/withdraw/repay transactions its evaluate()
//     state machine builds.
//   - Sending TriggerOpen/TriggerClose to the running bot on request
//     (see eval.go's SendTriggerOpen/SendTriggerClose) -- nothing opens
//     or closes a real position without one of these being called
//     explicitly.
//
// Deliberately a **separate bot mode and Kamino obligation** from
// testperpv1 -- see catscope-rust-bot's src/brain/leveragedloopv1/mod.rs
// doc comment for why.
package leveragedloopv1

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
}

type Configuration struct {
	BotImage string
}

// Hook is brain.Brain plus this mode's own explicit trigger senders --
// returned instead of a bare brain.Brain so cmd/leveragedloop.go can
// call SendTriggerOpen/SendTriggerClose on the same instance it hands to
// mothership.Create (a Hook value is itself a valid brain.Brain, since
// this interface embeds it).
type Hook interface {
	brain.Brain
	SendTriggerOpen(notionalUSD float64) error
	SendTriggerOpenAuto(notionalUSD float64) error
	SendTriggerEnableBasisTrading() error
	SendTriggerCloseAllBasisPositions() error
	SendTriggerClose() error
	SendTriggerRecoverToken(mint sgo.PublicKey) error
	SendTriggerRedepositUsdc(notionalUSD float64) error
	SendTriggerTestBundler() error
	SendTriggerTestBatch() error
	SendBundlerTipUpdate(update bundler.TipUpdate) error
}

// Create creates a leveragedloopv1 gRPC event hook. db is the
// already-open prefetch.db connection (see store.DB.Raw()) -- unused by
// this mode's Go side today (no persistence needed for a one-shot
// trigger, unlike testperpv1's target allocation), kept for signature
// parity with every other bot mode's Create.
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
