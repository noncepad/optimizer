package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/logger"
	"github.com/alecthomas/kong"
	"github.com/joho/godotenv"
)

// defaultBotMarketID is overridden at build time via -ldflags "-X main.defaultBotMarketID=<key>".
var defaultBotMarketID = "6VQk8GA84p7zZSyL8XtX6oVd3Vp4EJ5hoUenKoC3fHSf"

type CLI struct {
	Verbose          bool                `short:"v" env:"VERBOSE" help:"Enable debug-level logging."`
	StateURL         string              `option:"state" help:"state url."`
	CPUProfile       string              `option:"cpuprofile" help:"write a pprof CPU profile to this file."`
	CPUProfileTime   time.Duration       `option:"cpuprofiletime" default:"20s" help:"stop and flush the CPU profile after this long, regardless of how the command itself ends."`
	Version          VersionCmd          `cmd:"version" help:"Print version."`
	Arb              ArbCmd              `cmd:"arb" help:"Run arbv1."`
	DownloadArb      DownloadArbCmd      `cmd:"arb" help:"Run arbv1."`
	Perp             PerpCmd             `cmd:"perp" help:"Run perpfundingv1."`
	Testperp         TestPerpCmd         `cmd:"testperp" help:"Run testperpv1 (real-transaction Solend/Kamino deposit/withdraw smoke test)."`
	TestLatency      TestLatencyLiteV1Cmd  `cmd:"test-latency" help:"Run testperplatencyv1 (real-transaction, 100x-cycled deposit/withdraw latency test -- use --protocol to scope to one protocol)."`
	Balance          BalanceCmd          `cmd:"balance" help:"Get the balance for the trading wallet."`
	Summary          SummaryCmd          `cmd:"summary" help:"Print a summary of what's in prefetch.db."`
	Dashboard        DashboardCmd        `cmd:"dashboard" help:"Serve a local HTML dashboard for prefetch.db."`
	Harness          HarnessCmd          `cmd:"harness" help:"Print known-correct answers for the harness question set against prefetch.db."`
	WatchBalances    WatchBalancesCmd    `cmd:"watch-balances" help:"Stream bot balance snapshots (priced via Jupiter) into portfolio.db."`
	WatchPnl         WatchPnLCmd         `cmd:"watch-pnl" help:"Poll the trading wallet's balances (priced via Jupiter) into prefetch.db for mark-to-market PnL."`
	PnlBetween       PnlBetweenCmd       `cmd:"pnl-between" help:"Print per-mint PnL for the trading wallet between two timestamps, from prefetch.db (populated by watch-pnl)."`
	WatchLstYield    WatchLstYieldCmd    `cmd:"watch-lst-yield" help:"Sample real LST (37 real candidates) exchange rates into prefetch.db for staking-yield estimation."`
	WatchObligations WatchObligationsCmd `cmd:"watch-obligations" help:"Poll the trading wallet's own Solend/Kamino lending obligations (pair/directional/hawkes) into prefetch.db."`
	LeveragedLoop    LeveragedLoopCmd    `cmd:"leveraged-loop" help:"Run leveragedloopv1 (Phase 2: a single jitoSOL/USDC Kamino leverage loop, manual trigger only)."`
	MultiModel       MultiModelCmd       `cmd:"multimodel" help:"Run multimodelv1 (PLAN-1.md Phase 5 sub-phase 5a: idle-only -- wallet + market-data subscriptions, no decision/execution logic yet)."`
	Alt              AltCmd              `cmd:"alt" help:"Analyze the trading wallet's recent transaction history and (unless --dry-run=false) create/populate an on-chain Address Lookup Table, persisting it into prefetch.db."`
	Sweep            SweepCmd            `cmd:"sweep" help:"Sweep the index-1 child wallet's SOL/SPL token balances back to the parent fee-payer, via harness.Treasury/Budget.Close."`
	Compare          CompareCmd          `cmd:"compare" help:"Upload testperplatencyv1 to two validator pipelines via brain.Hook.Request and compare their native-transfer write-delay latency."`
	// Kong derives a command's name from its field name (kebab-cased),
	// not from the `cmd` tag's value (that tag is just a boolean marker).
	// `Latencyreport` is spelled as one un-capitalized-internally word on
	// purpose so kebab-casing doesn't insert a hyphen into the command
	// name.
	Latencyreport LatencyReportCmd `cmd:"latencyreport" help:"Turn a native-transfer latency run's log (testperplatencyv1/testlatencylitev1) into an HTML report."`
}

type VersionCmd struct{}

func (c *VersionCmd) Run() error {
	fmt.Println(common.GetLocalVersion())
	return nil
}

func main() {
	os.Exit(run())
}

// run holds all of main's logic so that every defer (notably the CPU
// profile's stop/flush) runs on every exit path -- os.Exit skips defers
// entirely, so it must only ever be called once, here, after run returns.
func run() int {
	// Best-effort: makes JUPITER_API_KEY (and anything else in .env) show
	// up via os.Getenv/kong's env: tag without the caller having to export
	// it manually. Silently a no-op if there's no .env in the cwd.
	_ = godotenv.Load()
	// PROGRAM_SOLPIPE overrides cba.ProgramID -- see state.ProgramSetByEnv's
	// doc comment for why this is required in practice: the compiled-in
	// default doesn't match the real mainnet Solpipe program, which
	// silently breaks Market/Pipeline/Payout/BidList/PeriodRing dispatch
	// for every bot mode until it's set. Must run after godotenv.Load
	// above so a .env-provided value is picked up too.
	if err := state.ProgramSetByEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
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
		return 1
	}
	if 0 < len(cli.CPUProfile) {
		f, err := os.Create(cli.CPUProfile)
		if err != nil {
			rc.Cancel(err)
			fmt.Fprintf(os.Stderr, "error: %s\n", err)
			return 1
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			rc.Cancel(err)
			fmt.Fprintf(os.Stderr, "error: %s\n", err)
			return 1
		}
		var stopOnce sync.Once
		stopProfile := func() {
			stopOnce.Do(func() {
				pprof.StopCPUProfile()
				_ = f.Close()
			})
		}
		defer stopProfile()
		// A long-running or hung command (e.g. one killed by SIGKILL, or
		// stuck past a shell's `timeout`) never reaches the deferred stop
		// above, which would otherwise lose the whole profile -- so stop
		// and flush unconditionally on this timer instead of relying on
		// the command's own exit path.
		go func() {
			select {
			case <-rc.Ctx.Done():
			case <-time.After(cli.CPUProfileTime):
			}
			stopProfile()
		}()
	}
	rc.StateURL = cli.StateURL
	err := ctx.Run()
	rc.Cancel(err)
	rc.Wait.Wait()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	return 0
}
