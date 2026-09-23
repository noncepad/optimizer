package main

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/pnl"
	pricefeed "git.noncepad.com/pkg/optimizer/prefetch/price-feed"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/common"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// mintSOL is wrapped SOL's mint -- the identifier native SOL balances are
// recorded under, matching prefetch/liquidity.go's own inline use of the
// same address (there's no shared exported constant for it elsewhere in
// this codebase).
var mintSOL = sgo.MustPublicKeyFromBase58("So11111111111111111111111111111111111111112")

// tradingChildKeyIndex is the derivation index every bot mode in this repo
// uses for its actual trading key -- see arbv1/testperpv1/helloworldv1/
// perpfundingv1's eval.go, all four call
// common.DeriveChildKeyFromIndex(hs.parentKey, 1) identically -- and
// shell/tools.go's walletBalanceTool, which documents the same constant
// for the same reason: the derived child, not the parent fee-payer, is
// what actually trades.
const tradingChildKeyIndex = 1

// WatchPnLCmd polls the derived trading (child) wallet's live SOL + SPL
// token balances on an interval and records any that changed into
// prefetch.db's pnl_position_snapshot table (see optimizer/prefetch/pnl),
// priced via Jupiter. Unlike watch-balances (Solpipe bidder/pipeline
// balance events -- infrastructure spend, not trading positions), this
// reads the wallet directly, the same way `optimizer balance` does, just
// repeatedly instead of once.
type WatchPnLCmd struct {
	ParentKey     string        `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	JupiterAPIKey string        `option:"jupiter-key" env:"JUPITER_API_KEY" help:"Jupiter Price API key (falls back to JUPITER_API_KEY, including from .env). Optional -- omit to use Jupiter's free unauthenticated tier."`
	PollInterval  time.Duration `option:"poll" default:"30s" help:"how often to re-read the trading wallet's balances."`
}

func (r *WatchPnLCmd) Run(rc *RunConfig) error {
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(r.ParentKey)
	if err != nil {
		return fmt.Errorf("failed to load authorizer: %s", err)
	}
	childKey := common.DeriveChildKeyFromIndex(parentKey, tradingChildKeyIndex)
	wallet := childKey.PublicKey()

	ctx := rc.Ctx
	cancel := rc.Cancel
	defer cancel(nil)

	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		return fmt.Errorf("failed to create dialer: %s", err)
	}
	stateClient := dialer.State()

	prefetchDB, err := store.Open(getDBFilePath())
	if err != nil {
		return fmt.Errorf("failed to open prefetch db: %s", err)
	}
	defer func() {
		_ = prefetchDB.Close()
	}()

	pt := newPnlPriceTracker(r.JupiterAPIKey)
	defer pt.Close()

	entry := logger.FromContext(ctx)
	fmt.Printf("watch-pnl: tracking %s (poll every %s)\n", wallet, r.PollInterval)

	ticker := time.NewTicker(r.PollInterval)
	defer ticker.Stop()
	for {
		if err := pollOnce(ctx, stateClient, prefetchDB.Raw(), pt, wallet); err != nil {
			entry.Error(fmt.Sprintf("watch-pnl: poll failed: %s", err))
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
		}
	}
}

// pollOnce reads wallet's current SOL + SPL token balances and records
// whatever changed since the last poll -- pnl.RecordIfChanged is a no-op
// for any mint whose balance hasn't moved, so this is safe to call on
// every tick regardless of poll interval.
func pollOnce(ctx context.Context, stateClient state.Client, db *sql.DB, pt *pnlPriceTracker, wallet sgo.PublicKey) error {
	balance, err := util.FetchWalletBalance(ctx, stateClient, wallet)
	if err != nil {
		return fmt.Errorf("fetch wallet balance: %w", err)
	}
	if !balance.Found {
		return nil
	}
	now := time.Now()

	pt.Track(ctx, mintSOL)
	solDecimals, solPrice := pt.Lookup(mintSOL)
	if _, err := pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: now, Wallet: wallet, Mint: mintSOL, Balance: balance.Lamports,
		Decimals: solDecimals, USDPrice: solPrice,
	}); err != nil {
		return fmt.Errorf("record SOL snapshot: %w", err)
	}

	for _, t := range balance.Tokens {
		if t.Mint.Equals(mintSOL) {
			// A real wrapped-SOL token account shares mintSOL's address with
			// the native-SOL entry recorded above, which represents a
			// different balance (lamports, not an SPL token amount). Recording
			// both under the same (wallet, mint) key made RecordIfChanged see
			// perpetual "changes" as the two alternated, reinserting a row
			// every single poll regardless of whether either actually moved --
			// confirmed live: 14,858 rows accumulated over 5 days, almost all
			// this churn. Skip it; native SOL is already the authoritative
			// spendable-SOL figure.
			continue
		}
		pt.Track(ctx, t.Mint)
		decimals, price := pt.Lookup(t.Mint)
		if _, err := pnl.RecordIfChanged(db, pnl.Snapshot{
			Time: now, Wallet: wallet, Mint: t.Mint, Balance: t.Amount,
			Decimals: decimals, USDPrice: price,
		}); err != nil {
			return fmt.Errorf("record %s snapshot: %w", t.Mint, err)
		}
	}
	return nil
}

// pnlPriceTracker keeps a Jupiter price poller running for exactly the
// set of mints seen so far, restarting it (pricefeed.Poller's mint list
// is fixed at construction) whenever a new mint shows up -- same shape as
// watchbalances.go's own priceTracker, duplicated rather than shared
// since the two commands' polling loops are otherwise unrelated. Track/
// Lookup are only ever called from this command's single polling loop,
// so no locking is needed around the poller lifecycle itself -- only
// around the price cache, which the poller's own goroutine also writes to.
type pnlPriceTracker struct {
	apiKey string
	mints  map[sgo.PublicKey]struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mx     sync.RWMutex
	prices map[sgo.PublicKey]pricefeed.PriceUpdate
}

func newPnlPriceTracker(apiKey string) *pnlPriceTracker {
	return &pnlPriceTracker{
		apiKey: apiKey,
		mints:  make(map[sgo.PublicKey]struct{}),
		prices: make(map[sgo.PublicKey]pricefeed.PriceUpdate),
	}
}

func (pt *pnlPriceTracker) Track(ctx context.Context, mint sgo.PublicKey) {
	if _, known := pt.mints[mint]; known {
		return
	}
	pt.mints[mint] = struct{}{}
	mints := make([]sgo.PublicKey, 0, len(pt.mints))
	for m := range pt.mints {
		mints = append(mints, m)
	}
	pt.restart(ctx, mints)
}

func (pt *pnlPriceTracker) restart(ctx context.Context, mints []sgo.PublicKey) {
	if pt.cancel != nil {
		pt.cancel()
		pt.wg.Wait()
	}
	pctx, cancel := context.WithCancel(ctx)
	pt.cancel = cancel
	poller, err := pricefeed.New(pricefeed.Config{APIKey: pt.apiKey, Mints: mints})
	if err != nil {
		// New only fails on empty APIKey/Mints, neither possible here --
		// but if it ever does, prices for these mints just stay unknown
		// rather than crashing balance recording.
		logger.FromContext(ctx).Error(fmt.Sprintf("watch-pnl: price poller: %s", err))
		return
	}
	updateC := poller.Run(pctx)
	pt.wg.Add(1)
	go func() {
		defer pt.wg.Done()
		for u := range updateC {
			pt.mx.Lock()
			pt.prices[u.Mint] = u
			pt.mx.Unlock()
		}
	}()
}

// Lookup returns the mint's decimals/USD price if a poll has landed yet.
func (pt *pnlPriceTracker) Lookup(mint sgo.PublicKey) (*uint8, *float64) {
	pt.mx.RLock()
	u, ok := pt.prices[mint]
	pt.mx.RUnlock()
	if !ok {
		return nil, nil
	}
	decimals, price := u.Decimals, u.USDPrice
	return &decimals, &price
}

func (pt *pnlPriceTracker) Close() {
	if pt.cancel != nil {
		pt.cancel()
		pt.wg.Wait()
	}
}
