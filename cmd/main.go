package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/solpipe-util/logger"
	"github.com/alecthomas/kong"
)

// defaultBotMarketID is overridden at build time via -ldflags "-X main.defaultBotMarketID=<key>".
var defaultBotMarketID = "6VQk8GA84p7zZSyL8XtX6oVd3Vp4EJ5hoUenKoC3fHSf"

type CLI struct {
	Verbose  bool       `short:"v" env:"VERBOSE" help:"Enable debug-level logging."`
	StateURL string     `option:"state" help:"state url."`
	Version  VersionCmd `cmd:"version" help:"Print version."`
	Run      RunCmd     `cmd:"run" help:"Run the optimizer mothership."`
	Balance  BalanceCmd `cmd:"balance" help:"Get the balance for the trading wallet."`
}

type VersionCmd struct{}

func (c *VersionCmd) Run() error {
	fmt.Println(common.GetLocalVersion())
	return nil
}

func main() {
	rc := new(RunConfig)
	signalC := make(chan os.Signal, 1)
	signal.Notify(signalC, os.Interrupt, syscall.SIGTERM)

	rc.Ctx, rc.Cancel = context.WithCancelCause(context.Background())
	rc.Wait = &sync.WaitGroup{}
	go func() {
		doneC := rc.Ctx.Done()
		var err2 error
		select {
		case <-doneC:
		case s := <-signalC:
			err2 = fmt.Errorf("received signal %s", s)
		}
		rc.Cancel(err2)
	}()
	var cli CLI
	ctx := kong.Parse(&cli, kong.Bind(rc), kong.Name("optimizer"),
		kong.Description("Catscope optimizer mothership — coordinates trading bots across Solana validators."),
		kong.UsageOnError(),
		kong.Vars{"bot_market_id_default": defaultBotMarketID},
	)
	if err := logger.Set(cli.Verbose); err != nil {
		rc.Cancel(err)
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
	rc.StateURL = cli.StateURL
	err := ctx.Run()
	rc.Cancel(err)
	rc.Wait.Wait()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}
