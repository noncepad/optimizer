// Package arbv1 is the Go-side orchestrator for the arbv1 (arbitrage v1) bot mode.
//
// It implements the brain.Brain interface and is responsible for:
//   - Allocating a slot on a Catscope/Solpipe validator pipeline (bid = 0, free tier).
//   - Uploading a WASM bot image to the validator with MODE=arbv1.
//   - Completing the handshake with the running bot instance.
//   - Sending the child trading keypair and address-lookup-table (ALT) data to the
//     bot via stdin so the bot can build and sign arbitrage transactions.
//   - Reading LatencyReportV1 structs from the bot's stdout and writing them to a
//     latency log file for performance monitoring.
//
// The WASM bot (catscope-rust-bot/src/brain/arbv1) does the actual DEX price graph
// construction and Bellman-Ford arbitrage detection. This Go brain's job is to keep
// the bot alive, funded, and supplied with configuration data.
package arbv1

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"

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
	wallet      *walletInfo
	config      *Configuration
	state       *pendingState
	latencyFile io.Writer
	// db is the shared prefetch.db connection, used to persist the bot's
	// periodic account-usage reports (KeyFlagCommonAccountUsage) into
	// account_usage -- see optimizer/prefetch/alt.
	db *sql.DB
}
type Configuration struct {
	BotImage        string
	LatencyFilePath string
}

// Hook is brain.Brain plus SendBundlerTipUpdate -- returned instead of a
// bare brain.Brain so cmd/*.go can call SendBundlerTipUpdate on the same
// instance it hands to mothership.Create (a Hook value is itself a valid
// brain.Brain, since this interface embeds it). Mirrors
// testperpv1.Hook's own reasoning for its trigger-sender methods.
type Hook interface {
	brain.Brain
	SendBundlerTipUpdate(update bundler.TipUpdate) error
}

// Create creates a helloworld gRPC event hook.
func Create(ctx context.Context, cancel context.CancelCauseFunc, parentKey sgo.PrivateKey, config *Configuration, db *sql.DB) (Hook, error) {
	entry := util.LoggerBrainSimple.Fields(logger.FromContext(ctx))
	var writer io.Writer
	var err error
	if 0 < len(config.LatencyFilePath) {
		var f *os.File
		f, err = os.Create(config.LatencyFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to open latency log path")
		}
		writer = bufio.NewWriter(f)
	}
	return &eventHook{
		ctx:         logger.ToContext(ctx, entry),
		cancel:      cancel,
		logger:      entry,
		parentKey:   parentKey,
		wallet:      createWallet(),
		config:      config,
		state:       createPendingState(),
		latencyFile: writer,
		db:          db,
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
