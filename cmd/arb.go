package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	mothership "git.noncepad.com/pkg/bot/solpipe/bidder/manager"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/prefetch/kamino"
	"git.noncepad.com/pkg/optimizer/prefetch/liquidity"
	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium"
	"git.noncepad.com/pkg/optimizer/prefetch/sanctum"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

type ArbCmd struct {
	ParentKey  string `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	LogLatency string `option:"latency" help:"log latency to a file"`
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
	entry := logger.FromContext(ctx)
	var botImage *prefetch.BotImage
	var b brain.Brain
	{

		pf, err := prefetch.Create(rc.Ctx, rc.Wait, stateClient)
		if err != nil {
			return fmt.Errorf("prefetcher setup failed: %s", err)
		}
		staticOrca, err := orca.Create(ctx, stateClient, r.WorkingDir, 1)
		if err != nil {
			return fmt.Errorf("failed to get orca: %s", err)
		}
		staticRaydium, err := raydium.Create(ctx, stateClient, r.WorkingDir)
		if err != nil {
			return fmt.Errorf("failed to get orca: %s", err)
		}
		staticSanctum, err := sanctum.Create(ctx, stateClient, r.WorkingDir)
		if err != nil {
			return fmt.Errorf("failed to get orca: %s", err)
		}
		staticKamino, err := kamino.Create(ctx, stateClient, r.WorkingDir)
		if err != nil {
			return fmt.Errorf("failed to get orca: %s", err)
		}
		// make sure to put this last so we get all of the trading pair data
		staticLiquidity := liquidity.Create(liquidity.DefaultConfig())
		botImage, err = pf.Build(ctx, os.Getenv("REPO"), []prefetch.StaticLoader{staticOrca, staticRaydium, staticKamino, staticSanctum}, staticLiquidity)
		if err != nil {
			return fmt.Errorf("failed to get loader: %s", err)
		}
		if botImage == nil {
			return errors.New("missing bot image")
		}
	}
	mode := "arbv1"
	entry.Info(fmt.Sprintf("cmd - 3; doing %s", mode))
	ms, err := mothership.Create(ctx, dialer, b)
	entry.Info(fmt.Sprintf("cmd - 4; doing %s", mode))
	if err != nil {
		return fmt.Errorf("create mothership: %w", err)
	}

	entry.Info(fmt.Sprintf("cmd - 5; doing %s", mode))
	select {
	case <-time.After(8 * time.Minute):
	case err = <-ms.CloseSignal():
	}
	entry.Info(fmt.Sprintf("cmd - 6; doing %s", mode))
	// make sure the bot image is not deleted
	_ = botImage
	return err
}
