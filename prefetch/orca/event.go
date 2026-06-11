package orca

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

func (orca *Orca) fetchWhirlpool(parentCtx context.Context, stateClient state.Client, logger *slog.Logger) error {
	ctx, cancel := context.WithCancelCause(parentCtx)
	doneC := ctx.Done()
	errorC := make(chan error, 2)
	poolMapC := make(chan map[sgo.PublicKey]*Whirlpool, 1)
	go stateClient.DetachHook(errorC, createHandler(ctx, cancel, poolMapC, errorC, logger))
	var err error
	select {
	case <-doneC:
		err = ctx.Err()
	case err = <-errorC:
	}
	cancel(errors.New("complete"))
	if err != nil {
		return fmt.Errorf("hook failed: %s", err)
	}
	mPool := <-poolMapC
	orca.mPool = make(map[sgo.PublicKey]int, len(mPool))
	orca.Pools = make([]*Whirlpool, len(mPool))
	{
		i := 0
		for k, v := range mPool {
			orca.Pools[i] = v
			orca.mPool[k] = i
			i++
		}
	}
	return nil
}

type eventHandler struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	g        graph.Graph
	poolMapC chan<- map[sgo.PublicKey]*Whirlpool
	errorC   chan<- error
	slot     *atomic.Uint64
	logger   *slog.Logger
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, poolMapC chan<- map[sgo.PublicKey]*Whirlpool, errorC chan<- error, logger *slog.Logger) graph.Hook {
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

func loopOnAck(ctx context.Context, cancel context.CancelCauseFunc, emC chan<- map[sgo.PublicKey]*Whirlpool, errorC chan<- error, g graph.Graph, slot *atomic.Uint64, logger *slog.Logger) {
	em := g.EdgeManager()
	// we are missing token accounts
	em.RLock()
	mConfigDown := em.EdgeDown(ProgramID)
	em.RUnlock()
	var discriminator [8]uint8
	var err error
	configI := 0
	mPool := make(map[sgo.PublicKey]*Whirlpool, 1000)
out1:
	for configPubkey := range mConfigDown {
		configI++
		{
			em.RLock()
			a := em.UnsafeAccount(configPubkey)
			em.RUnlock()
			if a == nil {
				continue
			}
			data := a.Data()
			if len(data) < 8 {
				continue
			}
			copy(discriminator[:], data[0:8])
			if discriminator != DiscriminatorWhirlpoolConfig {
				continue
			}
		}
		configCtx, configCancel := context.WithCancel(ctx)
		logger.Warn(fmt.Sprintf("...........Config %s %d/%d sending", configPubkey, configI, len(mConfigDown)))
		configAckC := g.Subscribe(configCtx, configPubkey, graph.WeightAll, 2)
		logger.Warn(fmt.Sprintf("...........Config %s %d/%d sent", configPubkey, configI, len(mConfigDown)))
		doneC2 := configCtx.Done()
		select {
		case <-doneC2:
			configCancel()
			cancel(errors.New("timeout - 1 "))
			return
		case <-configAckC:
			configCancel()
		}
		{
			// load in the pool
			em.RLock()
			mDown := em.EdgeDown(configPubkey)
			em.RUnlock()
			logger.Warn(fmt.Sprintf("...........processing Config %s %d/%d; pool count %d", configPubkey, configI, len(mConfigDown), len(mDown)))
			for poolPubkey := range mDown {
				_, present := mPool[poolPubkey]
				if present {
					continue
				}

				var pool *Whirlpool
				em.RLock()
				a := em.UnsafeAccount(poolPubkey)
				em.RUnlock()
				if a == nil {
					continue
				}
				header := a.Header()
				realSlot := slot.Load()
				if header.Slot+10_000 < realSlot {
					// too old
					continue
				}
				data := a.Data()
				if len(data) < 8 {
					continue
				}
				copy(discriminator[:], data[0:8])
				if discriminator != DiscriminatorWhirlpool {
					continue
				}
				pool, err = parseWhirlpool(poolPubkey, data)
				if err != nil {
					em.Unlock()
					err = fmt.Errorf("failed to parse whirlpool: %s", err)
					break out1
				}
				mPool[poolPubkey] = pool
			}

		}
	}
	logger.With("err", err).Warn(fmt.Sprintf("...........Config Finished %d", len(mConfigDown)))
	// have whirlpool config, get corresponding pools
	errorC <- err
	if err == nil {
		logger.Warn(fmt.Sprintf(".......finished processing.... mPool %d", len(mPool)))
		emC <- mPool
	}
	cancel(errors.New("finished"))
}

func (handler *eventHandler) OnSlot(slot graph.Slot, status graph.SlotStatus) {
	_ = slot
	_ = status
}

func (handler *eventHandler) OnAccount(a graph.Account, isNew bool) {
	_ = a
	_ = isNew
}
