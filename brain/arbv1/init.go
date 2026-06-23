package arbv1

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// Init is where we set a Pipeline allocation.  In this case, we just want to
// connect to a single Pipeline (ie Validator selling bot runtime capacity).
// We set the auction budget to 0.00 because we are not ready to participate in
// Solpipe auctions yet.
func (hs *eventHook) Init(g graph.Graph, builder *txbuilder.BuildManager, addressBook common.BotClientDialer, bidmgr *bidder.BidderManager, authorizer sgo.PublicKey) error {
	doneC := hs.ctx.Done()
	hs.graph = g
	hs.builder = builder
	hs.bidmgr = bidmgr
	hs.addressBook = addressBook
	hs.authorizer = authorizer
	hs.botMarketID = common.GetBotMarketID()
	// this is the free Catscope non-voting validator
	targetPipeline := common.SampleBotPipeline()
	entry := hs.logger.With("pipeline", targetPipeline)
	entry.With(logger.Loc("init", 1)).Info("calling Allocate on manager daemon")
	// spend money here to get into a validator if the validator is
	// oversubscribed.
	allocCtx, allocCancel := context.WithTimeout(hs.ctx, 30*time.Second)
	err := hs.bidmgr.Allocate(
		allocCtx,
		hs.botMarketID,
		0.00,
		map[sgo.PublicKey]float64{
			targetPipeline: 1.0,
		},
	)
	allocCancel()
	if err != nil {
		entry.With(logger.Loc("init", 2), "err", err).Info("failed to set allocation")
		return fmt.Errorf("allocation failed: %s", err)
	}
	entry.With(logger.Loc("init", 3)).Info("setting allocation; waiting for bidder proxy to connect with pipeline")

	mEnv := make(map[string]string, 1)
	mEnv["MODE"] = "arbv1"
	var botImage mgrbot.Image
	if 0 < len(hs.config.BotImage) {
		// compile a bot locally
		botImage, err = hs.useLocalImage(hs.ctx, hs.config.BotImage, mEnv)
	} else {
		// download a bot image from https://noncepad.com
		botImage, err = hs.downloadDefaultImage(hs.ctx)
	}
	if err != nil {
		return err
	}
	var instance mgrbot.Bot
	timeStart := time.Now()
	timeFinish := timeStart.Add(5 * time.Minute)
botdone:
	for timeFinish.After(time.Now()) {
		mEnv2 := make(map[string]string)
		mEnv2["RUST_BACKTRACE"] = "1"
		// send the image to the validator and turn it on.
		// if there is already an instance running, it will be killed.
		instance, err = botImage.Upload(targetPipeline, nil, mEnv2)
		if err != nil {
			select {
			case <-doneC:
				break botdone
			case <-time.After(30 * time.Second):
				entry.With(logger.Loc("init", 4)).Info("waiting for upload")
				continue
			}
		}
		go instance.LogToFile(hs.ctx, os.Stderr, true)
		// we have an instance, wait for the handshake
		handshakeC := instance.OnHandshake()
		err = nil
		select {
		case <-doneC:
			entry.Info("context cancel doneC")
			err = hs.ctx.Err()
		case <-time.After(time.Until(timeFinish)):
			entry.Info("timeout!")
			err = errors.New("time out")
		case x := <-handshakeC:
			err = x.Error
			if err == nil {
				hs.handshake = &x
				entry.Info("handshake done!")
				break botdone
			}
		}
		if err != nil {
			entry.With(logger.Loc("init", 5), "err", err).Info("failed to get bot client for pipeline")
		}
	}
	if err != nil {
		return fmt.Errorf("failed to get bot client for pipeline %s: %s", targetPipeline, err)
	}
	if hs.handshake == nil {
		return fmt.Errorf("failed to get bot client for pipeline %s: %s", targetPipeline, errors.New("timeout"))
	}
	entry.With(logger.Loc("init", 5)).Info(fmt.Sprintf("have bot;now uploading bot: have handshake %s", hs.handshake))
	if hs.handshake.Error != nil {
		return hs.handshake.Error
	}
	go loopInstance(hs.ctx, hs.cancel, *hs.handshake, instance, entry, hs.state)
	hs.instance = new(mgrbot.Bot)
	*hs.instance = instance
	return hs.initWallet()
}
