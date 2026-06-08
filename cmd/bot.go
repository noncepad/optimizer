package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	mothership "git.noncepad.com/pkg/bot/solpipe/bidder/manager"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/optimizer/brain/helloworldv1"
	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	sgo "github.com/gagliardetto/solana-go"
)

type RunCmd struct {
	ParentKey string `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	Orca      bool   `option:"orca" help:"add orca"`
	Mode      string `name:"mode" env:"MODE" help:"set MODE to helloworldv1 or bouncerv1"`
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

	var botImage *prefetch.BotImage
	var b brain.Brain
	switch r.Mode {
	case "helloworldv1":
		botConfig := new(helloworldv1.Configuration)
		if r.Orca {
			pf, err := prefetch.Create(rc.Ctx, rc.Wait, dialer.State())
			if err != nil {
				return fmt.Errorf("prefetcher setup failed: %s", err)
			}
			o1, err := orca.Create(ctx, dialer.State())
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
		botConfig.BotImage = botImage.Path()
		b = helloworldv1.Create(ctx, cancel, parentKey, botConfig)
		//	case "bouncerv1":
		//		b = bouncerv1.Create(ctx, cancel, parentKey)
	default:
		return fmt.Errorf("unknown mode %s", r.Mode)
	}
	ms, err := mothership.Create(ctx, dialer, b)
	if err != nil {
		return fmt.Errorf("create mothership: %w", err)
	}

	select {
	case <-time.After(8 * time.Minute):
	case err = <-ms.CloseSignal():
	}
	// make sure the bot image is not deleted
	_ = botImage
	return err
}
