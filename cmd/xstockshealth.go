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
	"git.noncepad.com/pkg/optimizer/brain/xstockshealthv1"
	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/prefetch/liquidity"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/solana-foundation/solana-go/v2"
)

// XstocksHealthCmd mirrors TestPerpCmd's allocation/prefetch/upload flow
// exactly, except it wires up xstockshealthv1.Create instead of
// testperpv1.Create, which uploads the WASM bot image with
// MODE=xstockshealthv1 selected instead of MODE=testperpv1. See
// catscope-rust-bot's src/brain/xstockshealthv1 doc comment for what that
// mode actually does (watches Kamino xStocks obligations/reserves and
// logs a health-factor board -- a passive watcher, not a trading
// strategy, sends no transactions).
type XstocksHealthCmd struct {
	ParentKey     string `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	WorkingDir    string `option:"work" help:"working directory"`
	DashboardAddr string `option:"dashboard-addr" help:"address for the local health-board page to listen on (default 127.0.0.1:8091)"`
}

func (r *XstocksHealthCmd) Run(rc *RunConfig) error {
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
	// XstocksHealthCmd only ever reads the prefetch database that
	// DownloadArbCmd already populated at this fixed path -- it does not
	// fetch on-chain dex state itself, regardless of r.WorkingDir. Same
	// as TestPerpCmd; this mode's Rust side has no use for the resulting
	// *sql.DB (no account-usage/target-allocation/LST-APY persistence),
	// but the bot image build still needs the same static liquidity
	// router data every other mode compiles in.
	prefetchDB, err := store.Open(getDBFilePath())
	if err != nil {
		return fmt.Errorf("failed to open prefetch db: %s", err)
	}
	entry := logger.FromContext(ctx)
	var botImage *prefetch.BotImage
	{
		pf, err := prefetch.Create(rc.Ctx, rc.Wait, stateClient, prefetchDB)
		if err != nil {
			return fmt.Errorf("prefetcher setup failed: %s", err)
		}
		defer func() {
			_ = prefetchDB.Close()
		}()
		staticLiquidity := liquidity.Create(liquidity.DefaultConfig())
		botImage, err = pf.Build(ctx, os.Getenv("REPO"), staticLiquidity)
		if err != nil {
			return fmt.Errorf("failed to get loader: %s", err)
		}
		if botImage == nil {
			return errors.New("missing bot image")
		}
	}
	b, err := xstockshealthv1.Create(ctx, cancel, parentKey, &xstockshealthv1.Configuration{
		BotImage:      botImage.Path(),
		DashboardAddr: r.DashboardAddr,
	})
	if err != nil {
		return fmt.Errorf("failed to create xstockshealthv1 brain: %s", err)
	}
	mode := "xstockshealthv1"
	entry.Info(fmt.Sprintf("cmd - 3; doing %s", mode))
	// No startBundlerTipBroadcaster call here, unlike TestPerpCmd -- this
	// mode's Hook has no SendBundlerTipUpdate (it sends no transactions,
	// so it never needs a live tip), and that helper expects a real
	// send func, not a nil one.
	ms, err := mothership.Create(ctx, dialer, b)
	entry.Info(fmt.Sprintf("cmd - 4; doing %s", mode))
	if err != nil {
		return fmt.Errorf("create mothership: %w", err)
	}

	entry.Info(fmt.Sprintf("cmd - 5; doing %s", mode))
	select {
	case <-time.After(3 * 60 * time.Minute):
	case err = <-ms.CloseSignal():
	}
	entry.Info(fmt.Sprintf("cmd - 6; doing %s", mode))
	_ = botImage
	return err
}
