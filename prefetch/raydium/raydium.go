// Package raydium preloads Raydium AMM v4 trading pools.
package raydium

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/amm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/clmm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/cpmm"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

var ProgramID = sgo.MustPublicKeyFromBase58("675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8")

// Raydium holds all AMM v4 pools loaded at startup.
type Raydium struct {
	Amm  *amm.Configuration  `json:"amm"`
	Clmm *clmm.Configuration `json:"clmm"`
	Cpmm *cpmm.Configuration `json:"cpmm"`
}

var defaultList = []string{"raydium_amm", "raydium_clmm", "raydium_cpmm"}

// Create downloads Raydium pool data from the on-chain state service and
// writes it to db (if non-nil). Each sub-package writes accounts to the
// database inline via CommitStart/CommitFinish as they stream in. If force
// is true, each sub-package re-fetches even if its tables are already
// populated, instead of skipping.
func Create(
	parentCtx context.Context,
	stateClient state.Client,
	db *sql.DB,
	maxSubscriptionCount int,
	force bool,
	mintTracker *mintinfo.Tracker,
	mDownload map[string]struct{},
) error {
	var present bool
	if len(mDownload) == 0 {
		mDownload = make(map[string]struct{})
		for _, v := range defaultList {
			mDownload[v] = struct{}{}
		}
	}

	var err error
	ctx, cancel := context.WithCancel(parentCtx)
	doneC := ctx.Done()
	wg := &sync.WaitGroup{}
	n := 0
	errorC := make(chan error, 3)
	_, present = mDownload["raydium_cpmm"]
	if present {
		wg.Go(func() {
			errorC <- cpmm.Download(ctx, stateClient, db, maxSubscriptionCount, force, mintTracker)
		})
		n++
	}
	_, present = mDownload["raydium_amm"]
	if present {
		wg.Go(func() {
			errorC <- amm.Download(ctx, stateClient, db, maxSubscriptionCount, force, mintTracker)
		})
		n++
	}
	_, present = mDownload["raydium_clmm"]
	if present {
		wg.Go(func() {
			errorC <- clmm.Download(ctx, stateClient, db, maxSubscriptionCount, force, mintTracker)
		})
		n++
	}
	for range n {
		select {
		case <-doneC:
			err = ctx.Err()
		case err = <-errorC:
		}
		if err != nil {
			cancel()
			return fmt.Errorf("raydium: %w", err)
		}
	}
	cancel()
	entry := logger.FromContext(ctx)
	entry.Info("finished database download!")
	return nil
}
