package main

// THROWAWAY verification harness -- reuses marketwatch.go's upload wiring
// minus the TUI to confirm live, on-chain that a fresh session's
// PriceHistory never contains an anomalous SOL/USDC tick (the "high 175"
// report) and that the Bollinger lower band never goes negative (the
// "low -0.1" report, now fixed in taindicators.go). Delete this
// directory once verification is done; it is not part of the shipped
// CLI.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	mothership "git.noncepad.com/pkg/bot/solpipe/bidder/manager"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/brain/marketwatchv1"
	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/prefetch/liquidity"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/solana-foundation/solana-go/v2"
)

func main() {
	os.Exit(run())
}

func run() int {
	feePayerPath := os.Getenv("FEE_PAYER")
	if feePayerPath == "" {
		feePayerPath = filepath.Join(os.Getenv("HOME"), "work", "optimizer", "fee-payer.json")
	}
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(feePayerPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load fee payer: %s\n", err)
		return 1
	}
	if err := state.ProgramSetByEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	runFor := 10 * time.Minute
	if v := os.Getenv("VERIFY_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			runFor = d
		}
	}

	ctx, cancel := context.WithTimeoutCause(context.Background(), runFor, errors.New("verification window elapsed"))
	defer cancel()
	signalC := make(chan os.Signal, 1)
	signal.Notify(signalC, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-ctx.Done():
		case <-signalC:
			cancel()
		}
	}()

	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create dialer: %s\n", err)
		return 1
	}

	dbPath := filepath.Join(os.Getenv("HOME"), ".optimizer", "prefetch.db")
	prefetchDB, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open prefetch db: %s\n", err)
		return 1
	}
	defer func() { _ = prefetchDB.Close() }()

	entry := logger.FromContext(ctx)
	wait := &sync.WaitGroup{}
	var botImage *prefetch.BotImage
	{
		pf, err := prefetch.Create(ctx, wait, dialer.State(), prefetchDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "prefetcher setup failed: %s\n", err)
			return 1
		}
		staticLiquidity := liquidity.Create(liquidity.DefaultConfig())
		botImage, err = pf.Build(ctx, os.Getenv("REPO"), staticLiquidity)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to build bot image: %s\n", err)
			return 1
		}
		if botImage == nil {
			fmt.Fprintln(os.Stderr, "missing bot image")
			return 1
		}
	}

	cancelCause := func(err error) { cancel() }
	hook, err := marketwatchv1.Create(ctx, cancelCause, parentKey, &marketwatchv1.Configuration{
		BotImage: botImage.Path(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create marketwatchv1 brain: %s\n", err)
		return 1
	}
	entry.Info("tmpverifymw: uploading bot, waiting for handshake...")
	if _, err := mothership.Create(ctx, dialer, hook); err != nil {
		fmt.Fprintf(os.Stderr, "create mothership: %s\n", err)
		return 1
	}

	solIdx := -1
	for i, s := range marketwatchv1.TrackedSymbols {
		if s == "SOL" {
			solIdx = i
		}
	}

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	var samples int
	for {
		select {
		case <-ctx.Done():
			hist := hook.Store().PriceHistory(solIdx)
			minH, maxH := -1.0, -1.0
			for _, p := range hist {
				if minH < 0 || p < minH {
					minH = p
				}
				if p > maxH {
					maxH = p
				}
			}
			fmt.Printf("=== verification window done: %d samples, %d history points, PriceHistory range [%.4f, %.4f] ===\n",
				samples, len(hist), minH, maxH)
			return 0
		case <-ticker.C:
			snap, ok := hook.Store().Latest()
			if !ok {
				fmt.Println("waiting for first snapshot...")
				continue
			}
			if solIdx >= 0 {
				hist := hook.Store().PriceHistory(solIdx)
				minH, maxH := -1.0, -1.0
				for _, p := range hist {
					if minH < 0 || p < minH {
						minH = p
					}
					if p > maxH {
						maxH = p
					}
				}
				samples++
				fmt.Printf("[%s] SOL/USDC = %.4f  (vol=%.6f)  history_len=%d history_range=[%.4f, %.4f]\n",
					time.Now().Format(time.TimeOnly), snap.Prices[solIdx], snap.Volatilities[solIdx], len(hist), minH, maxH)
			}
		}
	}
}
