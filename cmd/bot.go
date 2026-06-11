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
	"git.noncepad.com/pkg/optimizer/brain/helloworldv1"
	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

type RunCmd struct {
	ParentKey  string `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	Orca       bool   `option:"orca" help:"add orca"`
	LogLatency string `option:"latency" help:"log latency to a file"`
	WorkingDir string `option:"work" help:"working directory"`
	Mode       string `name:"mode" env:"MODE" help:"set MODE to helloworldv1 or bouncerv1"`
}

func (r *RunCmd) Run(rc *RunConfig) error {
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(r.ParentKey)
	if err != nil {
		return fmt.Errorf("failed to load authorizer: %s", err)
	}
	ctx := rc.Ctx
	cancel := rc.Cancel
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
	switch r.Mode {
	case "helloworldv1":
		entry.Info(r.Mode)
		botConfig := new(helloworldv1.Configuration)
		if r.Orca {
			pf, err := prefetch.Create(rc.Ctx, rc.Wait, stateClient)
			if err != nil {
				return fmt.Errorf("prefetcher setup failed: %s", err)
			}
			o1, err := orca.Create(ctx, stateClient, r.WorkingDir)
			if err != nil {
				return fmt.Errorf("failed to get orca: %s", err)
			}
			botImage, err = pf.Build(ctx, os.Getenv("REPO"), []prefetch.StaticLoader{o1})
			if err != nil {
				return fmt.Errorf("failed to get loader: %s", err)
			}

			if botImage == nil {
				return errors.New("missing bot image")
			}
		} else {
			return errors.New("missing bot image")
		}
		entry.Info(fmt.Sprintf("cmd - 1; doing %s: %s", r.Mode, botImage.Path()))
		botConfig.BotImage = botImage.Path()
		botConfig.LatencyFilePath = r.LogLatency
		b, err = helloworldv1.Create(ctx, cancel, parentKey, botConfig)
		entry.Info(fmt.Sprintf("cmd - 2; doing %s: %s", r.Mode, botImage.Path()))
		if err != nil {
			entry.With("err", err).Info(fmt.Sprintf("cmd - 2 - error; doing %s: %s", r.Mode, botImage.Path()))
			return fmt.Errorf("failed to create brain: %s", err)
		}
		//	case "bouncerv1":
		//		b = bouncerv1.Create(ctx, cancel, parentKey)
	default:
		return fmt.Errorf("unknown mode %s", r.Mode)
	}
	entry.Info(fmt.Sprintf("cmd - 3; doing %s", r.Mode))
	ms, err := mothership.Create(ctx, dialer, b)
	entry.Info(fmt.Sprintf("cmd - 4; doing %s", r.Mode))
	if err != nil {
		return fmt.Errorf("create mothership: %w", err)
	}

	entry.Info(fmt.Sprintf("cmd - 5; doing %s", r.Mode))
	select {
	case <-time.After(8 * time.Minute):
	case err = <-ms.CloseSignal():
	}
	entry.Info(fmt.Sprintf("cmd - 6; doing %s", r.Mode))
	// make sure the bot image is not deleted
	_ = botImage
	return err
}
