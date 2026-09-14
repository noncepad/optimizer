// Package prefetch builds wasm bots preloaded with necessary information
package prefetch

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/trading"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/solpipe-util/logger"
)

// Prefetcher holds a catscope client and an sqlite database.
type Prefetcher struct {
	ctx     context.Context
	state   state.Client
	trading *trading.TradingPair
	logger  *slog.Logger
	tmpdir  string
	storeDB *store.DB
}

func Create(ctx context.Context, wg *sync.WaitGroup, stateClient state.Client, storeDB *store.DB) (*Prefetcher, error) {
	tmpdir, err := os.MkdirTemp("/tmp", "prefetchholder*")
	if err != nil {
		return nil, fmt.Errorf("failed to create prefetcher holder directory: %s", err)
	}
	wg.Go(func() {
		<-ctx.Done()
		_ = os.RemoveAll(tmpdir)
	})
	return &Prefetcher{
		ctx: ctx, state: stateClient, logger: logger.FromContext(ctx), trading: trading.Create(), tmpdir: tmpdir, storeDB: storeDB,
	}, nil
}

func (pf *Prefetcher) Store() *store.DB {
	return pf.storeDB
}

func (pf *Prefetcher) State() state.Client {
	return pf.state
}
