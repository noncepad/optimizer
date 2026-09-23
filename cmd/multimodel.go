package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	mothership "git.noncepad.com/pkg/bot/solpipe/bidder/manager"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/brain/multimodelv1"
	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/prefetch/liquidity"
	"git.noncepad.com/pkg/optimizer/prefetch/multimodel"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// MultiModelCmd mirrors LeveragedLoopCmd/TestPerpCmd's allocation/
// prefetch/upload/handshake flow, wiring up multimodelv1.Create instead
// -- see that package's doc comment for what this mode actually does
// (catscope-rust-bot's src/brain/multimodelv1/PLAN-1.md Phase 5).
//
// --enable-factor-logging (sub-phase 5b) is real but read-only: opts the
// bot into a periodic factor-graph resync/logging cycle, opens/closes
// nothing. --enable-pair-trading (sub-phase 5c) is REAL execution: real
// Kamino deposits/borrows, real spot swaps, once a real candidate clears
// the real z-score and borrow-cost gates. --enable-directional-trading
// (trade type 1, directional factor-neutral) is also REAL execution --
// long a human-specified token (--directional-mint, required with it),
// short a real hedge basket built from that token's own factor loadings;
// unlike the pair trade, entry is a human decision, not an automated
// signal, but risk management (stop-loss/borrow-gate) is automatic once
// open -- --close-directional-position sends the human "I'm satisfied"
// half that automatic close-pass doesn't have. --enable-dispersion-trading
// (trade type 3, dispersion) is also REAL execution -- long a real
// basket of the curated universe's highest-idiosyncratic-volatility
// tokens (real spot buys), short a real Phoenix SOL-PERP index position
// sized to the basket's own aggregate market-factor exposure; unlike
// trade type 1, entry itself is automated (a real, computed volatility
// signal), so no target flag is needed -- --close-dispersion-position
// sends the same human override --close-directional-position does.
// --enable-hawkes-trading (trade type 5, Hawkes-on-eigenfactor momentum,
// see catscope-rust-bot's docs/HAWKES_FACTOR_TRADE_PLAN.md) is also REAL
// execution -- long/short a real momentum basket ranked by loading on
// whichever leading structural factor's own discrete-time self-exciting
// jump intensity crosses its own real threshold; like dispersion, entry
// is fully automated (no target flag), and short legs really do borrow
// (Kamino/Solend) -- --close-hawkes-position sends the same human
// override the other trade types' close flags do.
//
// Any combination of the six trigger flags may be sent together in one
// launch -- catscope-rust-bot's own state machine already runs
// pair/directional/dispersion trading simultaneously in one process
// (independent Kamino/Solend obligation IDs per trade type, a shared
// real factor resync feeding all three, and a real per-resync-cycle USDC
// reservation ledger so multiple trade types committing capital in the
// same cycle never double-spend the same real balance -- see
// state.rs's `m_usdc_reserved_this_cycle`/`available_usdc_value`). The
// only combinations still rejected are enabling and closing the *same*
// trade type at once (contradictory on their face). Omitting all six
// (the default) just connects the bot and lets it sit idle (real
// handshake, real wallet-key delivery, real DexState/TradeRouter
// subscriptions) -- the same "idle-verified" milestone every other mode
// proved before any trigger flag was added to its own command.
type MultiModelCmd struct {
	ParentKey                string        `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	WorkingDir               string        `option:"work" help:"working directory"`
	EnableFactorLogging      bool          `option:"enable-factor-logging" help:"send a real TriggerEnableFactorLogging shortly after the bot connects -- one-time opt-in for Phase 5 sub-phase 5b's real-but-read-only periodic factor-graph resync/logging cycle. Opens/closes nothing."`
	EnablePairTrading        bool          `option:"enable-pair-trading" help:"send a real TriggerEnablePairTrading shortly after the bot connects -- one-time opt-in for Phase 5 sub-phase 5c's REAL, executing pure-Kamino pair/stat-arb trade. Sends real transactions."`
	EnableDirectionalTrading bool          `option:"enable-directional-trading" help:"send a real TriggerEnableDirectionalTrading shortly after the bot connects -- one-time opt-in for trade type 1 (directional factor-neutral). Requires --directional-mint. Sends real transactions."`
	DirectionalMint          string        `option:"directional-mint" help:"base58 mint pubkey of the token to go long -- required with --enable-directional-trading. The bot builds and shorts a real hedge basket against this target's own factor loadings; this flag is the one human decision the bot doesn't make for you."`
	CloseDirectionalPosition bool          `option:"close-directional-position" help:"send a real TriggerCloseDirectionalPosition shortly after the bot connects -- explicit request to close whatever directional position is currently open (or pending open). No target mint needed."`
	EnableDispersionTrading  bool          `option:"enable-dispersion-trading" help:"send a real TriggerEnableDispersionTrading shortly after the bot connects -- one-time opt-in for trade type 3 (dispersion). No target needed -- entry is automated. Sends real transactions."`
	CloseDispersionPosition  bool          `option:"close-dispersion-position" help:"send a real TriggerCloseDispersionPosition shortly after the bot connects -- explicit request to close whatever dispersion position is currently open."`
	EnableHawkesTrading      bool          `option:"enable-hawkes-trading" help:"send a real TriggerEnableHawkesTrading shortly after the bot connects -- one-time opt-in for trade type 5 (Hawkes-on-eigenfactor momentum). No target needed -- entry is automated. Sends real transactions, including real borrows for short legs."`
	CloseHawkesPosition      bool          `option:"close-hawkes-position" help:"send a real TriggerCloseHawkesPosition shortly after the bot connects -- explicit request to close whatever Hawkes momentum basket is currently open."`
	TriggerAt                time.Duration `option:"trigger-at" default:"20s" help:"how long to wait after the bot connects before sending --enable-factor-logging/--enable-pair-trading/--enable-directional-trading/--enable-dispersion-trading/--enable-hawkes-trading -- gives real market data time to load; the trigger is safe to send early regardless (the state machine won't act until its own on-chain reads are ready), this just avoids an immediate no-op retry. --close-directional-position/--close-dispersion-position/--close-hawkes-position ignore this and send immediately (see the doc comment where they're wired below)."`
	SweepMint                string        `option:"sweep-mint" help:"temporary, standalone manual-cleanup tool (2026-09-03): base58 mint pubkey to sweep the bot's entire real balance of, via a real TriggerSweepMint sent --trigger-at after connect (needs real time for the live router to pick up the swept mint's own pool via subscribed account updates -- a fresh process inherits no router edges from any prior process). Independent of the six trade-mode flags above -- can be combined with any of them, or used alone. Requires --sweep-dest-mint. See KeyFlagTriggerSweepMint's doc comment for the real incident this exists to clean up (a hop-chain send stranding a real balance in an unsubscribed pass-through intermediate mint)."`
	SweepDestMint            string        `option:"sweep-dest-mint" help:"base58 mint pubkey --sweep-mint's real balance is swept to -- required with --sweep-mint. Not defaulted to USDC: a real, live-confirmed incident found the router had no route to USDC within the normal hop budget for one stranded mint, while a different destination did have one, so the real destination is always an explicit choice."`
}

func (r *MultiModelCmd) Run(rc *RunConfig) error {
	// Only the self-contradictory same-trade-type combinations are
	// rejected -- every other combination (including multiple real trade
	// types enabled together) is a real, supported mode of operation, see
	// this command's own doc comment above.
	if r.EnableDirectionalTrading && r.CloseDirectionalPosition {
		return errors.New("--enable-directional-trading and --close-directional-position are mutually exclusive")
	}
	if r.EnableDispersionTrading && r.CloseDispersionPosition {
		return errors.New("--enable-dispersion-trading and --close-dispersion-position are mutually exclusive")
	}
	if r.EnableHawkesTrading && r.CloseHawkesPosition {
		return errors.New("--enable-hawkes-trading and --close-hawkes-position are mutually exclusive")
	}
	var directionalMint sgo.PublicKey
	if r.EnableDirectionalTrading {
		if len(r.DirectionalMint) == 0 {
			return errors.New("--enable-directional-trading requires --directional-mint")
		}
		var err error
		directionalMint, err = sgo.PublicKeyFromBase58(r.DirectionalMint)
		if err != nil {
			return fmt.Errorf("failed to parse --directional-mint: %s", err)
		}
	}
	var sweepMint, sweepDestMint sgo.PublicKey
	if 0 < len(r.SweepMint) {
		if len(r.SweepDestMint) == 0 {
			return errors.New("--sweep-mint requires --sweep-dest-mint")
		}
		var err error
		sweepMint, err = sgo.PublicKeyFromBase58(r.SweepMint)
		if err != nil {
			return fmt.Errorf("failed to parse --sweep-mint: %s", err)
		}
		sweepDestMint, err = sgo.PublicKeyFromBase58(r.SweepDestMint)
		if err != nil {
			return fmt.Errorf("failed to parse --sweep-dest-mint: %s", err)
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
	b, err := multimodelv1.Create(ctx, cancel, parentKey, &multimodelv1.Configuration{
		BotImage: botImage.Path(),
	}, prefetchDB.Raw())
	if err != nil {
		return fmt.Errorf("failed to create multimodelv1 brain: %s", err)
	}
	mode := "multimodelv1"
	entry.Info(fmt.Sprintf("cmd - 3; doing %s", mode))
	startBundlerTipBroadcaster(ctx, entry, prefetchDB.Raw(), b.SendBundlerTipUpdate)
	ms, err := mothership.Create(ctx, dialer, b)
	entry.Info(fmt.Sprintf("cmd - 4; doing %s", mode))
	if err != nil {
		return fmt.Errorf("create mothership: %w", err)
	}
	entry.Info(fmt.Sprintf("cmd - 5; doing %s", mode))

	// isBotNotConnectedYet is this mode's sendTriggerWithRetry `retryable`
	// check -- same per-package sentinel pattern every other bot-mode
	// command uses (see leveragedloop.go's identical comment).
	isBotNotConnectedYet := func(err error) bool {
		return errors.Is(err, multimodelv1.ErrBotNotConnectedYet)
	}
	// Sent immediately, with no r.TriggerAt delay, whenever any
	// residual-consuming trade type is enabled -- replaying pre-existing
	// local state doesn't need to wait for real market data to load the
	// way a fresh trading decision does, and must win the race against
	// the bot's first real resync (see catscope-rust-bot's state.rs's
	// apply_residual_snapshot doc comment for why a late replay is
	// discarded rather than partially applied). Not pair-trading-specific
	// -- state.rs's own m_residual_history is shared across pair/
	// directional/dispersion (directional's stop-loss check and
	// dispersion's basket ranking both read real residual data too), so
	// any of the three benefits from a warm start, not just pair.
	if r.EnablePairTrading || r.EnableDirectionalTrading || r.EnableDispersionTrading || r.EnableHawkesTrading {
		go pushResidualSnapshots(ctx, entry, prefetchDB.Raw(), b, isBotNotConnectedYet)
	}
	// Every branch below is independent (not else-if) -- any subset of
	// these can be true together, see this command's own doc comment for
	// why that's a real, supported mode of operation, not just each one
	// in isolation.
	if r.EnableFactorLogging {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info("multimodel: sending real TriggerEnableFactorLogging")
			sendTriggerWithRetry(ctx, entry, "multimodel: TriggerEnableFactorLogging", b.SendTriggerEnableFactorLogging, isBotNotConnectedYet)
		}()
	}
	if r.EnablePairTrading {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info("multimodel: sending REAL TriggerEnablePairTrading")
			sendTriggerWithRetry(ctx, entry, "multimodel: TriggerEnablePairTrading", b.SendTriggerEnablePairTrading, isBotNotConnectedYet)
		}()
	}
	if r.EnableDirectionalTrading {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info(fmt.Sprintf("multimodel: sending REAL TriggerEnableDirectionalTrading (target %s)", directionalMint))
			sendTriggerWithRetry(ctx, entry, "multimodel: TriggerEnableDirectionalTrading", func() error {
				return b.SendTriggerEnableDirectionalTrading(directionalMint)
			}, isBotNotConnectedYet)
		}()
	}
	if r.CloseDirectionalPosition {
		// Sent immediately, no r.TriggerAt delay -- this only reads/acts
		// on whatever's already open (or pending open), doesn't need real
		// market data to load first the way a fresh trading decision
		// does, same precedent as the ReplayResidualSnapshot send above.
		go func() {
			entry.Info("multimodel: sending REAL TriggerCloseDirectionalPosition")
			sendTriggerWithRetry(ctx, entry, "multimodel: TriggerCloseDirectionalPosition", b.SendTriggerCloseDirectionalPosition, isBotNotConnectedYet)
		}()
	}
	if r.EnableDispersionTrading {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info("multimodel: sending REAL TriggerEnableDispersionTrading")
			sendTriggerWithRetry(ctx, entry, "multimodel: TriggerEnableDispersionTrading", b.SendTriggerEnableDispersionTrading, isBotNotConnectedYet)
		}()
	}
	if r.CloseDispersionPosition {
		// Sent immediately, same reasoning as CloseDirectionalPosition
		// above.
		go func() {
			entry.Info("multimodel: sending REAL TriggerCloseDispersionPosition")
			sendTriggerWithRetry(ctx, entry, "multimodel: TriggerCloseDispersionPosition", b.SendTriggerCloseDispersionPosition, isBotNotConnectedYet)
		}()
	}
	if r.EnableHawkesTrading {
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info("multimodel: sending REAL TriggerEnableHawkesTrading")
			sendTriggerWithRetry(ctx, entry, "multimodel: TriggerEnableHawkesTrading", b.SendTriggerEnableHawkesTrading, isBotNotConnectedYet)
		}()
	}
	if r.CloseHawkesPosition {
		// Sent immediately, same reasoning as CloseDirectionalPosition/
		// CloseDispersionPosition above.
		go func() {
			entry.Info("multimodel: sending REAL TriggerCloseHawkesPosition")
			sendTriggerWithRetry(ctx, entry, "multimodel: TriggerCloseHawkesPosition", b.SendTriggerCloseHawkesPosition, isBotNotConnectedYet)
		}()
	}
	if 0 < len(r.SweepMint) {
		// Independent of the mutual-exclusion group above (see
		// --sweep-mint's own help text). Real, live-confirmed fix
		// (2026-09-03): originally sent immediately, same reasoning as
		// the close triggers (only acts on whatever real balance already
		// exists) -- but unlike the close triggers, which only ever touch
		// curated-symbol pools already well-populated at wallet-connect,
		// a sweep's mint is often an obscure pass-through the live
		// TradeRouter has zero edges for on a fresh process (the router
		// only gets a pool's real edges from live account updates
		// actually arriving over the subscription stream, not inherited
		// from any prior process) -- confirmed live: a sweep sent 7s
		// after connect failed with "0 total" outgoing edges
		// (route_diagnostics) for a mint whose pool this same bot had
		// traded through minutes earlier in a previous process. Waits
		// r.TriggerAt, same as the trading-enable triggers, to give the
		// router real time to warm up first.
		// Real, live-confirmed fix (2026-09-04, attempt 1): a single send
		// can never succeed against a mint this process hasn't
		// subscribed to before -- `sweep_requested_mint` subscribes to
		// the mint's own ATA and checks its real balance in the *same*
		// synchronous call, so the very first attempt always sees "not
		// yet known" and just establishes the subscription; no value of
		// --trigger-at changes that, since it only controls when that
		// one call happens, not any gap *within* it. Confirmed live:
		// two separate real attempts (90s and 20s trigger-at) both hit
		// this identically.
		//
		// Real, live-confirmed fix (2026-09-04, attempt 2): a single
		// extra retry 15s later still isn't enough -- confirmed live
		// (mint 74SBV4z...pump, dest USDC): the ATA subscription DID
		// pick up the real balance (6436552701 raw units) on the retry,
		// but the swap itself then failed with "no route found" --
		// route_diagnostics showed the mint as a real, known router node
		// (build.rs's ROUTER_POOLS embeds 14 real on-chain pools for
		// this mint, all >= the $50k liquidity floor) with zero *live*
		// edges. Router edges only populate from real account updates
		// arriving over the subscription stream (never inherited from a
		// prior process, same as the ATA gap above) -- and per
		// `PubkeyAccountIdCache`'s own doc comment, a fresh process's
		// startup burst is ~32K subscription requests, which takes
		// materially longer than 15s to work through. Retry on a
		// widening schedule instead of just once, so a slow subscription
		// burst gets real minutes to land at least one of this mint's
		// pools before giving up; each attempt is independent and safe
		// to repeat (sweep is a no-op once the balance is already 0).
		//
		// Real, live-confirmed fix (2026-09-04, attempt 3): the 15s/45s/
		// 2m/5m schedule above did get a live edge (confirmed: the 2m
		// attempt found a real route through pool 944118658, an Orca
		// Whirlpool), but `planner::reverify_route_with_exact_quotes`
		// rejected it and cooled the pool down for
		// `planner::POOL_COOLDOWN_SLOTS` (4_500 slots, ~30-40 real
		// minutes) -- root-caused on the Rust side (not a Go-side
		// timing problem): `OrcaState::exact_quote`/`RaydiumClmm::
		// exact_quote` silently treat a not-yet-synced tick array as
		// zero liquidity, indistinguishable from a genuinely bad quote,
		// so a pool whose *main* account had just delivered its first
		// live update (real edge) but whose tick-array data hadn't
		// synced yet got the full cooldown penalty for a timing
		// artifact, not a real bad quote. Fixed in the Rust bot itself
		// (`planner::HopFailure::coolable`, wired through `OrcaState::
		// exact_quote_ready`/`RaydiumClmm::exact_quote_ready`): a
		// data-not-ready failure no longer cools the pool down at all
		// now, so retrying often is free -- confirmed live: 6 retries
		// (15s/15s/30s/30s/1m/1m) all correctly reported "not ready" with
		// no cooldown, subscriptions climbing the whole time (0/10477 ->
		// 89999/105361 acked by 228s). A fixed short list of gaps just
		// runs out before a big startup burst (~100K+ subscriptions,
		// real minutes) finishes -- loop on a fixed interval instead,
		// for as long as the process itself lives (each attempt is a
		// real no-op once genuinely swept or still not ready, so there's
		// no real cost to trying too often or too long).
		const sweepRetryInterval = time.Minute
		go func() {
			select {
			case <-time.After(r.TriggerAt):
			case <-ctx.Done():
				return
			}
			entry.Info(fmt.Sprintf("multimodel: sending REAL TriggerSweepMint (mint %s -> %s)", sweepMint, sweepDestMint))
			sendTriggerWithRetry(ctx, entry, "multimodel: TriggerSweepMint", func() error {
				return b.SendTriggerSweepMint(sweepMint, sweepDestMint)
			}, isBotNotConnectedYet)
			for i := 1; ; i++ {
				select {
				case <-time.After(sweepRetryInterval):
				case <-ctx.Done():
					return
				}
				label := fmt.Sprintf("multimodel: TriggerSweepMint (retry %d)", i)
				entry.Info(fmt.Sprintf("multimodel: sending REAL %s (mint %s -> %s)", label, sweepMint, sweepDestMint))
				sendTriggerWithRetry(ctx, entry, label, func() error {
					return b.SendTriggerSweepMint(sweepMint, sweepDestMint)
				}, isBotNotConnectedYet)
			}
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

// pushResidualSnapshots reads every real residual/z-score snapshot a
// prior run persisted (prefetch/multimodel.LoadResidualSnapshots) and
// sends each one back to the freshly connected bot, one message per
// mint -- same shape as leveragedloopv1/testperpv1's own boot-time
// pushLstApyEstimates, one bad/missing entry never blocks the rest. Called
// whenever any of --enable-pair-trading/--enable-directional-trading/
// --enable-dispersion-trading/--enable-hawkes-trading is set --
// catscope-rust-bot's state.rs's m_residual_history is shared across all
// four trade types (not just pair's run_pair_trade_cycle -- directional's
// stop-loss check, dispersion's basket ranking, and Hawkes's own
// delta_zscore jump projection all read real residual data too), so a
// warm start benefits any of them.
func pushResidualSnapshots(ctx context.Context, entry *slog.Logger, db *sql.DB, b multimodelv1.Hook, retryable func(error) bool) {
	payloads, err := multimodel.LoadResidualSnapshots(db)
	if err != nil {
		entry.Error(fmt.Sprintf("multimodel: failed to load persisted residual snapshots: %s", err))
		return
	}
	if len(payloads) == 0 {
		return
	}
	entry.Info(fmt.Sprintf("multimodel: sending %d persisted residual snapshot(s)", len(payloads)))
	for _, payload := range payloads {
		sendTriggerWithRetry(ctx, entry, "multimodel: ReplayResidualSnapshot", func() error {
			return b.SendReplayResidualSnapshot(payload)
		}, retryable)
	}
}
