package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	mothership "git.noncepad.com/pkg/bot/solpipe/bidder/manager"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/brain/leveragedloopv1"
	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/prefetch/liquidity"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// LeveragedLoopCmd mirrors TestPerpCmd's allocation/prefetch/upload/
// handshake flow exactly (see that file), wiring up
// leveragedloopv1.Create instead -- see that package's doc comment for
// what this mode actually does (Phase 2 of
// catscope-rust-bot's leveraged_yield_farming_plan.md: a single,
// conservative jitoSOL-collateral/USDC-debt Kamino leverage loop).
//
// --open and --close are the only way this mode ever sends a real
// TriggerOpen/TriggerClose -- manual/explicit, per the plan's own
// requirement. Sending one is real money: --open opens a real leveraged
// position sized at the given USD notional; --close unwinds whatever is
// currently open. Neither fires automatically -- omit both to just run
// the bot idle (e.g. to let it pick back up a position from a previous
// run's real on-chain state without touching it).
type LeveragedLoopCmd struct {
	ParentKey              string        `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	WorkingDir             string        `option:"work" help:"working directory"`
	Open                   float64       `option:"open" help:"if > 0, send a real TriggerOpen for this USD notional shortly after the bot connects -- opens a real leveraged position."`
	OpenAuto               float64       `option:"open-auto" help:"if > 0, send a real TriggerOpenAuto for this USD notional shortly after the bot connects -- same as --open, but the bot picks the LST candidate itself via its own Time-Expanded DAG instead of always jitoSOL, and declines (opens nothing) if the DAG says holding USDC beats every real candidate right now."`
	Close                  bool          `option:"close" help:"send a real TriggerClose shortly after the bot connects -- unwinds the current position, if any."`
	RecoverToken           string        `option:"recover-token" help:"mint pubkey, e.g. a stray token from a fixed bug -- send a real TriggerRecoverToken shortly after the bot connects, swapping the wallet's entire real balance of that mint back to USDC, independent of the loop's current phase."`
	RedepositUsdc          float64       `option:"redeposit-usdc" help:"if > 0, send a real TriggerRedepositUsdc for this USD notional shortly after the bot connects -- swaps that much USDC to jitoSOL and deposits it as additional Kamino collateral, independent of the loop's current phase."`
	TestBundler            bool          `option:"test-bundler" help:"send a real TriggerTestBundler shortly after the bot connects -- proves the Astralane dual-transaction bundler pipeline end-to-end with a trivial, inert instruction. First run bootstraps the wallet's durable-nonce account (a real, one-time transaction); a later run sends the real dual-transaction bundle."`
	TestBatch              bool          `option:"test-batch" help:"send a real TriggerTestBatch shortly after the bot connects -- forces the generic transactionprocessor::batch host import (not send_bundler_pair's durable-nonce pair) with two separate, deliberately inert transactions, tagged for whichever bundler this build was compiled with."`
	EnableBasisTrading     bool          `option:"enable-basis-trading" help:"send a real TriggerEnableBasisTrading shortly after the bot connects -- one-time opt-in for the real Phoenix-perp-funding-vs-Kamino-rate basis trade (a second strategy, fully independent of the leverage loop, own id=1 Kamino obligation). Runs autonomously every real funding epoch once enabled."`
	CloseAllBasisPositions bool          `option:"close-all-basis-positions" help:"send a real TriggerCloseAllBasisPositions shortly after the bot connects -- force-closes every currently-open basis-trade position, independent of the cycle's own logic. Manual safety valve."`
	TriggerAt              time.Duration `option:"trigger-at" default:"20s" help:"how long to wait after the bot connects before sending --open/--open-auto/--close/--recover-token/--redeposit-usdc/--test-bundler/--test-batch/--enable-basis-trading/--close-all-basis-positions -- gives real Kamino reserve/oracle data time to load; the trigger is safe to send early regardless (the state machine won't act until its own on-chain reads are ready), this just avoids an immediate no-op retry."`
}

func (r *LeveragedLoopCmd) Run(rc *RunConfig) error {
	n := 0
	for _, b := range []bool{r.Open > 0, r.OpenAuto > 0, r.Close, len(r.RecoverToken) > 0, r.RedepositUsdc > 0, r.TestBundler, r.TestBatch, r.EnableBasisTrading, r.CloseAllBasisPositions} {
		if b {
			n++
		}
	}
	if n > 1 {
		return errors.New("--open, --open-auto, --close, --recover-token, --redeposit-usdc, --test-bundler, --test-batch, --enable-basis-trading, and --close-all-basis-positions are mutually exclusive -- pick one")
	}
	var recoverMint sgo.PublicKey
	if len(r.RecoverToken) > 0 {
		var err error
		recoverMint, err = sgo.PublicKeyFromBase58(r.RecoverToken)
		if err != nil {
			return fmt.Errorf("bad --recover-token mint: %s", err)
		}
	}
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
	b, err := leveragedloopv1.Create(ctx, cancel, parentKey, &leveragedloopv1.Configuration{
		BotImage: botImage.Path(),
	}, prefetchDB.Raw())
	if err != nil {
		return fmt.Errorf("failed to create leveragedloopv1 brain: %s", err)
	}
	mode := "leveragedloopv1"
	entry.Info(fmt.Sprintf("cmd - 3; doing %s", mode))
	startBundlerTipBroadcaster(ctx, entry, prefetchDB.Raw(), b.SendBundlerTipUpdate)
	ms, err := mothership.Create(ctx, dialer, b)
	entry.Info(fmt.Sprintf("cmd - 4; doing %s", mode))
	if err != nil {
		return fmt.Errorf("create mothership: %w", err)
	}
	entry.Info(fmt.Sprintf("cmd - 5; doing %s", mode))

	// isBotNotConnectedYet is this mode's sendTriggerWithRetry `retryable`
	// check -- every bot-mode package (leveragedloopv1, testperpv1, ...)
	// defines its own ErrBotNotConnectedYet sentinel, so this can't be
	// hardcoded inside sendTriggerWithRetry itself.
	isBotNotConnectedYet := func(err error) bool {
		return errors.Is(err, leveragedloopv1.ErrBotNotConnectedYet)
	}
	if r.Open > 0 {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info(fmt.Sprintf("leveragedloop: sending real TriggerOpen ($%.2f notional)", r.Open))
			sendTriggerWithRetry(ctx, entry, "leveragedloop: TriggerOpen", func() error { return b.SendTriggerOpen(r.Open) }, isBotNotConnectedYet)
		}()
	} else if r.OpenAuto > 0 {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info(fmt.Sprintf("leveragedloop: sending real TriggerOpenAuto ($%.2f notional)", r.OpenAuto))
			sendTriggerWithRetry(ctx, entry, "leveragedloop: TriggerOpenAuto", func() error { return b.SendTriggerOpenAuto(r.OpenAuto) }, isBotNotConnectedYet)
		}()
	} else if r.Close {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info("leveragedloop: sending real TriggerClose")
			sendTriggerWithRetry(ctx, entry, "leveragedloop: TriggerClose", b.SendTriggerClose, isBotNotConnectedYet)
		}()
	} else if len(r.RecoverToken) > 0 {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info(fmt.Sprintf("leveragedloop: sending real TriggerRecoverToken (%s)", recoverMint))
			sendTriggerWithRetry(ctx, entry, "leveragedloop: TriggerRecoverToken", func() error { return b.SendTriggerRecoverToken(recoverMint) }, isBotNotConnectedYet)
		}()
	} else if r.RedepositUsdc > 0 {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info(fmt.Sprintf("leveragedloop: sending real TriggerRedepositUsdc ($%.2f notional)", r.RedepositUsdc))
			sendTriggerWithRetry(ctx, entry, "leveragedloop: TriggerRedepositUsdc", func() error { return b.SendTriggerRedepositUsdc(r.RedepositUsdc) }, isBotNotConnectedYet)
		}()
	} else if r.TestBundler {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info("leveragedloop: sending real TriggerTestBundler")
			sendTriggerWithRetry(ctx, entry, "leveragedloop: TriggerTestBundler", b.SendTriggerTestBundler, isBotNotConnectedYet)
		}()
	} else if r.TestBatch {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info("leveragedloop: sending real TriggerTestBatch")
			sendTriggerWithRetry(ctx, entry, "leveragedloop: TriggerTestBatch", b.SendTriggerTestBatch, isBotNotConnectedYet)
		}()
	} else if r.EnableBasisTrading {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info("leveragedloop: sending real TriggerEnableBasisTrading")
			sendTriggerWithRetry(ctx, entry, "leveragedloop: TriggerEnableBasisTrading", b.SendTriggerEnableBasisTrading, isBotNotConnectedYet)
		}()
	} else if r.CloseAllBasisPositions {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info("leveragedloop: sending real TriggerCloseAllBasisPositions")
			sendTriggerWithRetry(ctx, entry, "leveragedloop: TriggerCloseAllBasisPositions", b.SendTriggerCloseAllBasisPositions, isBotNotConnectedYet)
		}()
	}

	select {
	case <-time.After(3 * 60 * time.Minute):
	case err = <-ms.CloseSignal():
	}
	entry.Info(fmt.Sprintf("cmd - 6; doing %s", mode))
	_ = botImage
	return err
}

// triggerRetryMaxWait bounds how long sendTriggerWithRetry keeps retrying
// a trigger that's failing only because the bot hasn't connected yet.
// Real, live-confirmed motivation (2026-08-27): a Solpipe pipeline
// reconnect (same known quirk documented in leveragedloopv1's own doc
// comments) can take up to ~5 minutes to self-recover; before this
// retry existed, a trigger firing during that window crashed the whole
// process with a nil-pointer panic instead of just waiting.
const triggerRetryMaxWait = 5 * time.Minute

// sendTriggerWithRetry calls send, retrying every 5s while its error
// satisfies retryable (e.g. `errors.Is(err, somepkg.ErrBotNotConnectedYet)`
// -- every bot-mode package defines its own sentinel of that name, so
// callers pass the check rather than this function hardcoding one
// package's), until it succeeds, ctx is cancelled, or triggerRetryMaxWait
// elapses. Any other error is real (not a connection-timing race) and
// reported immediately, no retry. label is just this call's log prefix
// (e.g. "leveragedloop", "testperp"), independent of the trigger name
// already baked into the caller's own message.
func sendTriggerWithRetry(ctx context.Context, entry *slog.Logger, label string, send func() error, retryable func(error) bool) {
	deadline := time.Now().Add(triggerRetryMaxWait)
	for {
		err := send()
		if err == nil {
			entry.Info(fmt.Sprintf("%s sent", label))
			return
		}
		if !retryable(err) {
			entry.Error(fmt.Sprintf("%s failed: %s", label, err))
			return
		}
		if time.Now().After(deadline) {
			entry.Error(fmt.Sprintf("%s failed: bot still not connected after %s, giving up", label, triggerRetryMaxWait))
			return
		}
		entry.Info(fmt.Sprintf("%s: bot not connected yet, retrying in 5s", label))
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}
