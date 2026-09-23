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
	"git.noncepad.com/pkg/optimizer/brain/testperplatencyv1"
	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/prefetch/liquidity"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// TestPerpLatencyCmd mirrors TestPerpCmd exactly -- same
// allocation/prefetch/upload flow -- except it wires up
// testperplatencyv1.Create instead of testperpv1.Create, which uploads the
// WASM bot image with MODE=testperplatencyv1 selected instead of
// MODE=testperpv1, plus TEST_PROTOCOL (see Protocol below) to scope a real
// run to just one protocol's deposit<->withdraw cycle. See
// catscope-rust-bot's src/brain/testperplatencyv1 doc comment for what
// that mode actually does (a real-transaction, CYCLE_TARGET-times-repeated
// deposit/withdraw latency test, not a trading strategy).
type TestPerpLatencyCmd struct {
	ParentKey  string `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	WorkingDir string `option:"work" help:"working directory"`
	// Protocol scopes a real run to just one protocol's deposit<->withdraw
	// cycle ("solend"/"kamino"/"marginfi") instead of the original
	// unscoped full sequence (all three protocols plus every borrow/repay
	// leg) -- see catscope-rust-bot's TestProtocol::from_env doc comment.
	// Left empty, TEST_PROTOCOL is never set and the Rust side falls back
	// to that full sequence -- almost never what a real run wants, since
	// it multiplies the real transaction fees spent by however many
	// protocols/phases are included. Start with "marginfi": its cycle
	// logic needs no dust-remainder workaround (see
	// CYCLE_DUST_RAW's doc comment on the Rust side), so it's the
	// lowest-risk protocol to validate the harness itself against first.
	// "native_lite" is a Go-side-only sentinel (see
	// testperplatencyv1/init.go's Init) selecting an experimental copy of
	// the WASM module with the DEX/lending subscription setup stripped
	// out -- otherwise behaves exactly like "native".
	Protocol string `option:"protocol" help:"which single protocol to run (solend/kamino/marginfi/native/native_lite) -- leave unset to run the original full unscoped sequence"`
}

func (r *TestPerpLatencyCmd) Run(rc *RunConfig) error {
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
	// TestPerpLatencyCmd only ever reads the prefetch database that
	// DownloadArbCmd already populated at this fixed path — it does not
	// fetch on-chain dex state itself, regardless of r.WorkingDir.
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
	// the moment the graph hook starts up. testperplatencyv1.Create wires
	// up the real implementation: it uploads botImage (the same
	// mode-agnostic wasm binary just compiled above, with
	// MODE=testperplatencyv1 and TEST_PROTOCOL=r.Protocol selected at
	// upload time) to the validator and drives the handshake.
	b, err := testperplatencyv1.Create(ctx, cancel, parentKey, &testperplatencyv1.Configuration{
		BotImage:       botImage.Path(),
		TargetProtocol: r.Protocol,
	}, prefetchDB.Raw())
	if err != nil {
		return fmt.Errorf("failed to create testperplatencyv1 brain: %s", err)
	}
	mode := "testperplatencyv1"
	entry.Info(fmt.Sprintf("cmd - 3; doing %s", mode))
	startBundlerTipBroadcaster(ctx, entry, prefetchDB.Raw(), b.SendBundlerTipUpdate)
	ms, err := mothership.Create(ctx, dialer, b)
	entry.Info(fmt.Sprintf("cmd - 4; doing %s", mode))
	if err != nil {
		return fmt.Errorf("create mothership: %w", err)
	}

	entry.Info(fmt.Sprintf("cmd - 5; doing %s", mode))
	select {
	// testperp's original 3-minute window was tuned for testperpv1's
	// quick ~16-action single pass -- this mode instead runs
	// CYCLE_TARGET (100) real deposit<->withdraw round trips for one
	// protocol, each needing a real transaction to land and confirm, so
	// it needs a much longer window. 60 minutes is a generous guess, not
	// a measured number -- there's no real-run data yet on how long 100
	// cycles actually take (see this command's own doc comment on why
	// starting with a single protocol, e.g. marginfi, keeps a first real
	// run's cost/time bounded while that number gets established).
	case <-time.After(60 * time.Minute):
	case err = <-ms.CloseSignal():
	}
	entry.Info(fmt.Sprintf("cmd - 6; doing %s", mode))
	// make sure the bot image is not deleted
	_ = botImage
	return err
}
