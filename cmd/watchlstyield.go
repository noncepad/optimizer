package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	lstyield "git.noncepad.com/pkg/optimizer/prefetch/lst-yield"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// WatchLstYieldCmd samples every real candidate LST's SOL-per-LST exchange
// rate on an interval and appends it to prefetch.db's lst_yield_snapshot
// table (see optimizer/prefetch/lst-yield) -- the real profitability
// signal an LST-collateral leverage-loop strategy (originally built as
// leveragedloopv1's Time-Expanded DAG, since removed) would need before
// any loop mechanics are safe to act on.
// Unlike watch-pnl/watch-balances, this doesn't touch a wallet at all --
// every account it reads (Sanctum's shared lst_state_list account, each
// LST's pool-reserves ATA) is public, so no fee-payer/signing key is
// needed, just an RPC endpoint. Tracks lstyield.TrackedLSTs (37 real
// candidates as of 2026-08-29), fetched with two RPC calls per poll
// (lstyield.FetchRates: one lst_state_list read, one batched
// getMultipleAccounts for reserves) rather than one call per LST.
type WatchLstYieldCmd struct {
	RPCURL       string        `name:"rpc-url" default:"https://api.mainnet-beta.solana.com" help:"Solana RPC endpoint to read LST state accounts from."`
	PollInterval time.Duration `name:"poll" default:"1h" help:"how often to sample each tracked LST's exchange rate."`
}

func (r *WatchLstYieldCmd) Run(rc *RunConfig) error {
	ctx := rc.Ctx
	cancel := rc.Cancel
	defer cancel(nil)

	rpcClient := rpc.New(r.RPCURL)

	prefetchDB, err := store.Open(getDBFilePath())
	if err != nil {
		return fmt.Errorf("failed to open prefetch db: %s", err)
	}
	defer func() {
		_ = prefetchDB.Close()
	}()

	entry := logger.FromContext(ctx)
	fmt.Printf("watch-lst-yield: tracking %d LSTs (poll every %s)\n", len(lstyield.TrackedLSTs), r.PollInterval)

	ticker := time.NewTicker(r.PollInterval)
	defer ticker.Stop()
	for {
		pollLstYieldOnce(ctx, rpcClient, prefetchDB.Raw(), entry)
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
		}
	}
}

// pollLstYieldOnce fetches every tracked LST's rate in one batched round
// trip (see lstyield.FetchRates), then records a snapshot for each mint
// that resolved -- a mint missing from the result this poll (RPC hiccup,
// reserve momentarily zero) just gets skipped and retried next tick.
func pollLstYieldOnce(ctx context.Context, rpcClient *rpc.Client, db *sql.DB, entry *slog.Logger) {
	now := time.Now()
	mints := make([]sgo.PublicKey, len(lstyield.TrackedLSTs))
	for i, lst := range lstyield.TrackedLSTs {
		mints[i] = lst.Mint
	}
	rates, err := lstyield.FetchRates(ctx, rpcClient, mints)
	if err != nil {
		entry.Error(fmt.Sprintf("watch-lst-yield: fetch rates: %s", err))
		return
	}
	for _, lst := range lstyield.TrackedLSTs {
		rate, ok := rates[lst.Mint]
		if !ok {
			entry.Warn(fmt.Sprintf("watch-lst-yield: %s: rate unavailable this poll", lst.Label()))
			continue
		}
		if err := lstyield.RecordSnapshot(db, lst.Mint, now, rate); err != nil {
			entry.Error(fmt.Sprintf("watch-lst-yield: %s: record snapshot: %s", lst.Label(), err))
		}
	}
}
