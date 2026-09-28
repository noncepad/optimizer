// Package marketwatchv1 is the Go-side orchestrator for the marketwatchv1
// bot mode -- forked from optimizer/brain/arbv1, stripped of every
// trading-specific piece (no wallet, no latency-file, no bundler tip
// wiring). Where arbv1 keeps a real trading bot alive, funded, and
// supplied with configuration data, this package's only job is to keep a
// read-only market-statistics bot alive and feed its live
// volatility/correlation snapshots into a Store the TUI dashboard
// (cmd/marketwatch.go) reads from.
//
// It implements the brain.Brain interface and is responsible for:
//   - Allocating a slot on a Catscope/Solpipe validator pipeline (bid = 0, free tier).
//   - Uploading a WASM bot image to the validator with MODE=marketwatchv1.
//   - Completing the handshake with the running bot instance.
//   - Reading MarketStats messages from the bot's stdout and recording
//     them into a Store (see store.go) the TUI polls.
//
// The WASM bot (catscope-rust-bot/src/brain/marketwatchv1) does the
// actual live pool-price tracking and EWMA volatility/correlation math.
package marketwatchv1

import (
	"context"
	"fmt"
	"log/slog"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/solana-foundation/solana-go/v2"
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
	handshake   *mgrbot.Handshake
	instance    *mgrbot.Bot
	config      *Configuration
	store       *Store
}

type Configuration struct {
	BotImage string
}

// Hook is brain.Brain plus Store -- returned instead of a bare
// brain.Brain so cmd/marketwatch.go's TUI can read live stats off the
// same instance it hands to mothership.Create. Mirrors arbv1.Hook's own
// reasoning for its extra SendBundlerTipUpdate method.
type Hook interface {
	brain.Brain
	// Store is safe to call immediately after Create -- it never blocks
	// on the bot actually connecting; Store.Latest simply returns
	// (zero-value, false) until the bot's first push arrives.
	Store() *Store
}

// Create creates a marketwatchv1 gRPC event hook.
func Create(ctx context.Context, cancel context.CancelCauseFunc, parentKey sgo.PrivateKey, config *Configuration) (Hook, error) {
	entry := util.LoggerBrainSimple.Fields(logger.FromContext(ctx))
	return &eventHook{
		ctx:       logger.ToContext(ctx, entry),
		cancel:    cancel,
		logger:    entry,
		parentKey: parentKey,
		config:    config,
		store:     newStore(),
	}, nil
}

func (hs *eventHook) Store() *Store {
	return hs.store
}

func (hs *eventHook) useLocalImage(ctx context.Context, fp string, mEnv map[string]string) (mgrbot.Image, error) {
	image, err := mgrbot.Load(ctx, hs.parentKey, hs.botMarketID, fp, hs.addressBook, hs.builder, [2]int{1, 2}, mEnv)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("failed to load image: %s", err)
	}
	return image, nil
}

// downloadDefaultImage downloads the demonstrator bot image instead of
// compiling one locally -- same fallback arbv1.eventHook.downloadDefaultImage
// uses, though in practice marketwatchv1 has no reason to prefer the
// generic demonstrator over a locally compiled image with this bot's own
// MODE; kept for parity/consistency with every other bot-mode package in
// this codebase.
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
