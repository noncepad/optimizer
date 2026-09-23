package brain

import (
	"context"
	"fmt"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// Init sets a Pipeline allocation (bid = 0, free tier -- we're not
// participating in Solpipe auctions here) and starts the background
// dispatcher that services Request calls. It does NOT upload any bot
// itself -- that only ever happens in response to a request arriving on
// uploadRequestC, which is the whole point of this package (see
// dispatchUploads/upload in upload.go).
func (hs *eventHook) Init(g graph.Graph, builder *txbuilder.BuildManager, addressBook common.BotClientDialer, bidmgr *bidder.BidderManager, authorizer sgo.PublicKey) error {
	hs.graph = g
	hs.builder = builder
	hs.bidmgr = bidmgr
	hs.addressBook = addressBook
	hs.authorizer = authorizer
	hs.botMarketID = common.GetBotMarketID()
	// this is the free Catscope non-voting validator
	hs.defaultPipeline = common.SampleBotPipeline()
	entry := hs.logger.With("pipeline", hs.defaultPipeline)
	entry.With(logger.Loc("init", 1)).Info("calling Allocate on manager daemon")

	allocCtx, allocCancel := context.WithTimeout(hs.ctx, 30*time.Second)
	defer allocCancel()
	err := hs.bidmgr.Allocate(
		allocCtx,
		hs.botMarketID,
		0.00,
		map[sgo.PublicKey]float64{
			hs.defaultPipeline: 1.0,
		},
	)
	if err != nil {
		entry.With(logger.Loc("init", 2), "err", err).Info("failed to set allocation")
		return fmt.Errorf("allocation failed: %s", err)
	}
	entry.With(logger.Loc("init", 3)).Info("allocation set; upload requests will now be serviced")

	go hs.dispatchUploads()
	return nil
}
