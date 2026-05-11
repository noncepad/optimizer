// Package simple
package simple

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	mgrbot "git.noncepad.com/pkg/bot/solpipe/bidder/manager/bot"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/optimizer/util"
	sgo "git.noncepad.com/pkg/solana-go"
	"git.noncepad.com/pkg/solpipe-util/logger"
)

type hookSimple struct {
	ctx         context.Context
	cancel      context.CancelFunc
	logger      *slog.Logger
	client      state.Client
	bidmgr      *bidder.BidderManager
	addressBook common.BotClientDialer
	builder     *txbuilder.BuildManager
	authorizer  sgo.PublicKey
	botMarketID sgo.PublicKey
}

// Create creates a helloworld bot.
func Create(ctx context.Context, cancel context.CancelFunc) brain.Brain {
	entry := util.LoggerBrainSimple.Fields(logger.FromContext(ctx))
	return &hookSimple{
		ctx:    logger.ToContext(ctx, entry),
		cancel: cancel,
		logger: entry,
	}
}

func (hs *hookSimple) Init(client state.Client, builder *txbuilder.BuildManager, addressBook common.BotClientDialer, bidmgr *bidder.BidderManager, authorizer sgo.PublicKey) error {
	doneC := hs.ctx.Done()
	hs.client = client
	hs.builder = builder
	hs.bidmgr = bidmgr
	hs.addressBook = addressBook
	hs.authorizer = authorizer
	hs.botMarketID = common.GetBotMarketID()
	hs.logger.With(logger.Loc("init", 1)).Debug("setting allocation")
	targetPipeline := common.SampleBotPipeline()
	entry := hs.logger.With("pipeline", targetPipeline)
	err := hs.bidmgr.Allocate(hs.ctx, hs.botMarketID, 0.0, map[sgo.PublicKey]float64{
		targetPipeline: 1.0,
	})
	if err != nil {
		entry.With(logger.Loc("init", 2), "err", err).Info("failed to set allocation")
		return fmt.Errorf("allocation failed: %s", err)
	}
	entry.With(logger.Loc("init", 3)).Debug("setting allocation; waiting for bidder proxy to connect with pipeline")

	localImage := os.Getenv("BOT_IMAGE")
	args := make([]string, 1)
	args[0] = "--echo"
	mEnv := make(map[string]string, 1)
	mEnv["MODE"] = "HELLOWORLD"
	botImage, err := hs.useLocalImage(hs.ctx, localImage, args, mEnv)
	if err != nil {
		return err
	}
	var instance mgrbot.Bot
	uploadedBot := false
	timeStart := time.Now()
botdone:
	for time.Since(timeStart) < 2*time.Minute {
		instance, err = botImage.Upload(targetPipeline)
		// failed to upload: rpc error: code = AlreadyExists desc = duplicate instance
		if err == nil {
			uploadedBot = true
			break botdone
		}
		entry.With(logger.Loc("init", 4), "err", err).Info("failed to get bot client for pipeline")
		err = nil
		select {
		case <-doneC:
			err = hs.ctx.Err()
		case <-time.After(15 * time.Second):
		}
		if err != nil {
			return fmt.Errorf("failed to get bot client for pipeline %s: %s", targetPipeline, err)
		}
	}
	if !uploadedBot {
		return fmt.Errorf("failed to get bot client for pipeline %s: %s", targetPipeline, errors.New("timeout"))
	}
	entry.With(logger.Loc("init", 5)).Info("have bot;now uploading bot")
	go loopInstance(hs.ctx, hs.cancel, instance, entry)
	return nil
}

func (hs *hookSimple) Evaluate(solpipeState brain.SolpipeState, bidderState brain.BidderState) error {
	return nil
}

// downloadBouncerV1 tbd
func (hs *hookSimple) downloadBouncerV1(ctx context.Context) (mgrbot.Image, error) {
	fp, err := util.DownloadCatscopeRustBotDemonstrator(ctx)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("bot download failed: %s", err)
	}
	image, err := mgrbot.Load(ctx, hs.botMarketID, fp, hs.addressBook, hs.builder, [2]int{1, 2}, nil, nil)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("failed to load image: %s", err)
	}
	return image, nil
}

func (hs *hookSimple) useLocalImage(ctx context.Context, fp string, args []string, mEnv map[string]string) (mgrbot.Image, error) {
	image, err := mgrbot.Load(ctx, hs.botMarketID, fp, hs.addressBook, hs.builder, [2]int{1, 2}, args, mEnv)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("failed to load image: %s", err)
	}
	return image, nil
}
