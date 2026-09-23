// Package testperplatencyv1 is the Go-side orchestrator for the
// testperplatencyv1 bot mode -- a real-transaction latency test, not a
// real trading strategy. Otherwise a direct copy of package testperpv1's
// orchestration (itself a copy of perpfundingv1's): same
// allocation/upload/handshake/wallet-init flow, only MODE=testperplatencyv1
// selected at upload time instead.
//
// It implements the brain.Brain interface and is responsible for:
//   - Allocating a slot on a Catscope/Solpipe validator pipeline (bid = 0, free tier).
//   - Uploading a WASM bot image to the validator with MODE=testperplatencyv1
//     and TEST_PROTOCOL set to whichever single protocol this run should
//     exercise (see init.go) -- unlike testperpv1, this mode runs a
//     CYCLE_TARGET-times repeated deposit<->withdraw loop per protocol, so
//     TEST_PROTOCOL scopes a real run's spend to just one protocol instead
//     of all three plus every borrow/repay leg in one go.
//   - Completing the handshake with the running bot instance.
//   - Sending a child keypair to the bot via stdin -- like testperpv1
//     (unlike perpfundingv1), this mode's Rust side
//     (catscope-rust-bot/src/brain/testperplatencyv1) does use it, to sign
//     the real deposit/withdraw transactions its evaluate() state machine
//     builds.
//
// The WASM bot (catscope-rust-bot/src/brain/testperplatencyv1) runs the
// real bootstrap -> (deposit<->withdraw x100) cycle for whichever protocol
// TEST_PROTOCOL selects, recording write->low-latency-read latency and
// reporting p50/p99 once the cycle count target is hit -- see that
// module's doc comment for what it proves and why the cycling exists.
package testperplatencyv1

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/optimizer/bundler"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

type eventHook struct {
	ctx         context.Context
	cancel      context.CancelCauseFunc
	logger      *slog.Logger
	graph       graph.Graph
	bidmgr      *bidder.BidderManager
	addressBook common.BotClientDialer
	builder     *txbuilder.BuildManager
	authorizer  sgo.PublicKey
	botMarketID sgo.PublicKey
	parentKey   sgo.PrivateKey
	childKey    sgo.PrivateKey
	handshake   *mgrbot.Handshake
	instance    *mgrbot.Bot
	config      *Configuration
	// db is the shared prefetch.db connection, used by
	// SendTargetAllocation to persist the latest target portfolio
	// allocation alongside pushing it to the running bot -- the same
	// database build.rs reads at compile time for the bot's default.
	db *sql.DB
	// didBootTransfer gates the one-time parent->child SOL transfer in
	// Evaluate (see eval.go) -- Evaluate fires on every solpipe state
	// update, not just once, so this is what stops it from resending the
	// transfer repeatedly.
	didBootTransfer bool
}

type Configuration struct {
	BotImage string
	// TargetProtocol selects which single protocol's deposit<->withdraw
	// cycle the Rust side should run ("solend"/"kamino"/"marginfi"),
	// passed through as the TEST_PROTOCOL env var -- see init.go. Empty
	// runs the original full 16-phase sequence (all three protocols plus
	// every borrow/repay leg), matching testperpv1's unscoped behavior --
	// almost never what you want for a real run, since it multiplies the
	// real transaction fees spent by however many protocols/phases are
	// included.
	TargetProtocol string
}

// Hook is brain.Brain plus SendBundlerTipUpdate -- returned instead of a
// bare brain.Brain so cmd/*.go can call SendBundlerTipUpdate on the same
// instance it hands to mothership.Create (a Hook value is itself a valid
// brain.Brain, since this interface embeds it). Mirrors testperpv1.Hook's
// own reasoning -- see that type's doc comment. Needed for the native
// transfer loop's real Astralane bundled-send path
// (catscope-rust-bot's send_native_transfer_via_astralane) to ever see
// live tip data at all; without this, cmd/testperplatency.go has no
// SendBundlerTipUpdate method to wire startBundlerTipBroadcaster up to,
// and Wallet::select_tip_account never returns Some.
type Hook interface {
	brain.Brain
	SendBundlerTipUpdate(update bundler.TipUpdate) error
}

// Create creates a testperplatencyv1 gRPC event hook. db is the already-open
// prefetch.db connection (see store.DB.Raw()), used to persist target
// allocation updates sent via SendTargetAllocation.
func Create(ctx context.Context, cancel context.CancelCauseFunc, parentKey sgo.PrivateKey, config *Configuration, db *sql.DB) (Hook, error) {
	entry := util.LoggerBrainSimple.Fields(logger.FromContext(ctx))
	return &eventHook{
		ctx:       logger.ToContext(ctx, entry),
		cancel:    cancel,
		logger:    entry,
		parentKey: parentKey,
		config:    config,
		db:        db,
	}, nil
}

func (hs *eventHook) useLocalImage(ctx context.Context, fp string, mEnv map[string]string) (mgrbot.Image, error) {
	image, err := mgrbot.Load(ctx, hs.parentKey, hs.botMarketID, fp, hs.addressBook, hs.builder, [2]int{1, 2}, mEnv)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("failed to load image: %s", err)
	}
	return image, nil
}

// downloadDefaultImage tbd
func (hs *eventHook) downloadDefaultImage(ctx context.Context) (mgrbot.Image, error) {
	fp, err := util.DownloadCatscopeRustBotDemonstrator(ctx)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("bot download failed: %s", err)
	}
	image, err := mgrbot.Load(ctx, hs.parentKey, hs.botMarketID, fp, hs.addressBook, hs.builder, [2]int{1, 2}, nil)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("failed to load image: %s", err)
	}
	return image, nil
}
