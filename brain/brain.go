// Package brain is a generic, mode-agnostic bot-orchestration layer: it
// allocates a slot on a Catscope/Solpipe validator pipeline, then uploads
// whatever WASM bot images are requested of it over time (via Request),
// handing back a Bot object per upload -- the running instance's handshake,
// an on-disk log file of its stderr output, and plain Go channels for its
// stdin/stdout messaging.
//
// This package deliberately knows nothing about any specific trading
// strategy's wire protocol (message key flags, wallet funding, latency
// reports, etc.) -- that belongs to whatever code drives a given Bot's
// SendC/RecvC. See brain/arbv1, brain/testperpv1, and siblings for that.
package brain

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// Hook is brain.Brain (what mothership.Create needs to drive this as a real
// bot-mode) plus Request/Bots -- the two ways callers actually drive uploads
// and observe what's running.
type Hook interface {
	brain.Brain
	// Request submits req to the background upload dispatcher Init
	// starts. Non-blocking as long as the dispatcher isn't badly
	// backlogged (the request channel is buffered) -- req.ResultC
	// receives exactly one *UploadResult once the upload finishes
	// (success or failure). If req.ResultC is nil, Request allocates
	// one (buffered by 1) before submitting.
	Request(req *UploadRequest)
	// Bots returns a snapshot of every bot this Hook has successfully
	// uploaded and not yet closed, keyed by the pipeline it's running
	// on. Safe to call concurrently with Request/the dispatcher.
	Bots() map[sgo.PublicKey]*Bot
}

type Configuration struct {
	// LogDir is where each uploaded bot's stderr log file is created --
	// one file per upload, named "<mode>-<pipeline>-<unixnano>.log".
	// Defaults to os.TempDir() if empty.
	LogDir string
}

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
	config      *Configuration
	// defaultPipeline is the pipeline Init allocates against -- used by
	// any UploadRequest that doesn't specify its own Pipeline.
	defaultPipeline sgo.PublicKey
	// uploadRequestC is drained by dispatchUploads (started by Init),
	// one request at a time -- see upload's own doc comment for why
	// this is serial rather than concurrent.
	uploadRequestC chan *UploadRequest

	mx   sync.Mutex
	mBot map[sgo.PublicKey]*Bot
}

// Create builds a Hook. cancel is called (with the eventual error, if any)
// when the dispatcher loop exits -- same convention every other brain/*
// eventHook already uses via mothership.Create's own lifecycle.
func Create(ctx context.Context, cancel context.CancelCauseFunc, parentKey sgo.PrivateKey, config *Configuration) Hook {
	entry := util.LoggerBrainSimple.Fields(logger.FromContext(ctx))
	if config == nil {
		config = &Configuration{}
	}
	return &eventHook{
		ctx:            logger.ToContext(ctx, entry),
		cancel:         cancel,
		logger:         entry,
		parentKey:      parentKey,
		config:         config,
		uploadRequestC: make(chan *UploadRequest, 16),
		mBot:           make(map[sgo.PublicKey]*Bot),
	}
}

func (hs *eventHook) useLocalImage(ctx context.Context, fp string, mEnv map[string]string) (mgrbot.Image, error) {
	image, err := mgrbot.Load(ctx, hs.parentKey, hs.botMarketID, fp, hs.addressBook, hs.builder, [2]int{1, 2}, mEnv)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("failed to load image: %s", err)
	}
	return image, nil
}

// downloadDefaultImage fetches the published default bot image. mEnv is
// forwarded to mgrbot.Load exactly like useLocalImage's -- real gap fixed
// here (2026-09-16): this used to call Load with a hardcoded nil, silently
// dropping MODE (and any other requested env) for every downloaded-image
// upload, unlike the local-image path.
func (hs *eventHook) downloadDefaultImage(ctx context.Context, mEnv map[string]string) (mgrbot.Image, error) {
	fp, err := util.DownloadCatscopeRustBotDemonstrator(ctx)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("bot download failed: %s", err)
	}
	image, err := mgrbot.Load(ctx, hs.parentKey, hs.botMarketID, fp, hs.addressBook, hs.builder, [2]int{1, 2}, mEnv)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("failed to load image: %s", err)
	}
	return image, nil
}
