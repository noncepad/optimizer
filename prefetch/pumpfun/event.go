package pumpfun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	bin "github.com/gagliardetto/binary"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

// fetch discovers Pump.fun bonding curves via an active Subscribe/ack
// chain -- see orca.fetchWhirlpool for the same pattern applied to a real
// program-account tree.
//
// A BondingCurve account's own data never stores its mint (verified
// against the IDL -- the mint is only implicit in the PDA seed, which
// can't be reverse-derived), so mint discovery has to come from elsewhere.
// Rather than scanning the whole SPL Token Program (which would surface
// every mint on Solana, not just pump.fun's), this walks pump.fun's own
// portion of the graph two hops deep from the fixed `global` PDA:
//
//	global -> bonding_curve -> associated_bonding_curve
//
// `global -> bonding_curve` is pumpfun.rs's own edge (every BondingCurve
// account gets one, unconditionally). `bonding_curve ->
// associated_bonding_curve` is the generic SolToken `owner -> token_account`
// edge (edge-generator/src/primitive/soltoken.rs) -- associated_bonding_curve
// is a standard SPL token account whose own `owner` field is the
// bonding_curve PDA, and its first 32 bytes are its mint. A single
// `Subscribe(globalPDA, WeightAll, 3)` (3 = 2 hops, per the Depth doc
// comment) reaches both levels in one registered walk; every account this
// way is a real pump.fun account by construction, no client-side filtering
// needed (unlike a token-program-wide scan).
//
// BondingCurve and associated_bonding_curve accounts can arrive in either
// order, so both pieces are held in mPending (keyed by the bonding_curve
// pubkey) until both are present -- see tryFinalize.
func fetch(parentCtx context.Context, stateClient state.Client, logger_ *slog.Logger, maxSubscriptionCount int, db *sql.DB) error {
	ctx, cancel := context.WithCancelCause(parentCtx)
	doneSignalC := make(chan struct{}, 1)
	err := stateClient.Hook(createHandler(ctx, cancel, doneSignalC, logger_, maxSubscriptionCount, db))
	cancel(errors.New("complete"))
	if err != nil {
		return fmt.Errorf("hook failed: %s", err)
	}
	return nil
}

func (p *Pumpfun) fetch(parentCtx context.Context, stateClient state.Client, logger_ *slog.Logger, maxSubscriptionCount int, db *sql.DB) error {
	if err := fetch(parentCtx, stateClient, logger_, maxSubscriptionCount, db); err != nil {
		return err
	}
	curves, err := loadCurves(db)
	if err != nil {
		return fmt.Errorf("failed to load pumpfun data after fetch: %s", err)
	}
	p.Curves = curves
	return nil
}

// pendingCurve holds whichever half of a (BondingCurve, associated_bonding_curve)
// pair has arrived so far -- see tryFinalize.
type pendingCurve struct {
	curveData []byte // raw BondingCurve account body, nil until it arrives
	mint      sgo.PublicKey
}

type eventHandler struct {
	ctx                  context.Context
	cancel               context.CancelCauseFunc
	g                    graph.Graph
	doneSignalC          chan<- struct{}
	slot                 uint64
	logger               *slog.Logger
	maxSubscriptionCount int
	db                   *sql.DB
	tx                   *sql.Tx
	sendSubCount         uint32
	pss                  *util.PendingSubscriptionStatus
	// mPending is keyed by bonding_curve pubkey -- see pendingCurve's doc
	// comment.
	mPending map[sgo.PublicKey]*pendingCurve
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, doneSignalC chan<- struct{}, entry *slog.Logger, maxSubscriptionCount int, db *sql.DB) graph.Hook {
	eh := new(eventHandler)
	entry = entry.With("handler", "pumpfun", "program", ProgramID)
	eh.ctx = logger.ToContext(ctx, entry)
	eh.ctx = ctx
	eh.cancel = cancel
	eh.doneSignalC = doneSignalC
	eh.logger = entry
	eh.maxSubscriptionCount = maxSubscriptionCount
	eh.db = db
	eh.mPending = make(map[sgo.PublicKey]*pendingCurve, 10*1_024)
	return eh
}

func (handler *eventHandler) CommitStart(slot graph.Slot) {
	handler.slot = slot
	if handler.db == nil {
		return
	}
	tx, err := handler.db.Begin()
	if err != nil {
		handler.cancel(fmt.Errorf("pumpfun CommitStart: %w", err))
		return
	}
	handler.tx = tx
}

func (handler *eventHandler) CommitFinish() bool {
	if handler.tx == nil {
		handler.cancel(errors.New("missing sql handler"))
		return false
	}
	if err := handler.tx.Commit(); err != nil {
		handler.cancel(fmt.Errorf("pumpfun CommitFinish: %w", err))
		return false
	}
	handler.tx = nil

	if handler.sendSubCount == 0 {
		return false
	}
	if handler.pss.Check(handler.slot) {
		return true
	}
	x := handler.pss.Count()
	if handler.slot%50 == 0 {
		handler.logger.Info(fmt.Sprintf("pumpfun commit finish: count %d; inFlight %d; queued %d", x[0], x[1], x[2]))
	}
	return false
}

func (handler *eventHandler) Ctx() context.Context {
	return handler.ctx
}

// pumpfunQuietSlots is much larger than every other module's default (60,
// ~24s) -- confirmed empirically this session: with only ONE subscribe
// ever registered (unlike Orca/Sanctum, which keep resetting the quiet
// timer by issuing new Subscribe calls as they discover new pools/LSTs),
// the queue drains to empty almost immediately after the single depth-3
// walk acks, and the 60-slot default declared the fetch "done" after just
// ~27 seconds -- only 9 bonding curves, while the download-arb process
// went on to spend minutes fetching other, smaller dexes. Pump.fun's real
// dataset is far larger than what a short quiet window was tuned for, so
// this needs real patience rather than a quick "burst then done" pattern.
// ~4500 slots is ~30 minutes at 400ms/slot -- a generous multiple of the
// default, not derived from a known true dataset size (no such figure is
// available), so treat this as a starting point to tune further based on
// how many rows an actual run accumulates before going quiet.
const pumpfunQuietSlots = 4500

func (handler *eventHandler) Init(g graph.Graph) error {
	handler.g = g
	handler.pss = util.CreatePendingSubscriptionList(handler.ctx, g, handler.maxSubscriptionCount, pumpfunQuietSlots)
	handler.sendSubCount++
	handler.pss.Subscribe(globalPDA, graph.WeightAll, 3)
	handler.logger.Warn("...........Init...................")
	return nil
}

func (handler *eventHandler) OnSlot(slot graph.Slot, status graph.SlotStatus) {
	_ = slot
	_ = status
}

func (handler *eventHandler) pending(bondingCurve sgo.PublicKey) *pendingCurve {
	p, present := handler.mPending[bondingCurve]
	if !present {
		p = new(pendingCurve)
		handler.mPending[bondingCurve] = p
	}
	return p
}

// tryFinalize persists bondingCurve's row once both halves (its own
// BondingCurve account data and its associated_bonding_curve's mint) have
// arrived -- doesn't delete the pending entry afterward, so a later update
// to either half during the same walk (e.g. a real trade changing reserves)
// naturally re-finalizes and re-upserts.
func (handler *eventHandler) tryFinalize(bondingCurve sgo.PublicKey) {
	p, present := handler.mPending[bondingCurve]
	if !present || p.curveData == nil || p.mint.IsZero() {
		return
	}
	entry, err := parseBondingCurve(p.mint, bondingCurve, p.curveData)
	if err != nil {
		handler.logger.Warn(fmt.Sprintf("pumpfun: failed to parse bonding curve %s: %s", bondingCurve, err))
		return
	}
	// Sanity check: the discovered pair should match the deterministic PDA
	// derivation -- catches a discovery bug (e.g. a mis-keyed pending
	// entry) rather than silently persisting an inconsistent row.
	if derived, err := bondingCurveAddress(p.mint); err == nil && !derived.Equals(bondingCurve) {
		handler.logger.Warn(fmt.Sprintf(
			"pumpfun: bonding_curve/mint mismatch: observed bonding_curve=%s mint=%s, but PDA(mint)=%s",
			bondingCurve, p.mint, derived,
		))
		return
	}
	if handler.tx == nil {
		handler.logger.Error("missing tx handler")
		handler.cancel(errors.New("missing tx handler"))
		return
	}
	if err = upsertCurve(handler.tx, entry); err != nil {
		handler.cancel(err)
	}
}

func (handler *eventHandler) OnAccount(a graph.Account, isNew bool) {
	_ = isNew
	header := a.Header()
	body := a.Data()

	if header.Owner.Equals(ProgramID) {
		if len(body) < 8 {
			return
		}
		var discriminator [8]byte
		copy(discriminator[:], body[0:8])
		if discriminator != bondingCurveDiscriminator {
			return
		}
		handler.pending(header.Pubkey).curveData = body
		handler.tryFinalize(header.Pubkey)
		return
	}

	if !sgotkn.ProgramID.Equals(header.Owner) {
		return
	}
	// associated_bonding_curve is a standard SPL token account -- only
	// reachable here via the bonding_curve -> associated_bonding_curve
	// edge (we subscribed to nothing else that could deliver a
	// token-program-owned account), so every delivery genuinely belongs to
	// one of our tracked curves.
	if len(body) < 165 || 200 <= len(body) {
		return
	}
	b := new(sgotkn.Account)
	if err := bin.NewBorshDecoder(body).Decode(b); err != nil {
		handler.logger.Warn(fmt.Sprintf("pumpfun: failed to parse associated_bonding_curve %s: %s", header.Pubkey, err))
		return
	}
	handler.pending(b.Owner).mint = b.Mint
	handler.tryFinalize(b.Owner)
}
