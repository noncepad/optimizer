package testperpv1_test

// Diagnostic test for the "parentSOL reads 0" investigation
// (optimizer/brain/testperpv1's boot transfer never fires because
// solpipeState.System(parentKey) never sees real data). This does a
// direct, minimal graph.Subscribe -- no bot allocation, no WASM image
// upload, no handshake -- so each case here takes only as long as the
// subscribe+ack itself, instead of the ~90s+ it costs to get here through
// a real `optimizer testperp` run.
//
// Deliberately NOT state.Client.QuerySingleShot: that helper wraps the
// same underlying Subscribe/ack mechanism but was found to be unreliable
// in production use and has been dropped from this codebase (see
// optimizer/util/walletbalance.go's FetchWalletBalance, which replaced
// every real QuerySingleShot call site). This test drives the same raw
// state.Client.Hook + util.PendingSubscriptionStatus pattern
// FetchWalletBalance and prefetch/{solend,kamino,marginfi,jet,drift}/
// event.go's eventHandler already use.
//
// Run individually, e.g.:
//   go test ./brain/testperpv1/ -run TestSubscribeDepth2/parent -v
//
// Requires the same local Solpipe bidder-proxy sockets `optimizer
// testperp` itself depends on (~/.solpipe.bidder.manage.sock,
// ~/.solpipe.bidder.proxy.sock) -- i.e. run this on a machine where the
// real bot already runs, not in CI.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	botsolpipe "git.noncepad.com/pkg/bot/solpipe"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	bidcommon "git.noncepad.com/pkg/bot/solpipe/bidder/manager/common"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/optimizer/util"
	ty "git.noncepad.com/pkg/safejar"
	cba "git.noncepad.com/pkg/solpipe"
	"git.noncepad.com/pkg/solpipe-util/common"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

// TestMain applies PROGRAM_SOLPIPE/PROGRAM_JAR (see
// bot/state.ProgramSetByEnv's doc comment) once, before any test in this
// package runs -- cba.ProgramID's compiled-in default has no account on
// mainnet at all, so without this, TestMarketDataViaManagerCreate below
// fails identically to the real, still-unaccounted-for bug rather than
// testing the fix. A no-op if PROGRAM_SOLPIPE/PROGRAM_JAR aren't set.
func TestMain(m *testing.M) {
	if err := state.ProgramSetByEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "state.ProgramSetByEnv: %s\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// feePayerPath is the same real fee-payer testperp uses -- override with
// FEE_PAYER_PATH if running from somewhere other than the optimizer repo
// root's default layout.
func feePayerPath() string {
	if p := os.Getenv("FEE_PAYER_PATH"); p != "" {
		return p
	}
	return "../../fee-payer.json"
}

// subscribeTimeout bounds how long we wait for a single subscription's ack
// before giving up -- generous relative to the ~30s window used throughout
// this investigation's live-bot instrumentation.
const subscribeTimeout = 40 * time.Second

// subscribeQuietSlots mirrors walletBalanceQuietSlots (optimizer/util/
// walletbalance.go) -- how many consecutive quiet slot-commits to wait for
// before considering a subscription's neighborhood fully delivered.
const subscribeQuietSlots = 10

func dialStateClient(t *testing.T, ctx context.Context) state.Client {
	t.Helper()
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(feePayerPath())
	if err != nil {
		t.Fatalf("failed to load fee payer at %s (set FEE_PAYER_PATH?): %s", feePayerPath(), err)
	}
	// Pin STATE_URL and TXPROC_URL explicitly to the real bidder proxy
	// socket instead of relying on bidder.CreateDialer's implicit
	// fallback-to-PROXY_SOCKET behavior (bot/solpipe/bidder/manager/
	// bidder/dialer.go) -- makes this test's target explicit and
	// independent of whatever STATE_URL/TXPROC_URL may already be set to
	// in the ambient environment. t.Setenv restores the prior value (or
	// unsets it) automatically at the end of this test.
	proxySock := fmt.Sprintf("unix://%s/.solpipe.bidder.proxy.sock", os.Getenv("HOME"))
	t.Setenv("STATE_URL", proxySock)
	t.Setenv("TXPROC_URL", proxySock)
	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		t.Fatalf("failed to dial bidder proxy at %s -- is the real bot's Solpipe proxy running? %s", proxySock, err)
	}
	return dialer.State()
}

// diagHook is a minimal graph.Hook: subscribe once to root/depth in Init,
// log every account it sees, then wait for PendingSubscriptionStatus to
// report quiet (or the context to time out).
type diagHook struct {
	ctx   context.Context
	t     *testing.T
	label string
	root  sgo.PublicKey
	depth uint32
	g     graph.Graph
	pss   *util.PendingSubscriptionStatus
	slot  graph.Slot
}

func (h *diagHook) Ctx() context.Context { return h.ctx }

func (h *diagHook) Init(g graph.Graph) error {
	h.g = g
	h.pss = util.CreatePendingSubscriptionList(h.ctx, g, 10, subscribeQuietSlots)
	h.pss.Subscribe(h.root, graph.WeightAll, h.depth)
	return nil
}

func (h *diagHook) OnSlot(slot graph.Slot, status graph.SlotStatus) {}

func (h *diagHook) CommitStart(slot graph.Slot) {
	h.slot = slot
}

func (h *diagHook) OnAccount(a graph.Account, isNew bool) {
	header := a.Header()
	h.t.Logf("[%s] OnAccount: pubkey=%s owner=%s lamports=%d", h.label, header.Pubkey, header.Owner, header.Lamports)
}

func (h *diagHook) CommitFinish() bool {
	return h.pss.Check(h.slot)
}

// runSubscribeCase subscribes to a single root/depth via a raw
// state.Client.Hook (see diagHook) and reports whether it acked (quieted
// down) in time.
func runSubscribeCase(t *testing.T, client state.Client, label string, root sgo.PublicKey, depth uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), subscribeTimeout)
	defer cancel()
	start := time.Now()
	t.Logf("[%s] subscribing to %s at depth=%d, weight=WeightAll", label, root, depth)
	handler := &diagHook{ctx: ctx, t: t, label: label, root: root, depth: depth}
	err := client.Hook(handler)
	elapsed := time.Since(start)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			t.Logf("[%s] did NOT quiet down within %s -- elapsed %s", label, subscribeTimeout, elapsed)
			return
		}
		t.Logf("[%s] hook failed: %s -- elapsed %s", label, err, elapsed)
		return
	}
	em := handler.g.EdgeManager()
	em.Lock()
	defer em.Unlock()
	a := em.UnsafeAccount(root)
	if a == nil {
		t.Logf("[%s] quieted down without error but account is missing from the EdgeManager -- elapsed %s", label, elapsed)
		return
	}
	header := a.Header()
	t.Logf("[%s] ACKED -- got header %+v; elapsed %s", label, header, elapsed)
}

// concurrentTestTimeout bounds TestSubscribeDepth2Concurrent -- generous
// relative to the isolated cases (which all ack within ~6s) so a real
// starvation effect has room to show up, without waiting the full 180s+
// the live bot was left running.
const concurrentTestTimeout = 90 * time.Second

// concurrentDiagHook replicates testperpv1's actual live pattern on one
// shared stream, closest to what optimizer/brain/testperpv1/eval.go's
// initWallet does: subscribe the child and parent wallet keys directly
// (bypassing PendingSubscriptionStatus, exactly like eval.go's `_ =
// hs.graph.Subscribe(...)`, except here the ack channels are kept instead
// of discarded so their timing can be measured), issued alongside the
// market-wide botMarketID subscription (depth 0 -- "all downstream
// accounts", the same single raw Subscribe call
// bot/solpipe/bidder/manager/manager.go's mothershipSolpipe.Init makes)
// which is what generates the heavy, continuous account-update traffic
// the live bot's stream carries. The isolated tests above proved each of
// these acks fine completely on its own, on its own fresh stream; this
// tests whether that changes once they're all sharing one stream at once.
type concurrentDiagHook struct {
	ctx         context.Context
	t           *testing.T
	childKey    sgo.PublicKey
	parentKey   sgo.PublicKey
	botMarketID sgo.PublicKey
	g           graph.Graph
	pss         *util.PendingSubscriptionStatus
	slot        graph.Slot
	start       time.Time
	childAckC   <-chan struct{}
	parentAckC  <-chan struct{}
	childAcked  bool
	parentAcked bool
}

func (h *concurrentDiagHook) Ctx() context.Context { return h.ctx }

func (h *concurrentDiagHook) Init(g graph.Graph) error {
	h.g = g
	h.start = time.Now()
	h.childAckC = g.Subscribe(h.ctx, h.childKey, graph.WeightAll, 2)
	h.parentAckC = g.Subscribe(h.ctx, h.parentKey, graph.WeightAll, 2)
	h.pss = util.CreatePendingSubscriptionList(h.ctx, g, 50, subscribeQuietSlots)
	h.pss.Subscribe(h.botMarketID, graph.WeightAll, 0)
	h.t.Logf("[concurrent] subscribed child, parent, and botMarketID together on one stream")
	return nil
}

func (h *concurrentDiagHook) OnSlot(slot graph.Slot, status graph.SlotStatus) {}

func (h *concurrentDiagHook) CommitStart(slot graph.Slot) {
	h.slot = slot
}

func (h *concurrentDiagHook) OnAccount(a graph.Account, isNew bool) {
	header := a.Header()
	if header.Pubkey.Equals(h.childKey) {
		h.t.Logf("[concurrent] OnAccount saw CHILD key directly: lamports=%d elapsed=%s", header.Lamports, time.Since(h.start))
	} else if header.Pubkey.Equals(h.parentKey) {
		h.t.Logf("[concurrent] OnAccount saw PARENT key directly: lamports=%d elapsed=%s", header.Lamports, time.Since(h.start))
	}
}

func (h *concurrentDiagHook) CommitFinish() bool {
	if !h.childAcked {
		select {
		case <-h.childAckC:
			h.childAcked = true
			h.t.Logf("[concurrent] CHILD key ACKED at elapsed=%s", time.Since(h.start))
		default:
		}
	}
	if !h.parentAcked {
		select {
		case <-h.parentAckC:
			h.parentAcked = true
			h.t.Logf("[concurrent] PARENT key ACKED at elapsed=%s", time.Since(h.start))
		default:
		}
	}
	marketQuiet := h.pss.Check(h.slot)
	if h.slot%20 == 0 {
		x := h.pss.Count()
		h.t.Logf("[concurrent] still waiting: childAcked=%v parentAcked=%v marketQuiet=%v market(count=%d,inFlight=%d,queued=%d) elapsed=%s",
			h.childAcked, h.parentAcked, marketQuiet, x[0], x[1], x[2], time.Since(h.start))
	}
	return h.childAcked && h.parentAcked && marketQuiet
}

// TestSubscribeDepth2Concurrent replicates the live bot's actual
// concurrent-subscription pattern on one shared stream (see
// concurrentDiagHook's doc comment), unlike TestSubscribeDepth2 above,
// which gives every root its own fresh, uncontended stream.
func TestSubscribeDepth2Concurrent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), concurrentTestTimeout)
	defer cancel()
	client := dialStateClient(t, ctx)

	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(feePayerPath())
	if err != nil {
		t.Fatalf("failed to load fee payer: %s", err)
	}
	childKey := common.DeriveChildKeyFromIndex(parentKey, 1)
	botMarketID := bidcommon.GetBotMarketID()

	handler := &concurrentDiagHook{
		ctx:         ctx,
		t:           t,
		childKey:    childKey.PublicKey(),
		parentKey:   parentKey.PublicKey(),
		botMarketID: botMarketID,
	}
	err = client.Hook(handler)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hook failed: %s", err)
	}
	if !handler.childAcked {
		t.Errorf("child key never acked within %s while subscribed concurrently with botMarketID", concurrentTestTimeout)
	}
	if !handler.parentAcked {
		t.Errorf("parent key never acked within %s while subscribed concurrently with botMarketID", concurrentTestTimeout)
	}
}

// initBlockDuration simulates testperpv1's real Init() -- hs.bidmgr.Allocate
// plus the bot-image upload/handshake dance -- which live-bot logs show
// takes on the order of 90s before initWallet() ever issues its first
// Subscribe call. bot/state/hook.go's Client.Hook calls `hook.Init(ga)`
// synchronously, inline, BEFORE its own event-draining loop (the one that
// ultimately flushes queued acks back to callers -- see hook.go's
// commitC/listAck handling) starts running at all. Meanwhile the state
// gRPC stream is already connected and receiving server traffic --
// including, per the isolated tests above, a commit notification roughly
// every slot (~400ms) even with zero subscriptions -- onto commitC, a
// channel buffered at only 100 (bot/state/graph.go's grapher()). 90s at
// ~400ms/slot is ~225 commits: enough to overflow that buffer well before
// Init() ever returns, if nothing is silently dropping the excess. This
// test holds Init() open for that long BEFORE subscribing, to see whether
// that alone reproduces the live bot's "ack never arrives" symptom that
// none of the immediate/concurrent tests above could reproduce.
const initBlockDuration = 100 * time.Second

// delayedInitTestTimeout must clear initBlockDuration plus a real margin
// for the acks to land afterward if they're merely delayed, not lost.
const delayedInitTestTimeout = 180 * time.Second

type delayedInitDiagHook struct {
	ctx         context.Context
	t           *testing.T
	childKey    sgo.PublicKey
	parentKey   sgo.PublicKey
	g           graph.Graph
	slot        graph.Slot
	start       time.Time
	childAckC   <-chan struct{}
	parentAckC  <-chan struct{}
	childAcked  bool
	parentAcked bool
}

func (h *delayedInitDiagHook) Ctx() context.Context { return h.ctx }

// Init deliberately does NOT subscribe right away -- it blocks first, the
// same way testperpv1's real Init() is blocked on allocation + bot image
// upload + handshake before initWallet() ever runs, while the state
// stream underneath it is already live. Only after that delay does it
// issue the same two Subscribe calls the isolated/concurrent tests above
// already proved ack in ~1-6s on an otherwise-idle or freshly-active
// stream.
func (h *delayedInitDiagHook) Init(g graph.Graph) error {
	h.g = g
	h.start = time.Now()
	h.t.Logf("[delayed] stream connected; blocking Init() for %s before subscribing (simulating allocation+bot upload+handshake)", initBlockDuration)
	select {
	case <-h.ctx.Done():
		return h.ctx.Err()
	case <-time.After(initBlockDuration):
	}
	h.t.Logf("[delayed] Init() unblocking after %s; subscribing child+parent now", time.Since(h.start))
	h.childAckC = g.Subscribe(h.ctx, h.childKey, graph.WeightAll, 2)
	h.parentAckC = g.Subscribe(h.ctx, h.parentKey, graph.WeightAll, 2)
	return nil
}

func (h *delayedInitDiagHook) OnSlot(slot graph.Slot, status graph.SlotStatus) {}

func (h *delayedInitDiagHook) CommitStart(slot graph.Slot) {
	h.slot = slot
}

func (h *delayedInitDiagHook) OnAccount(a graph.Account, isNew bool) {
	header := a.Header()
	if header.Pubkey.Equals(h.childKey) {
		h.t.Logf("[delayed] OnAccount saw CHILD key directly: lamports=%d elapsed=%s", header.Lamports, time.Since(h.start))
	} else if header.Pubkey.Equals(h.parentKey) {
		h.t.Logf("[delayed] OnAccount saw PARENT key directly: lamports=%d elapsed=%s", header.Lamports, time.Since(h.start))
	}
}

func (h *delayedInitDiagHook) CommitFinish() bool {
	if !h.childAcked {
		select {
		case <-h.childAckC:
			h.childAcked = true
			h.t.Logf("[delayed] CHILD key ACKED at elapsed=%s (%s after Init() unblocked)", time.Since(h.start), time.Since(h.start)-initBlockDuration)
		default:
		}
	}
	if !h.parentAcked {
		select {
		case <-h.parentAckC:
			h.parentAcked = true
			h.t.Logf("[delayed] PARENT key ACKED at elapsed=%s (%s after Init() unblocked)", time.Since(h.start), time.Since(h.start)-initBlockDuration)
		default:
		}
	}
	if h.slot%20 == 0 {
		h.t.Logf("[delayed] still waiting: childAcked=%v parentAcked=%v elapsed=%s", h.childAcked, h.parentAcked, time.Since(h.start))
	}
	return h.childAcked && h.parentAcked
}

// TestSubscribeDepth2DelayedInit tests whether a long-blocked Init() --
// matching the real testperpv1 bot's actual ~90s allocation/upload/
// handshake delay before its first Subscribe call -- causes the eventual
// ack to get lost, given the state stream is already live and receiving
// (undrained) traffic that whole time. See delayedInitDiagHook and
// initBlockDuration's doc comments for the exact mechanism being tested.
func TestSubscribeDepth2DelayedInit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), delayedInitTestTimeout)
	defer cancel()
	client := dialStateClient(t, ctx)

	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(feePayerPath())
	if err != nil {
		t.Fatalf("failed to load fee payer: %s", err)
	}
	childKey := common.DeriveChildKeyFromIndex(parentKey, 1)

	handler := &delayedInitDiagHook{
		ctx:       ctx,
		t:         t,
		childKey:  childKey.PublicKey(),
		parentKey: parentKey.PublicKey(),
	}
	err = client.Hook(handler)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hook failed: %s", err)
	}
	if !handler.childAcked {
		t.Errorf("child key never acked within %s after a %s blocked Init()", delayedInitTestTimeout, initBlockDuration)
	}
	if !handler.parentAcked {
		t.Errorf("parent key never acked within %s after a %s blocked Init()", delayedInitTestTimeout, initBlockDuration)
	}
}

// superHookTestTimeout bounds TestSubscribeViaSolpipeHook. Every prior
// test in this file acked in single-digit seconds, so this is generous
// margin, not an expectation that it needs the full window.
const superHookTestTimeout = 40 * time.Second

// superHookDiagHook implements botsolpipe.SuperHook (bot/solpipe/
// hook.go's Hook + bot/safejar's Hook/HookSystem/HookToken) -- the real
// interface bot/solpipe/bidder/manager's mothershipSolpipe implements and
// the real testperpv1 bot's Client.Hook actually drives, via
// bot/solpipe.New(ctx, h) wrapping it into a graph.Hook. Every prior test
// in this file called state.Client.Hook with a bare graph.Hook directly,
// bypassing this entire translation layer -- bot/solpipe.go's
// external.onAccount, which dispatches each account by owner into
// OnSol/OnToken/Translate/etc *before* anything resembling
// mothershipSolpipe ever sees it. All methods except OnSol are no-ops:
// OnSol is the one that feeds internalSolpipeState.mSystem in the real
// bot (see bot/solpipe/bidder/manager/fund.go), and is what
// solpipeState.System(parentKey) ultimately reads -- and per this
// investigation's earlier live-bot instrumentation (fund.go's OnSol),
// it was never observed to fire even once in 15-30 minutes of a real,
// otherwise-functioning run. This test isolates exactly that dispatch
// path: does OnSol fire for the child/parent keys when going through the
// real translation layer, even though the raw Subscribe/ack underneath
// it (proven by every test above) works fine?
type superHookDiagHook struct {
	ctx             context.Context
	t               *testing.T
	childKey        sgo.PublicKey
	parentKey       sgo.PublicKey
	g               graph.Graph
	slot            graph.Slot
	start           time.Time
	childAckC       <-chan struct{}
	parentAckC      <-chan struct{}
	childAcked      bool
	parentAcked     bool
	childOnSolSeen  bool
	parentOnSolSeen bool
}

func (h *superHookDiagHook) Ctx() context.Context { return h.ctx }

func (h *superHookDiagHook) Init(g graph.Graph) error {
	h.g = g
	h.start = time.Now()
	h.childAckC = g.Subscribe(h.ctx, h.childKey, graph.WeightAll, 2)
	h.parentAckC = g.Subscribe(h.ctx, h.parentKey, graph.WeightAll, 2)
	h.t.Logf("[superhook] subscribed child+parent through the real bot/solpipe.New translation layer")
	return nil
}

func (h *superHookDiagHook) OnSlot(slot graph.Slot, status graph.SlotStatus) {}

func (h *superHookDiagHook) CommitStart(slot graph.Slot) {
	h.slot = slot
}

// OnSol is the one method here that matters -- see the type doc comment.
func (h *superHookDiagHook) OnSol(header graph.AccountHeader) {
	if header.Pubkey.Equals(h.childKey) {
		h.childOnSolSeen = true
		h.t.Logf("[superhook] OnSol fired for CHILD key: lamports=%d elapsed=%s", header.Lamports, time.Since(h.start))
	} else if header.Pubkey.Equals(h.parentKey) {
		h.parentOnSolSeen = true
		h.t.Logf("[superhook] OnSol fired for PARENT key: lamports=%d elapsed=%s", header.Lamports, time.Since(h.start))
	}
}

func (h *superHookDiagHook) CommitFinish() bool {
	if !h.childAcked {
		select {
		case <-h.childAckC:
			h.childAcked = true
			h.t.Logf("[superhook] CHILD key raw Subscribe ACKED at elapsed=%s", time.Since(h.start))
		default:
		}
	}
	if !h.parentAcked {
		select {
		case <-h.parentAckC:
			h.parentAcked = true
			h.t.Logf("[superhook] PARENT key raw Subscribe ACKED at elapsed=%s", time.Since(h.start))
		default:
		}
	}
	if h.slot%20 == 0 {
		h.t.Logf("[superhook] still waiting: childAcked=%v childOnSolSeen=%v parentAcked=%v parentOnSolSeen=%v elapsed=%s",
			h.childAcked, h.childOnSolSeen, h.parentAcked, h.parentOnSolSeen, time.Since(h.start))
	}
	return h.childAcked && h.parentAcked && h.childOnSolSeen && h.parentOnSolSeen
}

// The rest of these satisfy botsolpipe.SuperHook but are irrelevant to
// this diagnostic -- none of our target accounts are Solpipe program,
// safejar, or SPL token/mint accounts, so these should never fire for
// child/parent, but are required by the interface.
func (h *superHookDiagHook) OnAccount(a graph.Account, isNew bool)                    {}
func (h *superHookDiagHook) OnDelete(header graph.AccountHeader)                      {}
func (h *superHookDiagHook) OnToken(a graph.Account, token *sgotkn.Account)           {}
func (h *superHookDiagHook) OnMint(a graph.Account, mint *sgotkn.Mint)                {}
func (h *superHookDiagHook) OnJar(*graph.AnchorAccount[*ty.Controller])               {}
func (h *superHookDiagHook) OnDelegation(*graph.AnchorAccount[*ty.Delegation])        {}
func (h *superHookDiagHook) OnSpendRequest(*graph.AnchorAccount[*ty.SpendRequest])    {}
func (h *superHookDiagHook) OnMarket(*graph.AnchorAccount[*cba.Controller])           {}
func (h *superHookDiagHook) OnPipeline(*graph.AnchorAccount[*cba.Pipeline])           {}
func (h *superHookDiagHook) OnPayout(*graph.AnchorAccount[*cba.Payout])               {}
func (h *superHookDiagHook) OnAgent(*graph.AnchorAccount[*cba.Agent])                 {}
func (h *superHookDiagHook) OnRefund(*graph.AnchorAccount[*cba.Refunds])              {}
func (h *superHookDiagHook) OnControllerAPI(*graph.AnchorAccount[*cba.ControllerApi]) {}
func (h *superHookDiagHook) OnPeriodRing(*graph.AnchorAccount[*cba.PeriodRing])       {}
func (h *superHookDiagHook) OnBidList(*graph.AnchorAccount[*cba.BidList])             {}

// TestSubscribeViaSolpipeHook drives the same child/parent Subscribe
// calls through the real bot/solpipe.New(ctx, h) translation layer
// (see superHookDiagHook's doc comment) instead of a bare graph.Hook --
// the one thing every prior test in this file didn't do.
func TestSubscribeViaSolpipeHook(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), superHookTestTimeout)
	defer cancel()
	client := dialStateClient(t, ctx)

	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(feePayerPath())
	if err != nil {
		t.Fatalf("failed to load fee payer: %s", err)
	}
	childKey := common.DeriveChildKeyFromIndex(parentKey, 1)

	inner := &superHookDiagHook{
		ctx:       ctx,
		t:         t,
		childKey:  childKey.PublicKey(),
		parentKey: parentKey.PublicKey(),
	}
	wrapped, err := botsolpipe.New(ctx, inner)
	if err != nil {
		t.Fatalf("botsolpipe.New failed: %s", err)
	}
	err = client.Hook(wrapped)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hook failed: %s", err)
	}
	if !inner.childAcked {
		t.Errorf("child key's raw Subscribe never acked within %s", superHookTestTimeout)
	}
	if !inner.parentAcked {
		t.Errorf("parent key's raw Subscribe never acked within %s", superHookTestTimeout)
	}
	if !inner.childOnSolSeen {
		t.Errorf("OnSol never fired for the CHILD key within %s, even though its raw Subscribe acked=%v -- this is the real dispatch-layer bug", superHookTestTimeout, inner.childAcked)
	}
	if !inner.parentOnSolSeen {
		t.Errorf("OnSol never fired for the PARENT key within %s, even though its raw Subscribe acked=%v -- this is the real dispatch-layer bug", superHookTestTimeout, inner.parentAcked)
	}
}

// managerTestTimeout bounds TestSubscribeViaManagerCreate. Generous
// relative to every prior test's single-digit-second results, since this
// is the first one that also depends on bidmgr.Authorizer/BidderAgent/Log
// (real calls manager.Create makes before minimalBrain.Init even runs)
// succeeding, not just the graph stream.
const managerTestTimeout = 60 * time.Second

// minimalBrain implements brain.Brain (the interface testperpv1's
// eventHook itself implements) with everything testperpv1.Init actually
// does -- hs.bidmgr.Allocate, the bot image upload/handshake, all of it
// -- stripped out, keeping only the two Subscribe calls. The point is to
// drive the exact same production stack (manager.Create -> mothershipSolpipe
// -> loopInternal's commitStoreC/commitFinishC handoff, see
// bot/solpipe/bidder/manager/{manager,commit}.go) that TestSubscribeViaSolpipeHook
// above did NOT exercise -- that test called state.Client.Hook with a
// SuperHook directly, bypassing manager.Create and loopInternal entirely.
// Evaluate here calls the exact same brain.SolpipeState.System(pubkey)
// that testperpv1/eval.go's Evaluate calls and that's stuck reading 0 in
// the live bot -- if this reproduces that, the bug is in
// manager.go/commit.go's cross-goroutine handoff, not in Subscribe/ack or
// the OnSol dispatch (both already proven fine).
type minimalBrain struct {
	ctx        context.Context
	t          *testing.T
	childKey   sgo.PublicKey
	parentKey  sgo.PublicKey
	start      time.Time
	evalCount  int
	childSeen  bool
	parentSeen bool
	done       chan struct{}
}

func (b *minimalBrain) Init(g graph.Graph, builder *txbuilder.BuildManager, dialer bidcommon.BotClientDialer, bidmgr *bidder.BidderManager, authorizer sgo.PublicKey) error {
	b.start = time.Now()
	_ = g.Subscribe(b.ctx, b.childKey, graph.WeightAll, 2)
	_ = g.Subscribe(b.ctx, b.parentKey, graph.WeightAll, 2)
	b.t.Logf("[manager] subscribed child+parent via minimalBrain.Init, through the full manager.Create stack")
	return nil
}

func (b *minimalBrain) Evaluate(solpipeState brain.SolpipeState, bidderState brain.BidderState) error {
	b.evalCount++
	childSOL := solpipeState.System(b.childKey)
	parentSOL := solpipeState.System(b.parentKey)
	if !b.childSeen && childSOL != 0 {
		b.childSeen = true
		b.t.Logf("[manager] Evaluate: CHILD System() first nonzero (%d lamports) after %d calls, elapsed=%s", childSOL, b.evalCount, time.Since(b.start))
	}
	if !b.parentSeen && parentSOL != 0 {
		b.parentSeen = true
		b.t.Logf("[manager] Evaluate: PARENT System() first nonzero (%d lamports) after %d calls, elapsed=%s", parentSOL, b.evalCount, time.Since(b.start))
	}
	if b.evalCount%500 == 0 {
		b.t.Logf("[manager] still waiting: childSeen=%v parentSeen=%v evalCount=%d elapsed=%s", b.childSeen, b.parentSeen, b.evalCount, time.Since(b.start))
	}
	if b.childSeen && b.parentSeen {
		select {
		case <-b.done:
		default:
			close(b.done)
		}
	}
	return nil
}

// TestSubscribeViaManagerCreate drives the child/parent Subscribe calls
// through the real manager.Create stack (mothershipSolpipe + loopInternal)
// instead of a bare SuperHook -- see minimalBrain's doc comment for why
// this is the one remaining untested layer.
func TestSubscribeViaManagerCreate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), managerTestTimeout)
	defer cancel()

	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(feePayerPath())
	if err != nil {
		t.Fatalf("failed to load fee payer: %s", err)
	}
	childKey := common.DeriveChildKeyFromIndex(parentKey, 1)

	proxySock := fmt.Sprintf("unix://%s/.solpipe.bidder.proxy.sock", os.Getenv("HOME"))
	t.Setenv("STATE_URL", proxySock)
	t.Setenv("TXPROC_URL", proxySock)
	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		t.Fatalf("failed to dial bidder proxy at %s -- is the real bot's Solpipe proxy running? %s", proxySock, err)
	}

	mb := &minimalBrain{
		ctx:       ctx,
		t:         t,
		childKey:  childKey.PublicKey(),
		parentKey: parentKey.PublicKey(),
		done:      make(chan struct{}),
	}
	_, err = manager.Create(ctx, dialer, mb)
	if err != nil {
		t.Fatalf("manager.Create failed: %s", err)
	}
	select {
	case <-ctx.Done():
		t.Logf("[manager] timed out after %s", managerTestTimeout)
	case <-mb.done:
	}
	if !mb.childSeen {
		t.Errorf("CHILD System() never went nonzero within %s via the full manager.Create stack -- reproduces the live bug at the loopInternal/commitStoreC layer", managerTestTimeout)
	}
	if !mb.parentSeen {
		t.Errorf("PARENT System() never went nonzero within %s via the full manager.Create stack -- reproduces the live bug at the loopInternal/commitStoreC layer", managerTestTimeout)
	}
}

// marketDataCandidates are real Solpipe-program-owned pubkeys observed
// under botMarketID (owner CBAidZ5BjA1BYi9WF6Ca1AaWakF2MPxkVgp7oo5tDyW3) in
// this investigation's earlier TestSubscribeDepth2/bot_market_id run --
// used to opportunistically check which of Pipeline/Payout/BidList/
// PeriodRing resolve, without needing to know each one's exact account
// type ahead of time.
var marketDataCandidates = []string{
	"2Gi87bD2MNKFREReqkJgcLodkBuQEcYGKaaWz45i2Nbo",
	"EKyqxytpnqUfcN429ZwDXsfQKcc2rGs5DqK1S6crbZx2",
	"3Wb9geBMLCJUvMU46X4YgDjhq1mQPi4jVAhtFBRue4dj",
	"BDacspNTi5SHy7qvCbEum1mcUUoVvBWrj58bLtA9CLsK",
	"41hLKeX979tnSJArDn2dxNVwHMdwpyYmqRTVvYpS3UJr",
}

// marketDataBrain checks whether OnMarket -- and, opportunistically,
// OnPipeline/OnPayout/OnBidList/OnPeriodRing -- actually populate
// SolpipeState through the real manager.Create stack, the same fixed
// dispatch layer TestSubscribeViaManagerCreate proved works for OnSol/
// System. Market(botMarketID) is the definitive check: botMarketID is
// subscribed automatically by mothershipSolpipe.Init itself (manager.go)
// regardless of what this brain subscribes to, and is known for certain
// to be a Controller account, so a non-nil result there directly confirms
// the OnMarket dispatch path works for the account that matters most to
// arbv1's real trading decisions.
type marketDataBrain struct {
	ctx         context.Context
	t           *testing.T
	botMarketID sgo.PublicKey
	candidates  []sgo.PublicKey
	start       time.Time
	evalCount   int
	marketSeen  bool
	foundTypes  map[string]bool
	done        chan struct{}
}

func (b *marketDataBrain) Init(g graph.Graph, builder *txbuilder.BuildManager, dialer bidcommon.BotClientDialer, bidmgr *bidder.BidderManager, authorizer sgo.PublicKey) error {
	b.start = time.Now()
	b.foundTypes = make(map[string]bool)
	b.t.Logf("[market] relying on mothershipSolpipe.Init's own botMarketID subscribe -- this brain subscribes to nothing extra")
	return nil
}

func (b *marketDataBrain) Evaluate(solpipeState brain.SolpipeState, bidderState brain.BidderState) error {
	b.evalCount++
	if !b.marketSeen && solpipeState.Market(b.botMarketID) != nil {
		b.marketSeen = true
		b.t.Logf("[market] Evaluate: Market(botMarketID) first non-nil after %d calls, elapsed=%s", b.evalCount, time.Since(b.start))
	}
	for _, c := range b.candidates {
		if solpipeState.Pipeline(c) != nil && !b.foundTypes["Pipeline:"+c.String()] {
			b.foundTypes["Pipeline:"+c.String()] = true
			b.t.Logf("[market] Evaluate: Pipeline(%s) non-nil, elapsed=%s", c, time.Since(b.start))
		}
		if solpipeState.Payout(c) != nil && !b.foundTypes["Payout:"+c.String()] {
			b.foundTypes["Payout:"+c.String()] = true
			b.t.Logf("[market] Evaluate: Payout(%s) non-nil, elapsed=%s", c, time.Since(b.start))
		}
		if solpipeState.BidList(c) != nil && !b.foundTypes["BidList:"+c.String()] {
			b.foundTypes["BidList:"+c.String()] = true
			b.t.Logf("[market] Evaluate: BidList(%s) non-nil, elapsed=%s", c, time.Since(b.start))
		}
		if solpipeState.PeriodRing(c) != nil && !b.foundTypes["PeriodRing:"+c.String()] {
			b.foundTypes["PeriodRing:"+c.String()] = true
			b.t.Logf("[market] Evaluate: PeriodRing(%s) non-nil, elapsed=%s", c, time.Since(b.start))
		}
	}
	if b.evalCount%500 == 0 {
		b.t.Logf("[market] still waiting: marketSeen=%v evalCount=%d elapsed=%s", b.marketSeen, b.evalCount, time.Since(b.start))
	}
	if b.marketSeen {
		select {
		case <-b.done:
		default:
			close(b.done)
		}
	}
	return nil
}

// TestMarketDataViaManagerCreate confirms the same manager.Create dispatch
// fix that made System()/OnSol work also makes OnMarket (and friends) work
// -- the data arbv1's actual trading Evaluate() depends on,
// which was never directly tested by TestSubscribeViaManagerCreate above.
func TestMarketDataViaManagerCreate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), managerTestTimeout)
	defer cancel()

	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(feePayerPath())
	if err != nil {
		t.Fatalf("failed to load fee payer: %s", err)
	}

	proxySock := fmt.Sprintf("unix://%s/.solpipe.bidder.proxy.sock", os.Getenv("HOME"))
	t.Setenv("STATE_URL", proxySock)
	t.Setenv("TXPROC_URL", proxySock)
	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		t.Fatalf("failed to dial bidder proxy at %s -- is the real bot's Solpipe proxy running? %s", proxySock, err)
	}

	candidates := make([]sgo.PublicKey, 0, len(marketDataCandidates))
	for _, c := range marketDataCandidates {
		candidates = append(candidates, sgo.MustPublicKeyFromBase58(c))
	}
	mb := &marketDataBrain{
		ctx:         ctx,
		t:           t,
		botMarketID: bidcommon.GetBotMarketID(),
		candidates:  candidates,
		done:        make(chan struct{}),
	}
	_, err = manager.Create(ctx, dialer, mb)
	if err != nil {
		t.Fatalf("manager.Create failed: %s", err)
	}
	select {
	case <-ctx.Done():
		t.Logf("[market] timed out after %s", managerTestTimeout)
	case <-mb.done:
	}
	if !mb.marketSeen {
		t.Errorf("Market(botMarketID) never went non-nil within %s via the full manager.Create stack -- OnMarket dispatch is still broken even though OnSol/System works", managerTestTimeout)
	}
	t.Logf("[market] opportunistic candidate matches: %d found", len(mb.foundTypes))
}

func TestSubscribeDepth2(t *testing.T) {
	ctx := context.Background()
	client := dialStateClient(t, ctx)

	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(feePayerPath())
	if err != nil {
		t.Fatalf("failed to load fee payer: %s", err)
	}
	childKey := common.DeriveChildKeyFromIndex(parentKey, 1)
	// Confirmed via `spl-token address --owner <childKey> --token
	// EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v --verbose` against
	// mainnet -- a real, currently-funded SPL token account (Token
	// Program owned, not System Program owned like the wallet keys
	// above).
	childUSDCAta := sgo.MustPublicKeyFromBase58("7ZSj9RArHWzT6RwYWzuFjSaPFh1q5PrptkYB7bpUnr8m")
	botMarketID := bidcommon.GetBotMarketID()

	t.Run("parent", func(t *testing.T) {
		runSubscribeCase(t, client, "parent depth=2", parentKey.PublicKey(), 2)
	})
	t.Run("parent_depth0", func(t *testing.T) {
		runSubscribeCase(t, client, "parent depth=0", parentKey.PublicKey(), 0)
	})
	t.Run("parent_depth1", func(t *testing.T) {
		runSubscribeCase(t, client, "parent depth=1", parentKey.PublicKey(), 1)
	})
	t.Run("child", func(t *testing.T) {
		runSubscribeCase(t, client, "child depth=2", childKey.PublicKey(), 2)
	})
	t.Run("child_usdc_ata", func(t *testing.T) {
		runSubscribeCase(t, client, fmt.Sprintf("child USDC ATA %s depth=2", childUSDCAta), childUSDCAta, 2)
	})
	t.Run("bot_market_id", func(t *testing.T) {
		runSubscribeCase(t, client, "botMarketID depth=0", botMarketID, 0)
	})
}
