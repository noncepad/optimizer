package cpmm

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
	poolMapC       chan<- map[sgo.PublicKey]*PoolStateWithToken
	errorC         chan<- error
	onTokenResultC chan *cpmmVaultResult
	slot           *atomic.Uint64
	logger         *slog.Logger
	mPool          map[sgo.PublicKey]*PoolStateWithToken
}

type cpmmVaultResult struct {
	pool   sgo.PublicKey
	av     *sgotkn.Account
	pubkey sgo.PublicKey
}

func createHandler(ctx context.Context, programID sgo.PublicKey, cancel context.CancelCauseFunc, poolMapC chan<- map[sgo.PublicKey]*PoolStateWithToken, errorC chan<- error, logger *slog.Logger) graph.Hook {
	eh := new(eventHandler)
	eh.ctx = ctx
	eh.cancel = cancel
	eh.programID = programID
	eh.poolMapC = poolMapC
	eh.errorC = errorC
	eh.logger = logger
	eh.slot = &atomic.Uint64{}
	eh.slot.Store(0)
	eh.mPool = make(map[sgo.PublicKey]*PoolStateWithToken)
	eh.onTokenResultC = make(chan *cpmmVaultResult, 100)
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
	return nil
}

func loopOnAck(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	emC chan<- map[sgo.PublicKey]*PoolStateWithToken,
	onTokenResultC <-chan *cpmmVaultResult,
	wg *sync.WaitGroup,
	mPool map[sgo.PublicKey]*PoolStateWithToken,
	logger *slog.Logger,
) error {
	logger.Info("cpmm loopOnAck - 1")
	doneC := ctx.Done()
	waitC := make(chan struct{}, 1)
	go func() {
		wg.Wait()
		waitC <- struct{}{}
	}()
	mPending := make(map[sgo.PublicKey]*cpmmVaultResult)
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
	logger.Info("cpmm loopOnAck - 2")
	for k, v := range mPending {
		x, present := mPool[v.pool]
		if present {
			if x.Info.Token0Vault.Equals(k) {
				x.Token0Vault = v.av
			} else if x.Info.Token1Vault.Equals(k) {
				x.Token1Vault = v.av
			}
		}
	}
	cancel(errors.New("finished"))
	select {
	case <-doneC:
	case emC <- mPool:
	}
	logger.Info("cpmm loopOnAck - 3")
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
	body, err := a.AnchorData(DiscPoolState)
	if err != nil {
		return
	}
	if len(body) != PoolStateBodySize {
		return
	}
	pool, err := ParsePoolState(header.Pubkey, body)
	if err != nil {
		handler.logger.Warn("cpmm PoolState parse failed", "pubkey", header.Pubkey, "err", err)
		return
	}
	_, present := handler.mPool[header.Pubkey]
	if present {
		return
	}
	handler.mPool[header.Pubkey] = &PoolStateWithToken{Info: pool}
	tokenResultC := handler.onTokenResultC
	em := handler.g.EdgeManager()
	for _, vaultPubkey := range []sgo.PublicKey{pool.Token0Vault, pool.Token1Vault} {
		vp := vaultPubkey
		poolPubkey := header.Pubkey
		doneC := handler.ctx.Done()
		ackC := handler.g.Subscribe(handler.ctx, vp, 0, 1)
		handler.wg.Go(func() {
			select {
			case <-doneC:
				return
			case <-ackC:
			}
			em.RLock()
			acc := em.UnsafeAccount(vp)
			em.RUnlock()
			if acc != nil {
				d := new(sgotkn.Account)
				if err2 := bin.UnmarshalBorsh(d, acc.Data()); err2 == nil {
					select {
					case <-doneC:
					case tokenResultC <- &cpmmVaultResult{pool: poolPubkey, av: d, pubkey: vp}:
					}
				}
			}
		})
	}
}
