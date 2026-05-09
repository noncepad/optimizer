package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	mothership "git.noncepad.com/pkg/bot/solpipe/bidder/manager"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	sgo "git.noncepad.com/pkg/solana-go"
	"git.noncepad.com/pkg/solpipe-util/logger"
	"github.com/alecthomas/kong"
	"github.com/noncepad/optimizer/brain/simple"
)

// defaultBotMarketID is overridden at build time via -ldflags "-X main.defaultBotMarketID=<key>".
var defaultBotMarketID = "6VQk8GA84p7zZSyL8XtX6oVd3Vp4EJ5hoUenKoC3fHSf"

type CLI struct {
	Verbose bool       `short:"v" env:"VERBOSE" help:"Enable debug-level logging."`
	Version VersionCmd `cmd:"version" help:"Print version."`
	Run     RunCmd     `cmd:"run" help:"Run the optimizer mothership."`
}

type VersionCmd struct{}

func (c *VersionCmd) Run() error {
	fmt.Println(common.GetLocalVersion())
	return nil
}

type RunCmd struct {
	ProxyURL      string `option:"proxy"                              help:"proxy server address (tcp://host:port or unix:///path)."`
	ManagerURL    string `option:"manager"                             help:"manage server address (tcp://host:port or unix:///path)."`
	LocalFeePayer string `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	Echo          bool   `name:"echo" env:"ECHO" help:"enable echo mode in helloworldv1"`
}

func (c *RunCmd) Run() error {
	localFeePayer, err := sgo.PrivateKeyFromSolanaKeygenFile(c.LocalFeePayer)
	if err != nil {
		return fmt.Errorf("failed to load authorizer: %s", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dialer, err := bidder.CreateDialer(ctx, localFeePayer)
	if err != nil {
		return fmt.Errorf("failed to create dialer: %s", err)
	}

	ms, err := mothership.Create(ctx, dialer, simple.Create(ctx, cancel))
	if err != nil {
		return fmt.Errorf("create mothership: %w", err)
	}

	return <-ms.CloseSignal()
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli, kong.Name("optimizer"),
		kong.Description("Catscope optimizer mothership — coordinates trading bots across Solana validators."),
		kong.UsageOnError(),
		kong.Vars{"bot_market_id_default": defaultBotMarketID},
	)
	if err := logger.Set(cli.Verbose); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
	if err := ctx.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}
