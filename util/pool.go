package util

import (
	"context"
	"sync/atomic"
)

type Pool[T any] struct {
	ctx   context.Context
	reqC  chan<- *poolRequest[T]
	index *atomic.Uint64
}

func Create[T any](ctx context.Context, workerCount int) (Pool[T], <-chan *PoolResponse[T]) {
	reqC := make(chan *poolRequest[T], 10*workerCount)
	responseC := make(chan *PoolResponse[T], 10*workerCount)
	for range workerCount {
		go loopWorker(ctx, reqC, responseC)
	}
	index := &atomic.Uint64{}
	index.Store(1)
	return Pool[T]{
		ctx: ctx, reqC: reqC, index: index,
	}, responseC
}

func (p Pool[T]) Run(ctx context.Context, callback func() (T, error)) uint64 {
	id := p.index.Add(1)
	doneC := ctx.Done()
	select {
	case <-doneC:
	case p.reqC <- &poolRequest[T]{
		ctx:      ctx,
		id:       id,
		callback: callback,
	}:
	}
	return id
}

type poolRequest[T any] struct {
	id       uint64
	ctx      context.Context
	callback func() (T, error)
}
type PoolResponse[T any] struct {
	ID      uint64
	Error   error
	Payload T
}

func loopWorker[T any](
	ctx context.Context,
	reqC <-chan *poolRequest[T],
	responseC chan<- *PoolResponse[T],
) {
	doneC := ctx.Done()
out:
	for {
		select {
		case <-doneC:
			break out
		case req := <-reqC:
			resp, err := req.callback()
			select {
			case <-doneC:
				break out
			case responseC <- &PoolResponse[T]{
				ID:      req.id,
				Error:   err,
				Payload: resp,
			}:
			}
		}
	}
}
