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
	"git.noncepad.com/pkg/optimizer/brain/arbv1"
	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/prefetch/liquidity"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

type ArbCmd struct {
	ParentKey  string `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	LogLatency string `option:"latency" help:"log latency to a file"`
	Telnet     string `option:"telnet" help:"have a telnet endpoint to allow interactions with the optimizer"`
	WorkingDir string `option:"work" help:"working directory"`
}

func (r *ArbCmd) Run(rc *RunConfig) error {
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
	// ArbCmd only ever reads the prefetch database that DownloadArbCmd
	// already populated at this fixed path — it does not fetch on-chain
	// dex state itself, regardless of r.WorkingDir.
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
		// make sure to put this last so we get all of the trading pair data
		staticLiquidity := liquidity.Create(liquidity.DefaultConfig())
		botImage, err = pf.Build(ctx, os.Getenv("REPO"), staticLiquidity)
		if err != nil {
			return fmt.Errorf("failed to get loader: %s", err)
		}
		if botImage == nil {
			return errors.New("missing bot image")
		}
	}
	// b was previously left as a nil brain.Brain (zero-value interface) --
	// mothershipSolpipe.Init calls ms.init.brain.Init(...) unconditionally,
	// so a nil brain here is a guaranteed nil-interface-method-call panic
	// the moment the graph hook starts up. arbv1.Create wires up the real
	// implementation: it uploads botImage (the wasm binary just compiled
	// above) to the validator and drives the handshake.
	b, err := arbv1.Create(ctx, cancel, parentKey, &arbv1.Configuration{
		BotImage:        botImage.Path(),
		LatencyFilePath: r.LogLatency,
	}, prefetchDB.Raw())
	if err != nil {
		return fmt.Errorf("failed to create arbv1 brain: %s", err)
	}
	mode := "arbv1"
	entry.Info(fmt.Sprintf("cmd - 3; doing %s", mode))
	startBundlerTipBroadcaster(ctx, entry, prefetchDB.Raw(), b.SendBundlerTipUpdate)
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
	// make sure the bot image is not deleted
	_ = botImage
	return err
}
