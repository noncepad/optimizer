// Package xstockshealthv1 is the Go-side orchestrator for the
// xstockshealthv1 bot mode. Originally a passive-watcher-only mode;
// merged with the separate self-triggered liquidation demo bot into one
// (see the Rust side's state.rs doc comment for why) -- the WASM bot now
// genuinely signs and sends real transactions when it finds a real
// eligible obligation, not just harness-lifecycle wallet plumbing.
// Otherwise a direct copy of package testperpv1's orchestration: same
// allocation/upload/handshake/wallet-init flow, only MODE=xstockshealthv1
// selected at upload time instead.
//
// It implements the brain.Brain interface and is responsible for:
//   - Allocating a slot on a Catscope/Solpipe validator pipeline (bid = 0, free tier).
//   - Uploading a WASM bot image to the validator with MODE=xstockshealthv1.
//   - Completing the handshake with the running bot instance.
//   - Sending a child keypair to the bot via stdin -- now the real
//     signing key for the liquidations the bot fires, not just
//     harness-lifecycle consistency.
//
// The WASM bot (catscope-rust-bot/src/brain/xstockshealthv1) tracks a
// known set of Kamino xStocks obligations/reserves
// (trader::dex::kamino_xstocks_watcher::KaminoXstocksWatcher), logs a
// real-time health-factor board, and fires a real liquidation instruction
// against whichever tracked obligation is actually eligible -- see that
// module's doc comment for what it proves and HACKATHON_PLAN.md
// (edge-generator repo) for the plan this serves.
package xstockshealthv1

import (
	"context"
	"fmt"
	"log/slog"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/solana-foundation/solana-go/v2"
)

type eventHook struct {
	ctx          context.Context
	cancel       context.CancelCauseFunc
	logger       *slog.Logger
	graph        graph.Graph
	bidmgr       *bidder.BidderManager
	addressBook  common.BotClientDialer
	builder      *txbuilder.BuildManager
	authorizer   sgo.PublicKey
	botMarketID  sgo.PublicKey
	parentKey    sgo.PrivateKey
	childKey     sgo.PrivateKey
	handshake    *mgrbot.Handshake
	instance     *mgrbot.Bot
	config       *Configuration
	board        *boardStore
	liquidations *liquidationStore
	dex          *dexStore
	overview     *overviewStore
}

type Configuration struct {
	BotImage string
	// DashboardAddr is where the local health-board page listens, e.g.
	// "127.0.0.1:8090". Defaults to defaultDashboardAddr if empty. "Run
	// it locally for now" instead of the catscope-htmx-or-standalone
	// hosting decision (HACKATHON_PLAN.md §5/§10 item 4) -- see
	// dashboard.go's own doc comment.
	DashboardAddr string
}

// 8091, not 8090 -- 8090 is the port the separate, pre-existing `optimizer
// dashboard` command (cmd/dashboard.go) conventionally uses for its own
// prefetch.db viewer. That command is unrelated to this one (it's a
// generic read-only viewer over whatever prefetch.db-shaped file you point
// it at) but a stale instance of it left running from an earlier session
// was still holding 8090, so this mode gets its own port instead of
// fighting over that one.
const defaultDashboardAddr = "127.0.0.1:8091"

// Create creates an xstockshealthv1 gRPC event hook. Unlike testperpv1's
// Create, this doesn't take a prefetch.db connection -- this mode has no
// use for one (no account-usage/target-allocation/LST-APY persistence,
// see instance.go/message.go). Starts the local dashboard immediately
// (page is up right away, shows "waiting for the bot's first health
// board" until Init/handshake complete and real data starts arriving).
func Create(ctx context.Context, cancel context.CancelCauseFunc, parentKey sgo.PrivateKey, config *Configuration) (brain.Brain, error) {
	entry := util.LoggerBrainSimple.Fields(logger.FromContext(ctx))
	addr := config.DashboardAddr
	if addr == "" {
		addr = defaultDashboardAddr
	}
	board := &boardStore{}
	liquidations := &liquidationStore{}
	dex := &dexStore{}
	overview := &overviewStore{}
	startDashboard(addr, board, liquidations, dex, overview, entry)
	return &eventHook{
		ctx:          logger.ToContext(ctx, entry),
		cancel:       cancel,
		logger:       entry,
		parentKey:    parentKey,
		config:       config,
		board:        board,
		liquidations: liquidations,
		dex:          dex,
		overview:     overview,
	}, nil
}

func (hs *eventHook) useLocalImage(ctx context.Context, fp string, mEnv map[string]string) (mgrbot.Image, error) {
	image, err := mgrbot.Load(ctx, hs.parentKey, hs.botMarketID, fp, hs.addressBook, hs.builder, [2]int{1, 2}, mEnv)
	if err != nil {
		return mgrbot.Image{}, fmt.Errorf("failed to load image: %s", err)
	}
	return image, nil
}

// downloadDefaultImage mirrors testperpv1's own -- unused unless
// Configuration.BotImage is left empty (a locally-built image is the
// normal path for this mode, same as testperpv1/testperplatency).
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
