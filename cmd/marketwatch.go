package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	tea "github.com/charmbracelet/bubbletea"
)

// MarketWatchCmd uploads marketwatchv1 (a read-only market-statistics
// bot -- see optimizer/brain/marketwatchv1 and catscope-rust-bot's
// src/brain/marketwatchv1) to a validator pipeline and opens a live
// terminal dashboard (see marketwatchtui.go) showing the volatility and
// pairwise correlation the bot computes from real, live pool prices
// across a small curated symbol set (SOL/JUP/JTO/BONK/WIF/RAY, each
// independently confirmed to have real tracked liquidity -- see
// marketwatchv1.TrackedSymbols's own doc comment). This bot
// never trades -- no wallet, no signing, no transactions -- it only
// watches.
type MarketWatchCmd struct {
	ParentKey  string `arg:"fee-payer" help:"the file path to the fee payer (used only to allocate/upload the bot -- this bot never signs or sends a transaction of its own)"`
	WorkingDir string `option:"work" help:"working directory"`
}

func (r *MarketWatchCmd) Run(rc *RunConfig) error {
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(r.ParentKey)
	if err != nil {
		return fmt.Errorf("failed to load authorizer: %s", err)
	}
	ctx := rc.Ctx
	cancel := rc.Cancel
	defer cancel(nil)
	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		return fmt.Errorf("failed to create dialer: %s", err)
	}
	if len(r.WorkingDir) == 0 {
		r.WorkingDir = filepath.Join(os.Getenv("HOME"), ".optimizer")
		_ = os.Mkdir(r.WorkingDir, 0o750)
	}
	var stateClient state.Client
	if 0 < len(rc.StateURL) {
		stateAddr, err := bidder.ParseAddress(rc.StateURL)
		if err != nil {
			return fmt.Errorf("failed to parse state url: %s: %s", rc.StateURL, err)
		}
		d := state.DefaultDialer(stateAddr)
		stateClient = state.New(ctx, d, 30*time.Second)
	} else {
		stateClient = dialer.State()
	}

	prefetchDB, err := store.Open(getDBFilePath())
	if err != nil {
		return fmt.Errorf("failed to open prefetch db: %s", err)
	}
	defer func() {
		_ = prefetchDB.Close()
	}()

	entry := logger.FromContext(ctx)
	var botImage *prefetch.BotImage
	{
		pf, err := prefetch.Create(rc.Ctx, rc.Wait, stateClient, prefetchDB)
		if err != nil {
			return fmt.Errorf("prefetcher setup failed: %s", err)
		}
		// make sure to put this last so we get all of the trading pair data
		staticLiquidity := liquidity.Create(liquidity.DefaultConfig())
		botImage, err = pf.Build(ctx, os.Getenv("REPO"), staticLiquidity)
		if err != nil {
			return fmt.Errorf("failed to build bot image: %s", err)
		}
		if botImage == nil {
			return errors.New("missing bot image")
		}
	}

	hook, err := marketwatchv1.Create(ctx, cancel, parentKey, &marketwatchv1.Configuration{
		BotImage: botImage.Path(),
	})
	if err != nil {
		return fmt.Errorf("failed to create marketwatchv1 brain: %s", err)
	}
	entry.Info("marketwatch: uploading bot, waiting for handshake...")
	if _, err := mothership.Create(ctx, dialer, hook); err != nil {
		return fmt.Errorf("create mothership: %w", err)
	}

	// The TUI runs in the foreground; the bot upload/connect/stats-feed
	// above continues in mothership's own background goroutines
	// regardless -- the dashboard just shows "waiting for data..." (see
	// marketwatchtui.go) until the first MarketStats snapshot arrives.
	p := tea.NewProgram(newMarketWatchModel(hook.Store()), tea.WithAltScreen())
	_, err = p.Run()
	// make sure the bot image is not deleted while the TUI (and the bot
	// upload it may still be waiting on) is running
	_ = botImage
	return err
}
