package kamino

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

type eventHandler struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	g        graph.Graph
	poolMapC chan<- map[sgo.PublicKey]*MarketReserve
	errorC   chan<- error
	slot     *atomic.Uint64
	logger   *slog.Logger
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, poolMapC chan<- map[sgo.PublicKey]*MarketReserve, errorC chan<- error, logger *slog.Logger) graph.Hook {
	eh := new(eventHandler)
	eh.ctx = ctx
	eh.cancel = cancel
	eh.poolMapC = poolMapC
	eh.errorC = errorC
	eh.logger = logger
	eh.slot = &atomic.Uint64{}
	eh.slot.Store(0)
	return eh
}

func (handler *eventHandler) CommitFinish() {
}

func (handler *eventHandler) CommitStart(slot graph.Slot) {
	handler.slot.Store(slot)
}

func (handler *eventHandler) Ctx() context.Context {
	return handler.ctx
}

func (handler *eventHandler) Init(g graph.Graph) error {
	handler.g = g
	// fetch whirlpool configs and whirlpools
	ctx, cancel := context.WithCancel(handler.ctx)

	ackC := g.Subscribe(ctx, ProgramID, graph.WeightAll, 2)
	go func() {
		doneC := ctx.Done()
		select {
		case <-doneC:
		case <-ackC:
			cancel()
			loopOnAck(handler.ctx, handler.cancel, handler.poolMapC, handler.errorC, g, handler.slot, handler.logger)
		}
	}()
	handler.logger.Warn("...........Init...................")
	return nil
}

type requestLending struct {
	ctx           context.Context
	cancel        context.CancelCauseFunc
	lendingMarket sgo.PublicKey
}

func loopOnAck(ctx context.Context, cancel context.CancelCauseFunc, emC chan<- map[sgo.PublicKey]*MarketReserve, errorC chan<- error, g graph.Graph, slot *atomic.Uint64, logger *slog.Logger) {
	em := g.EdgeManager()
	// we are missing token accounts
	em.RLock()
	mConfigDown := em.EdgeDown(ProgramID)
	em.RUnlock()
	var checkDisc [8]uint8
	var err error
	configI := 0
	lendAckC := make(chan *replyWithMarket, 100)
	mLendingMarket := make(map[sgo.PublicKey]*requestLending, 1000)
	wg := &sync.WaitGroup{}
	for lendPubkey := range mConfigDown {
		_, present := mLendingMarket[lendPubkey]
		if present {
			continue
		}
		configI++
		{
			em.RLock()
			a := em.UnsafeAccount(lendPubkey)
			em.RUnlock()
			if a == nil {
				continue
			}
			data := a.Data()
			if len(data) < 8 {
				continue
			}
			copy(checkDisc[:], data[0:8])
			if checkDisc != DiscLendingMarket {
				continue
			}
			req := new(requestLending)
			req.ctx, req.cancel = context.WithCancelCause(ctx)
			req.lendingMarket = lendPubkey
			mLendingMarket[lendPubkey] = req
			wg.Go(func() {
				getReserve(req.ctx, req.cancel, lendAckC, lendPubkey, g)
			})
		}
	}
	waitC := make(chan struct{}, 1)
	go func() {
		wg.Wait()
		waitC <- struct{}{}
	}()
	doneC := ctx.Done()
	mMarket := make(map[sgo.PublicKey]*MarketReserve)
out2:
	for {
		select {
		case <-doneC:
			return
		case r := <-lendAckC:
			mMarket[r.m.Market] = r.m
			r.replyC <- struct{}{}
		case <-waitC:
			break out2
		}
	}
	logger.With("err", err).Warn(fmt.Sprintf("...........Config Finished %d", len(mConfigDown)))
	// have whirlpool config, get corresponding pools
	errorC <- err
	if err == nil {
		logger.Warn(fmt.Sprintf(".......finished processing.... mPool %d", len(mLendingMarket)))
		emC <- mMarket
	}
	cancel(errors.New("finished"))
}

type replyWithMarket struct {
	m *MarketReserve
	// make sure we do not exit the Goroutine before getting all markets
	replyC chan<- struct{}
}

type MarketReserve struct {
	Market   sgo.PublicKey
	MReserve map[sgo.PublicKey]*Reserve
}

func getReserve(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	marketC chan<- *replyWithMarket,
	market sgo.PublicKey,
	g graph.Graph,
) {
	doneC := ctx.Done()

	ackC := g.Subscribe(ctx, market, graph.WeightAll, 2)
	select {
	case <-doneC:
		return
	case <-ackC:
		// stop receiving updates
		cancel(nil)
	}

	em := g.EdgeManager()
	em.RLock()
	mDown := em.EdgeDown(market)
	em.RUnlock()
	out := new(MarketReserve)
	out.Market = market
	out.MReserve = make(map[sgo.PublicKey]*Reserve)
	var checkDisc [8]uint8
	for reservePubkey := range mDown {
		em.RLock()
		a := em.UnsafeAccount(reservePubkey)
		em.RUnlock()
		_, present := out.MReserve[reservePubkey]
		if present {
			continue
		}
		rdata := a.Data()
		if len(rdata) < 8 {
			continue
		}
		copy(checkDisc[:], rdata[0:8])
		if checkDisc != DiscReserve {
			continue
		}
		// Body is data after the 8-byte discriminator
		reserve := parseReserve(reservePubkey, rdata[8:])
		if reserve == nil {
			continue
		}
		out.MReserve[reservePubkey] = reserve
	}
	respC := make(chan struct{}, 1)
	select {
	case <-doneC:
		return
	case marketC <- &replyWithMarket{m: out, replyC: respC}:
	}
}

func (handler *eventHandler) OnSlot(slot graph.Slot, status graph.SlotStatus) {
	_ = slot
	_ = status
}

func (handler *eventHandler) OnAccount(a graph.Account, isNew bool) {
	_ = a
	_ = isNew
}
