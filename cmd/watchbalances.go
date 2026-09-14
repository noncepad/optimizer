package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/optimizer/portfolio"
	pricefeed "git.noncepad.com/pkg/optimizer/prefetch/price-feed"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// farFuture stands in for "no upper bound" on the bidder log query -- Log()
// takes a bounded [start, finish) window, not an open-ended tail, so this
// just pushes finish far enough out that it never becomes the limiting
// factor.
const farFuture = 100 * 365 * 24 * time.Hour

// idleReconnect is how long watch-balances waits without seeing a single
// event (balance or otherwise) before assuming the log stream died and
// reconnecting. The bidder manager's Log() stream has no clean "this
// window is done" signal on our side (see drainBalances), so this is the
// only way to notice a silently dropped connection.
const idleReconnect = 90 * time.Second

func getPortfolioDBFilePath() string {
	return filepath.Join(os.Getenv("HOME"), ".optimizer", "portfolio.db")
}

// WatchBalancesCmd streams ActionLogBalance events from the bidder daemon
// and records one priced snapshot per event into portfolio.db. There is no
// per-trade log available here -- only balance snapshots -- so this is the
// entire data source PnL is computed from (see the portfolio package).
type WatchBalancesCmd struct {
	ParentKey     string        `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	PortfolioPath string        `option:"portfolio-db" help:"path to portfolio.db (default ~/.optimizer/portfolio.db)."`
	JupiterAPIKey string        `option:"jupiter-key" env:"JUPITER_API_KEY" help:"Jupiter Price API key (falls back to JUPITER_API_KEY, including from .env)."`
	PollInterval  time.Duration `option:"poll" default:"10s" help:"how often to pull new balance log entries."`
	Backfill      time.Duration `option:"backfill" default:"24h" help:"on first run, how far back to pull balance history."`
}

func (r *WatchBalancesCmd) Run(rc *RunConfig) error {
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(r.ParentKey)
	if err != nil {
		return fmt.Errorf("failed to load authorizer: %s", err)
	}
	if len(r.PortfolioPath) == 0 {
		r.PortfolioPath = getPortfolioDBFilePath()
		_ = os.MkdirAll(filepath.Dir(r.PortfolioPath), 0o750)
	}
	if len(r.JupiterAPIKey) == 0 {
		return fmt.Errorf("missing Jupiter API key: pass --jupiter-key or set JUPITER_API_KEY (e.g. in .env)")
	}
	ctx := rc.Ctx
	cancel := rc.Cancel
	defer cancel(nil)

	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		return fmt.Errorf("failed to create dialer: %s", err)
	}
	bm, err := dialer.Manager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create bidder manager: %s", err)
	}

	db, err := portfolio.Open(r.PortfolioPath)
	if err != nil {
		return fmt.Errorf("failed to open portfolio db: %s", err)
	}
	defer func() {
		_ = db.Close()
	}()

	pt := newPriceTracker(r.JupiterAPIKey)
	defer pt.Close()

	entry := logger.FromContext(ctx)
	cursor := time.Now().Add(-r.Backfill)
	fmt.Printf("watch-balances: writing to %s (backfill %s, reconnect after %s idle)\n",
		r.PortfolioPath, r.Backfill, idleReconnect)
	for {
		lcg := bm.Log(ctx, entry, cursor, time.Now().Add(farFuture))
		newCursor, n, err := drainBalances(ctx, lcg, db, pt, cursor)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		cursor = newCursor
		fmt.Printf("watch-balances: stream idle after %d balance event(s), reconnecting from %s\n",
			n, cursor.Format(time.RFC3339))
	}
}

// drainBalances consumes one Log() stream until it's been idle for
// idleReconnect, recording every ActionLogBalance as a priced snapshot.
// It returns the timestamp of the last event actually processed (across
// all channels), so the caller can resume the next window from there --
// and the count of balance events recorded, for the reconnect log line.
func drainBalances(ctx context.Context, lcg bidder.LogChannelGroup, db *portfolio.DB, pt *priceTracker, cursor time.Time) (time.Time, int, error) {
	n := 0
	for {
		select {
		case <-ctx.Done():
			return cursor, n, nil
		case err, ok := <-lcg.ErrorC:
			if ok && err != nil {
				return cursor, n, fmt.Errorf("balance log stream: %w", err)
			}
		case ev, ok := <-lcg.BalanceC:
			if !ok {
				return cursor, n, nil
			}
			pt.Track(ctx, ev.Mint)
			decimals, price := pt.Lookup(ev.Mint)
			if err := db.Insert(portfolio.Snapshot{
				Time: ev.Source.Time, Market: ev.Source.Market, Pipeline: ev.Source.Pipeline,
				Account: ev.Account, Mint: ev.Mint, Balance: ev.Balance,
				Decimals: decimals, USDPrice: price,
			}); err != nil {
				return cursor, n, err
			}
			if ev.Source.Time.After(cursor) {
				cursor = ev.Source.Time
			}
			n++
		// The other channels are drained so they never fill their 100-slot
		// buffer and block the sender's goroutine, even though this
		// command doesn't otherwise act on uptime/proxy/state/tx/usage/
		// summary events.
		case <-lcg.UptimeC:
		case <-lcg.ProxyEventC:
		case <-lcg.StateC:
		case <-lcg.TxC:
		case <-lcg.UsageC:
		case <-lcg.SummaryC:
		case <-time.After(idleReconnect):
			return cursor, n, nil
		}
	}
}

// priceTracker keeps a Jupiter price poller running for exactly the set of
// mints seen so far, restarting it (pricefeed.Poller's mint list is fixed
// at construction) whenever a new mint shows up. Track/Lookup are only
// ever called from watch-balances' single balance-consuming loop, so no
// locking is needed around the poller lifecycle itself -- only around the
// price cache, which the poller's own goroutine also writes to.
type priceTracker struct {
	apiKey string
	mints  map[sgo.PublicKey]struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mx     sync.RWMutex
	prices map[sgo.PublicKey]pricefeed.PriceUpdate
}

func newPriceTracker(apiKey string) *priceTracker {
	return &priceTracker{
		apiKey: apiKey,
		mints:  make(map[sgo.PublicKey]struct{}),
		prices: make(map[sgo.PublicKey]pricefeed.PriceUpdate),
	}
}

func (pt *priceTracker) Track(ctx context.Context, mint sgo.PublicKey) {
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

func (pt *priceTracker) restart(ctx context.Context, mints []sgo.PublicKey) {
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
		logger.FromContext(ctx).Error(fmt.Sprintf("watch-balances: price poller: %s", err))
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
func (pt *priceTracker) Lookup(mint sgo.PublicKey) (*uint8, *float64) {
	pt.mx.RLock()
	u, ok := pt.prices[mint]
	pt.mx.RUnlock()
	if !ok {
		return nil, nil
	}
	decimals, price := u.Decimals, u.USDPrice
	return &decimals, &price
}

func (pt *priceTracker) Close() {
	if pt.cancel != nil {
		pt.cancel()
		pt.wg.Wait()
	}
}
