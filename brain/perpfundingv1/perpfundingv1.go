// Package perpfundingv1 is the Go-side orchestrator for the perpfundingv1
// bot mode (inter-venue perpetual-futures funding-rate observation,
// Phoenix vs. Velocity/Drift).
//
// It implements the brain.Brain interface and is responsible for:
//   - Allocating a slot on a Catscope/Solpipe validator pipeline (bid = 0, free tier).
//   - Uploading a WASM bot image to the validator with MODE=perpfundingv1.
//   - Completing the handshake with the running bot instance.
//   - Sending a child keypair to the bot via stdin, and (see eval.go's
//     Evaluate) funding it with a one-time SOL transfer from the parent
//     fee-payer -- the Rust side's spot-market execution and Solend/
//     Kamino obligation bootstrap sign real transactions with this key.
//
// The WASM bot (catscope-rust-bot/src/brain/perpfundingv1) does the
// actual funding-rate comparison (trader::perp_router::PerpRouter) and
// its own Solend/Kamino basis-trade execution -- there is no latency
// reporting or ALT wiring here, unlike arbv1's Go package, since nothing
// on the Rust side needs it.
package perpfundingv1

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

// Hook is brain.Brain plus SendBundlerTipUpdate -- returned instead of a
// bare brain.Brain so cmd/*.go can call SendBundlerTipUpdate on the same
// instance it hands to mothership.Create (a Hook value is itself a valid
// brain.Brain, since this interface embeds it). Mirrors
// leveragedloopv1.Hook's own reasoning for its trigger-sender methods.
type Hook interface {
	brain.Brain
	SendBundlerTipUpdate(update bundler.TipUpdate) error
}

// Create creates a perpfundingv1 gRPC event hook. db is the already-open
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
