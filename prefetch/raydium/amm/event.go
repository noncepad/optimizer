package amm

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"git.noncepad.com/pkg/solpipe-util/graph"
	bin "github.com/gagliardetto/binary"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

type eventHandler struct {
	ctx            context.Context
	cancel         context.CancelCauseFunc
	g              graph.Graph
	wg             *sync.WaitGroup
	programID      sgo.PublicKey
	poolMapC       chan<- map[sgo.PublicKey]*AmmInfoWithToken
	errorC         chan<- error
	onTokenResultC chan *ammVaultResult
	slot           *atomic.Uint64
	logger         *slog.Logger
	mPool          map[sgo.PublicKey]*AmmInfoWithToken
}
type ammVaultResult struct {
	pool   sgo.PublicKey
	av     *sgotkn.Account
	pubkey sgo.PublicKey
}

func createHandler(ctx context.Context, programID sgo.PublicKey, cancel context.CancelCauseFunc, poolMapC chan<- map[sgo.PublicKey]*AmmInfoWithToken, errorC chan<- error, logger *slog.Logger) graph.Hook {
	eh := new(eventHandler)
	eh.ctx = ctx
	eh.cancel = cancel
	eh.programID = programID
	eh.poolMapC = poolMapC
	eh.errorC = errorC
	eh.logger = logger
	eh.slot = &atomic.Uint64{}
	eh.slot.Store(0)
	eh.mPool = make(map[sgo.PublicKey]*AmmInfoWithToken)
	eh.onTokenResultC = make(chan *ammVaultResult, 100)
	eh.wg = &sync.WaitGroup{}
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
	ackC := g.Subscribe(ctx, handler.programID, graph.WeightAll, 2)
	errorC := handler.errorC
	go func() {
		doneC := ctx.Done()
		select {
		case <-doneC:
		case <-ackC:
			cancel()
			errorC <- loopOnAck(
				handler.ctx,
				handler.cancel,
				handler.poolMapC,
				handler.onTokenResultC,
				handler.wg,
				handler.mPool,
				handler.logger,
			)
		}
	}()
	handler.logger.Warn("...........Init...................")
	return nil
}

func loopOnAck(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	emC chan<- map[sgo.PublicKey]*AmmInfoWithToken,
	onTokenResultC <-chan *ammVaultResult,
	wg *sync.WaitGroup,
	mPool map[sgo.PublicKey]*AmmInfoWithToken, // do not touch this until waitgroup finishes
	logger *slog.Logger,
) error {
	logger.Info("loopOnAck - 1")
	doneC := ctx.Done()
	waitC := make(chan struct{}, 1)
	go func() {
		wg.Wait()
		waitC <- struct{}{}
	}()
	mPending := make(map[sgo.PublicKey]*ammVaultResult)
done:
	for {
		select {
		case <-doneC:
			return ctx.Err()
		case result := <-onTokenResultC:
			mPending[result.pubkey] = result
		case <-waitC:
			break done
		}
	}
	logger.Info("loopOnAck - 2")
	for k, v := range mPending {
		x, present := mPool[v.pool]
		if present {
			if x.Info.CoinVault.Equals(k) {
				x.CoinVault = v.av
			} else if x.Info.PcVault.Equals(k) {
				x.PcVault = v.av
			}
		}
	}
	cancel(errors.New("finished"))
	select {
	case <-doneC:
	case emC <- mPool:
	}
	logger.Info("loopOnAck - 3")
	return nil
}

func (handler *eventHandler) OnSlot(slot graph.Slot, status graph.SlotStatus) {
	_ = slot
	_ = status
}

func (handler *eventHandler) OnAccount(a graph.Account, isNew bool) {
	header := a.Header()
	if graph.AccountIsDeleted(a) {
		delete(handler.mPool, header.Pubkey)
		return
	}
	data := a.Data()
	if len(data) != Size {
		return
	}
	info, err := Parse(header.Pubkey, data)
	if err != nil {
		handler.logger.Warn("raydium amm parse failed", "pubkey", header.Pubkey, "err", err)
		return
	}
	_, present := handler.mPool[header.Pubkey]
	if present {
		return
	}
	handler.mPool[header.Pubkey] = &AmmInfoWithToken{Info: info}
	tokenResultC := handler.onTokenResultC
	em := handler.g.EdgeManager()
	for _, p := range []sgo.PublicKey{info.CoinVault, info.LpVault} {
		doneC := handler.ctx.Done()
		ackC := handler.g.Subscribe(handler.ctx, p, 0, 1)
		handler.wg.Go(func() {
			select {
			case <-doneC:
				return
			case <-ackC:
			}
			// got token results
			em.RLock()
			a := em.UnsafeAccount(p)
			em.RUnlock()
			if a != nil {
				header := a.Header()
				data := a.Data()
				d := new(sgotkn.Account)
				err2 := bin.UnmarshalBorsh(d, data)
				if err2 == nil {
					select {
					case <-doneC:
						return
					case tokenResultC <- &ammVaultResult{pool: header.Pubkey, av: d, pubkey: header.Pubkey}:
					}
				}
			}
		})
	}
}
