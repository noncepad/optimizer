// Package botruntime is the one concrete api.BotRuntimeManager/api.BotRuntime,
// modeled directly on brain/testperpv1's own Init (init.go): allocate a
// slot on a target validator pipeline via BidderManager.Allocate (bid 0,
// free tier -- same as testperpv1), upload the already-loaded bot image to
// it, and wait for the handshake. testperpv1 does this once, at startup,
// against one hardcoded pipeline (common.SampleBotPipeline()); Connect is
// the same sequence with the pipeline as a caller-supplied parameter
// instead, callable more than once against different pipelines from the
// same already-loaded image.
package botruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/optimizer/api"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// handshakeTimeout bounds each individual upload attempt's wait for the
// bot to complete its handshake -- same 30s upload-retry cadence
// testperpv1/init.go's own botdone loop uses.
const uploadRetryInterval = 30 * time.Second

// connectTimeout is the overall deadline across every allocate+upload+
// handshake retry -- same 5-minute budget testperpv1/init.go's own Init
// gives itself (timeStart.Add(5 * time.Minute)).
const connectTimeout = 5 * time.Minute

type manager struct {
	ctx         context.Context
	parentKey   sgo.PrivateKey
	botMarketID sgo.PublicKey
	bidmgr      *bidder.BidderManager
	image       mgrbot.Image
}

// Create wraps an already-loaded bot image (mgrbot.Load -- see e.g.
// brain/testperpv1's useLocalImage/downloadDefaultImage for the two ways
// callers in this codebase already build one) and an already-dialed
// bidmgr (bidder.Dialer.Manager) as an api.BotRuntimeManager. Loading the
// image and dialing the bidder manager are left to the caller, same
// separation chainstate.Create draws around store.Open vs an
// already-open *store.DB -- this package only owns the
// allocate/upload/handshake sequence, not how the image or bidder
// connection were obtained.
func Create(ctx context.Context, parentKey sgo.PrivateKey, bidmgr *bidder.BidderManager, image mgrbot.Image) api.BotRuntimeManager {
	return &manager{
		ctx:         ctx,
		parentKey:   parentKey,
		botMarketID: common.GetBotMarketID(),
		bidmgr:      bidmgr,
		image:       image,
	}
}

// Connect allocates a free-tier slot on pipelineID (bid 0.00, full weight
// -- same allocation testperpv1/init.go's Init makes for its own single
// hardcoded pipeline), then retries uploading the bot image and waiting
// for its handshake until connectTimeout elapses, the same retry shape
// Init's own botdone loop uses (Upload can fail transiently, e.g. while a
// previous instance on that pipeline is still shutting down -- see
// mgrbot.Image.Upload's own doc comment on killing an existing instance).
func (m *manager) Connect(ctx context.Context, pipelineID sgo.PublicKey) (api.BotRuntime, error) {
	entry := logger.FromContext(m.ctx).With("pipeline", pipelineID)

	allocCtx, allocCancel := context.WithTimeout(ctx, 30*time.Second)
	err := m.bidmgr.Allocate(
		allocCtx,
		m.botMarketID,
		0.00,
		map[sgo.PublicKey]float64{pipelineID: 1.0},
	)
	allocCancel()
	if err != nil {
		return nil, fmt.Errorf("botruntime: allocate on pipeline %s failed: %w", pipelineID, err)
	}

	deadline := time.Now().Add(connectTimeout)
	mEnv := map[string]string{"RUST_BACKTRACE": "1"}
	doneC := ctx.Done()
	var instance mgrbot.Bot
	var handshake mgrbot.Handshake
connect:
	for time.Now().Before(deadline) {
		instance, err = m.image.Upload(pipelineID, nil, mEnv)
		if err != nil {
			entry.Info(fmt.Sprintf("botruntime: upload failed, retrying: %s", err))
			select {
			case <-doneC:
				break connect
			case <-time.After(uploadRetryInterval):
				continue
			}
		}
		select {
		case <-doneC:
			err = ctx.Err()
			break connect
		case <-time.After(time.Until(deadline)):
			err = errors.New("timed out waiting for handshake")
			break connect
		case handshake = <-instance.OnHandshake():
			err = handshake.Error
			if err == nil {
				break connect
			}
			entry.Info(fmt.Sprintf("botruntime: handshake failed, retrying: %s", err))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("botruntime: connect to pipeline %s failed: %w", pipelineID, err)
	}

	entry.Info("botruntime: connected, handshake complete")
	return &runtime{
		ctx:         m.ctx,
		pipelineID:  pipelineID,
		botMarketID: m.botMarketID,
		bidmgr:      m.bidmgr,
		bot:         newBotAdapter(instance, entry),
	}, nil
}

// runtime is the one concrete api.BotRuntime -- it tracks exactly the one
// bot instance Connect uploaded to pipelineID. Real simplification, not
// hidden: BotRuntimeManager/BotRuntime have no Upload method of their own
// (see api/bot.go), and testperpv1 -- the example this is modeled on --
// only ever runs a single bot instance per pipeline too, so BotList()
// returning that one bot is a faithful match to the example, not an
// arbitrary restriction. A runtime hosting multiple concurrently-uploaded
// bots would need api.BotRuntimeManager to grow an Upload method first.
type runtime struct {
	ctx         context.Context
	pipelineID  sgo.PublicKey
	botMarketID sgo.PublicKey
	bidmgr      *bidder.BidderManager
	bot         *botAdapter
}

// Status reports Offline once the bot's own connection has ended
// (instance.Ctx().Err() -- see botAdapter's own doc comment on why every
// loop there keys off that same context instead of a separately owned
// one), otherwise Bidder/Nonbidder depending on whether Budget has ever
// been called with a nonzero amount. Deliberately reads instance.Ctx()
// rather than instance.CloseSignal() -- CloseSignal registers a brand new
// subscription channel on every call (see mgrbot.Bot.CloseSignal's own
// internalC send), which would leak one per Status() poll; Ctx().Err() is
// a plain, repeatable, non-blocking check.
func (r *runtime) Status() api.StatusType {
	if r.bot.instance.Ctx().Err() != nil {
		return api.StatusTypeOffline
	}
	if r.bot.bidding() {
		return api.StatusTypeBidder
	}
	return api.StatusTypeNonbidder
}

// Budget re-allocates this pipeline's full weight at the given amount.
// Real gap, not papered over: bidder.BidderManager.Allocate (the only
// budget-setting call this bidder API version exposes) has no slot-scoped
// duration parameter -- slot is accepted for api.BotRuntime interface
// compliance but not otherwise used; amount takes effect immediately with
// no expiration tied to slot. Budget has no error return on the
// api.BotRuntime interface, so a failed re-allocation is logged, not
// surfaced -- same fire-and-forget convention harness/harness.go's
// SetSOL/Fund/Sweep already use for their own no-error-return mutating
// methods.
func (r *runtime) Budget(amount uint64, slot graph.Slot) {
	_ = slot
	entry := logger.FromContext(r.ctx).With("pipeline", r.pipelineID)
	budget := float64(amount)
	r.bot.setBidding(budget > 0)
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	if err := r.bidmgr.Allocate(ctx, r.botMarketID, budget, map[sgo.PublicKey]float64{r.pipelineID: 1.0}); err != nil {
		entry.Error(fmt.Sprintf("botruntime: budget update to %d failed: %s", amount, err))
	}
}

func (r *runtime) BotList() []api.Bot {
	return []api.Bot{r.bot}
}

var _ api.BotRuntimeManager = (*manager)(nil)
var _ api.BotRuntime = (*runtime)(nil)
