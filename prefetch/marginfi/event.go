package marginfi

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

// fetch discovers marginfi-v2 groups and banks the same way
// orca.fetchWhirlpool discovers WhirlpoolConfigs and Whirlpools: an active,
// per-node Subscribe/ack chain (program -> group, then group -> bank) via
// util.PendingSubscriptionStatus, rather than a single bulk depth-bounded
// QuerySingleShot. A group is only reachable from the program root once its
// own account has actually been subscribed to and acknowledged -- unlike a
// bulk query, this doesn't depend on an edge having already been recorded
// by some earlier backfill; each Subscribe call asks the server to derive
// that specific subtree live.
//
// Groups/banks already fetched within the last SevenDaysOfSlots are skipped
// (see eventHandler.isFresh/isGroupFresh) -- prefetch.db itself is the
// dedup/staleness cache now, not an in-memory map, so persistence happens
// immediately per-account (handler.tx) rather than being collected and
// handed back over a channel at the end.
func (m *MarginFi) fetch(parentCtx context.Context, stateClient state.Client, db *sql.DB, entry *slog.Logger) error {
	ctx, cancel := context.WithCancelCause(parentCtx)
	doneSignalC := make(chan struct{}, 1)
	err := stateClient.Hook(createHandler(ctx, cancel, doneSignalC, db, entry, maxSubscriptionCount))
	cancel(errors.New("complete"))
	if err != nil {
		return fmt.Errorf("hook failed: %s", err)
	}
	m.Banks, m.mBank, err = loadBanks(db)
	if err != nil {
		return fmt.Errorf("failed to load marginfi data after fetch: %s", err)
	}
	entry.Info(fmt.Sprintf("marginfi: found %d banks", len(m.Banks)))
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
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, doneSignalC chan<- struct{}, db *sql.DB, entry *slog.Logger, maxSubscriptionCount int) graph.Hook {
	eh := new(eventHandler)
	entry = entry.With("handler", "marginfi", "program", ProgramID)
	eh.ctx = logger.ToContext(ctx, entry)
	eh.ctx = ctx
	eh.cancel = cancel
	eh.doneSignalC = doneSignalC
	eh.db = db
	eh.logger = entry
	eh.slot = 0
	eh.maxSubscriptionCount = maxSubscriptionCount
	eh.sendSubCount = 0
	return eh
}

func (handler *eventHandler) CommitStart(slot graph.Slot) {
	handler.slot = slot
	if handler.db == nil {
		return
	}
	tx, err := handler.db.Begin()
	if err != nil {
		handler.cancel(fmt.Errorf("marginfi CommitStart: %w", err))
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
		handler.cancel(fmt.Errorf("marginfi CommitFinish: %w", err))
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
		handler.logger.Info(fmt.Sprintf("marginfi commit finish: count %d; inFlight %d; queued %d", x[0], x[1], x[2]))
	}
	return false
}

func (handler *eventHandler) Ctx() context.Context {
	return handler.ctx
}

func (handler *eventHandler) Init(g graph.Graph) error {
	handler.g = g
	handler.pss = util.CreatePendingSubscriptionList(handler.ctx, g, 50, 60)
	handler.logger.Info("init - 1")
	handler.sendSubCount++
	// Depth 2 = root + direct children: program -> group.
	handler.pss.Subscribe(ProgramID, graph.WeightAll, 2)
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

// isGroupFresh reports whether this MarginfiGroup's banks were already
// walked within the last SevenDaysOfSlots, per marginfi_group_seen.
func (handler *eventHandler) isGroupFresh(pubkey sgo.PublicKey) bool {
	if handler.db == nil {
		return false
	}
	var lastSlot uint64
	err := handler.dbQuery()(`SELECT last_slot FROM marginfi_group_seen WHERE pubkey = ?`, pubkey[:]).Scan(&lastSlot)
	if err != nil {
		return false
	}
	return lastSlot+SevenDaysOfSlots > handler.slot
}

// isBankFresh reports whether this Bank was already fetched within the
// last SevenDaysOfSlots, per marginfi_bank.last_slot.
func (handler *eventHandler) isBankFresh(pubkey sgo.PublicKey) bool {
	if handler.db == nil {
		return false
	}
	var lastSlot uint64
	err := handler.dbQuery()(`SELECT last_slot FROM marginfi_bank WHERE pubkey = ?`, pubkey[:]).Scan(&lastSlot)
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
	var discriminator [8]byte
	if len(body) < 8 {
		return
	}
	copy(discriminator[:], body[0:8])
	switch discriminator {
	case DiscMarginfiGroup:
		if handler.isGroupFresh(header.Pubkey) {
			return
		}
		if handler.tx == nil {
			handler.logger.Error("missing tx handler")
			handler.cancel(errors.New("missing tx handler"))
			return
		}
		if _, err := handler.tx.Exec(
			`INSERT OR REPLACE INTO marginfi_group_seen (pubkey, last_slot) VALUES (?, ?)`,
			header.Pubkey[:], handler.slot,
		); err != nil {
			handler.cancel(fmt.Errorf("marginfi insert group_seen: %w", err))
			return
		}
		handler.sendSubCount++
		// Depth 2 again, now rooted at this group: group -> bank.
		handler.pss.Subscribe(header.Pubkey, graph.WeightAll, 2)
	case DiscBank:
		if handler.isBankFresh(header.Pubkey) {
			return
		}
		bank := parseBank(header.Pubkey, body)
		if bank == nil {
			return
		}
		if handler.tx == nil {
			handler.logger.Error("missing tx handler")
			handler.cancel(errors.New("missing tx handler"))
			return
		}
		if _, err := handler.tx.Exec(`
			INSERT OR REPLACE INTO marginfi_bank
			(pubkey, group_pubkey, mint, oracle_setup, oracle_key, last_slot)
			VALUES (?,?,?,?,?,?)`,
			bank.Pubkey[:], bank.Group[:], bank.Mint[:], bank.OracleSetup, bank.OracleKey[:], handler.slot,
		); err != nil {
			handler.cancel(fmt.Errorf("marginfi insert bank: %w", err))
			return
		}
	}
}
