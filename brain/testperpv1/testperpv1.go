// Package testperpv1 is the Go-side orchestrator for the testperpv1 bot
// mode -- a real-transaction smoke test, not a real trading strategy.
// Otherwise a direct copy of package perpfundingv1's orchestration: same
// allocation/upload/handshake/wallet-init flow, only MODE=testperpv1
// selected at upload time instead.
//
// It implements the brain.Brain interface and is responsible for:
//   - Allocating a slot on a Catscope/Solpipe validator pipeline (bid = 0, free tier).
//   - Uploading a WASM bot image to the validator with MODE=testperpv1.
//   - Completing the handshake with the running bot instance.
//   - Sending a child keypair to the bot via stdin -- unlike perpfundingv1,
//     this mode's Rust side (catscope-rust-bot/src/brain/testperpv1) does
//     use it, to sign the real Solend/Kamino deposit/withdraw
//     transactions its evaluate() state machine builds.
//
// The WASM bot (catscope-rust-bot/src/brain/testperpv1) runs the real
// bootstrap -> deposit -> withdraw sequence for Solend then Kamino --
// see that module's doc comment for why it exists and what it proves.
package testperpv1

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
	// db is the shared prefetch.db connection, used by
	// SendTargetAllocation to persist the latest target portfolio
	// allocation alongside pushing it to the running bot -- the same
	// database build.rs reads at compile time for the bot's default.
	db *sql.DB
	// didBootTransfer gates the one-time parent->child SOL transfer in
	// Evaluate (see eval.go) -- Evaluate fires on every solpipe state
	// update, not just once, so this is what stops it from resending the
	// transfer repeatedly.
	didBootTransfer bool
}

type Configuration struct {
	BotImage string
}

// Hook is brain.Brain plus SendBundlerTipUpdate/SendTriggerTestAstralane --
// returned instead of a bare brain.Brain so cmd/*.go can call them on the
// same instance it hands to mothership.Create (a Hook value is itself a
// valid brain.Brain, since this interface embeds it). Mirrors
// leveragedloopv1.Hook's own reasoning for its trigger-sender methods.
type Hook interface {
	brain.Brain
	SendBundlerTipUpdate(update bundler.TipUpdate) error
	SendTriggerTestAstralane() error
}

// Create creates a testperpv1 gRPC event hook. db is the already-open
// prefetch.db connection (see store.DB.Raw()), used to persist target
// allocation updates sent via SendTargetAllocation.
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

// downloadDefaultImage tbd
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
