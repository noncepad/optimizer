package solend

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
	sgo "github.com/gagliardetto/solana-go"
)

// maxSubscriptionCount bounds how many outstanding graph subscriptions
// fetch keeps in flight at once, mirroring orca.fetchWhirlpool's own
// concurrency limit.
const maxSubscriptionCount = 64

// SevenDaysOfSlots assumes ~400ms/slot (2.5 slots/sec), the same fallback
// bot/state/clock.go itself uses before real slot-timing data arrives --
// see raydium/cpmm's own OldSlot for the established precedent of this
// convention.
const SevenDaysOfSlots = 7 * 24 * 60 * 60 * 25 / 10 // 1,512,000 slots

// fetch discovers Solend lending markets and reserves the same way
// orca.fetchWhirlpool discovers WhirlpoolConfigs and Whirlpools: an active,
// per-node Subscribe/ack chain (program -> lending_market, then
// lending_market -> reserve) via util.PendingSubscriptionStatus, rather
// than a single bulk depth-bounded QuerySingleShot. A lending market is only
// reachable from the program root once its own account has actually been
// subscribed to and acknowledged -- unlike a bulk query, this doesn't
// depend on an edge having already been recorded by some earlier backfill;
// each Subscribe call asks the server to derive that specific subtree
// live.
//
// Lending markets/reserves already fetched within the last SevenDaysOfSlots
// are skipped (see eventHandler.isMarketFresh/isReserveFresh) -- prefetch.db
// itself is the dedup/staleness cache now, not an in-memory map, so
// persistence happens immediately per-account (handler.tx) rather than
// being collected and handed back over a channel at the end. force, when
// true, bypasses that staleness check entirely (see eventHandler.force) --
// without threading it this far down, Solend.Create's own force=true only
// ever decided whether to call fetch at all, while isMarketFresh/
// isReserveFresh kept silently skipping any market/reserve already marked
// seen within SevenDaysOfSlots regardless -- the opposite of what --force
// on the download-arb CLI promises ("re-fetch all dex data even if
// prefetch.db is already populated").
func (s *Solend) fetch(parentCtx context.Context, stateClient state.Client, db *sql.DB, entry *slog.Logger, force bool) error {
	ctx, cancel := context.WithCancelCause(parentCtx)
	doneSignalC := make(chan struct{}, 1)
	err := stateClient.Hook(createHandler(ctx, cancel, doneSignalC, db, entry, maxSubscriptionCount, force))
	cancel(errors.New("complete"))
	if err != nil {
		return fmt.Errorf("hook failed: %s", err)
	}
	s.Reserves, s.mReserve, err = loadReserves(db)
	if err != nil {
		return fmt.Errorf("failed to load solend data after fetch: %s", err)
	}
	entry.Info(fmt.Sprintf("solend: found %d reserves", len(s.Reserves)))
	return nil
}

type eventHandler struct {
	ctx                  context.Context
	cancel               context.CancelCauseFunc
	g                    graph.Graph
	doneSignalC          chan<- struct{}
	slot                 uint64
	logger               *slog.Logger
	maxSubscriptionCount int
	pss                  *util.PendingSubscriptionStatus
	db                   *sql.DB
	tx                   *sql.Tx
	sendSubCount         uint32
	// force, when true, makes isMarketFresh/isReserveFresh always report
	// stale -- see fetch's doc comment for why this has to be threaded
	// down here rather than only gating whether fetch runs at all.
	force bool
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, doneSignalC chan<- struct{}, db *sql.DB, entry *slog.Logger, maxSubscriptionCount int, force bool) graph.Hook {
	eh := new(eventHandler)
	entry = entry.With("handler", "solend", "program", ProgramID)
	eh.ctx = logger.ToContext(ctx, entry)
	eh.ctx = ctx
	eh.cancel = cancel
	eh.doneSignalC = doneSignalC
	eh.db = db
	eh.logger = entry
	eh.slot = 0
	eh.maxSubscriptionCount = maxSubscriptionCount
	eh.sendSubCount = 0
	eh.force = force
	return eh
}

func (handler *eventHandler) CommitStart(slot graph.Slot) {
	handler.slot = slot
	if handler.db == nil {
		return
	}
	tx, err := handler.db.Begin()
	if err != nil {
		handler.cancel(fmt.Errorf("solend CommitStart: %w", err))
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
		handler.cancel(fmt.Errorf("solend CommitFinish: %w", err))
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
		handler.logger.Info(fmt.Sprintf("solend commit finish: slot %d; count %d; inFlight %d; queued %d", handler.slot, x[0], x[1], x[2]))
	}
	return false
}

func (handler *eventHandler) Ctx() context.Context {
	return handler.ctx
}

func (handler *eventHandler) Init(g graph.Graph) error {
	handler.g = g
	handler.pss = util.CreatePendingSubscriptionList(handler.ctx, g, 50, 120)
	handler.logger.Info("init - 1")
	handler.sendSubCount++
	// Depth 2 = root + direct children: program -> lending_market.
	handler.pss.Subscribe(ProgramID, graph.WeightDirect, 2)
	handler.logger.Warn("...........Init...................")
	return nil
}

func (handler *eventHandler) OnSlot(slot graph.Slot, status graph.SlotStatus) {
	_ = slot
	_ = status
}

// dbQuery returns the QueryRow func to use for a per-account lookup: the
// open tx when one exists, handler.db otherwise. Must route through the
// open tx -- prefetch.db is opened with SetMaxOpenConns(1), and
// CommitStart already holds the only connection for the duration of a
// commit, so a fresh handler.db query while a tx is open would deadlock
// waiting for a connection that won't be released until the tx commits.
func (handler *eventHandler) dbQuery() func(query string, args ...any) *sql.Row {
	if handler.tx != nil {
		return handler.tx.QueryRow
	}
	return handler.db.QueryRow
}

// isMarketFresh reports whether this lending market's reserves were
// already walked within the last SevenDaysOfSlots, per
// solend_lending_market_seen. Always false when handler.force is set --
// otherwise a market already marked seen from an earlier run would keep
// getting silently skipped even when the caller explicitly asked for a
// forced re-fetch.
func (handler *eventHandler) isMarketFresh(pubkey sgo.PublicKey) bool {
	if handler.force || handler.db == nil {
		return false
	}
	var lastSlot uint64
	err := handler.dbQuery()(`SELECT last_slot FROM solend_lending_market_seen WHERE pubkey = ?`, pubkey[:]).Scan(&lastSlot)
	if err != nil {
		return false
	}
	return lastSlot+SevenDaysOfSlots > handler.slot
}

// isReserveFresh reports whether this Reserve was already fetched within
// the last SevenDaysOfSlots, per solend_reserve.last_slot. Same
// force-bypass reasoning as isMarketFresh.
func (handler *eventHandler) isReserveFresh(pubkey sgo.PublicKey) bool {
	if handler.force || handler.db == nil {
		return false
	}
	var lastSlot uint64
	err := handler.dbQuery()(`SELECT last_slot FROM solend_reserve WHERE pubkey = ?`, pubkey[:]).Scan(&lastSlot)
	if err != nil {
		return false
	}
	return lastSlot+SevenDaysOfSlots > handler.slot
}

func (handler *eventHandler) OnAccount(a graph.Account, isNew bool) {
	_ = isNew
	header := a.Header()
	if !header.Owner.Equals(ProgramID) {
		return
	}
	body := a.Data()
	switch len(body) {
	case lendingMarketLen:
		handler.logger.Info(fmt.Sprintf("lending market %s", header.Pubkey))
		if handler.isMarketFresh(header.Pubkey) {
			return
		}
		if handler.tx == nil {
			handler.logger.Error("missing tx handler")
			handler.cancel(errors.New("missing tx handler"))
			return
		}
		if _, err := handler.tx.Exec(
			`INSERT OR REPLACE INTO solend_lending_market_seen (pubkey, last_slot) VALUES (?, ?)`,
			header.Pubkey[:], handler.slot,
		); err != nil {
			handler.cancel(fmt.Errorf("solend insert lending_market_seen: %w", err))
			return
		}
		handler.sendSubCount++
		// Depth 2 again, now rooted at this lending market:
		// lending_market -> reserve.
		handler.pss.Subscribe(header.Pubkey, graph.WeightAll, 2)
	case reserveLen:
		if handler.isReserveFresh(header.Pubkey) {
			return
		}
		reserve := parseReserve(header.Pubkey, body)
		if reserve == nil {
			return
		}
		if handler.tx == nil {
			handler.logger.Error("missing tx handler")
			handler.cancel(errors.New("missing tx handler"))
			return
		}
		if _, err := handler.tx.Exec(`
			INSERT OR REPLACE INTO solend_reserve
			(pubkey, lending_market, mint, supply_vault, last_slot)
			VALUES (?,?,?,?,?)`,
			reserve.Pubkey[:], reserve.LendingMarket[:], reserve.Mint[:], reserve.SupplyVault[:], handler.slot,
		); err != nil {
			handler.cancel(fmt.Errorf("solend insert reserve: %w", err))
			return
		}
	}
}
