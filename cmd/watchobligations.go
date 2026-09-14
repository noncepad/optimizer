package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/obligation"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/common"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// WatchObligationsCmd polls the trading wallet's own Solend/Kamino
// lending obligations (one per multimodelv1 trade type, since removed --
// see obligation.TrackedObligations) on an interval and records any
// deposit/borrow that changed into prefetch.db's
// obligation_position_snapshot table. Reads go through the same shared
// internal state.Client (gRPC/geyser-backed graph, dialed via
// bidder.CreateDialer) every other watch-* command and prefetch/*
// package uses -- not the public Solana RPC endpoint. This used to hit
// api.mainnet-beta.solana.com directly via rpc.Client.GetMultipleAccounts,
// which was the prime suspect behind a recurring multi-hour silent
// freeze (a plain public HTTP client has no visibility into or control
// over that endpoint's own reliability); switching to the same internal
// client the rest of this codebase already depends on removes that
// dependency entirely, for free.
type WatchObligationsCmd struct {
	ParentKey    string        `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	PollInterval time.Duration `option:"poll" default:"30s" help:"how often to re-read the trading wallet's obligations."`
}

// obligationsQuietSlots mirrors util.FetchWalletBalance's own
// walletBalanceQuietSlots choice: this fetch is a handful of individually
// known, fixed addresses (not a bulk program crawl), so a burst arrives
// in one shot, but the same conservative quiet window is reused here
// rather than inventing a new untested value.
const obligationsQuietSlots = 10

func (r *WatchObligationsCmd) Run(rc *RunConfig) error {
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(r.ParentKey)
	if err != nil {
		return fmt.Errorf("failed to load authorizer: %s", err)
	}
	childKey := common.DeriveChildKeyFromIndex(parentKey, tradingChildKeyIndex)
	wallet := childKey.PublicKey()

	ctx := rc.Ctx
	cancel := rc.Cancel
	defer cancel(nil)

	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		return fmt.Errorf("failed to create dialer: %s", err)
	}
	stateClient := dialer.State()

	prefetchDB, err := store.Open(getDBFilePath())
	if err != nil {
		return fmt.Errorf("failed to open prefetch db: %s", err)
	}
	defer func() {
		_ = prefetchDB.Close()
	}()

	entry := logger.FromContext(ctx)
	fmt.Printf("watch-obligations: tracking %s (poll every %s)\n", wallet, r.PollInterval)

	ticker := time.NewTicker(r.PollInterval)
	defer ticker.Stop()
	for {
		pollObligationsOnce(ctx, stateClient, prefetchDB.Raw(), wallet, entry)
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
		}
	}
}

// obligationsHandler is a minimal graph.Hook: subscribe once to every
// tracked obligation address at depth 0 (each address is already the
// exact account we want, not a root to expand from -- unlike
// util.FetchWalletBalance's depth-2 wallet-plus-token-children walk),
// then just wait for PendingSubscriptionStatus to report quiet.
// pollObligationsOnce reads the resulting state straight out of the
// EdgeManager afterward, so OnAccount itself doesn't need to do anything.
type obligationsHandler struct {
	ctx   context.Context
	addrs []sgo.PublicKey
	g     graph.Graph
	pss   *util.PendingSubscriptionStatus
	slot  graph.Slot
}

func (h *obligationsHandler) Ctx() context.Context { return h.ctx }

func (h *obligationsHandler) Init(g graph.Graph) error {
	h.g = g
	h.pss = util.CreatePendingSubscriptionList(h.ctx, g, len(h.addrs), obligationsQuietSlots)
	for _, addr := range h.addrs {
		h.pss.Subscribe(addr, graph.WeightAll, 0)
	}
	return nil
}

func (h *obligationsHandler) OnSlot(slot graph.Slot, status graph.SlotStatus) {}

func (h *obligationsHandler) CommitStart(slot graph.Slot) {
	h.slot = slot
}

func (h *obligationsHandler) OnAccount(a graph.Account, isNew bool) {}

func (h *obligationsHandler) CommitFinish() bool {
	return h.pss.Check(h.slot)
}

// pollObligationsOnce derives every tracked obligation's real address,
// fetches them all in one Hook subscription, and records whatever
// deposit(s)/borrow(s) changed. An obligation that doesn't exist yet
// on-chain (never bootstrapped -- e.g. pair trading before its first
// real open) is simply skipped, not an error: a nil EdgeManager account
// or one with empty data is the normal "not created yet" case here, same
// as any other real account this bot tracks before it's been
// initialized.
func pollObligationsOnce(ctx context.Context, stateClient state.Client, db *sql.DB, wallet sgo.PublicKey, entry *slog.Logger) {
	start := time.Now()
	now := start
	addrs := make([]sgo.PublicKey, len(obligation.TrackedObligations))
	for i, t := range obligation.TrackedObligations {
		addr, err := obligation.Address(wallet, t.Protocol, t.ID)
		if err != nil {
			entry.Error(fmt.Sprintf("watch-obligations: derive %s/%s address: %s", t.Protocol, t.TradeType, err))
			return
		}
		addrs[i] = addr
	}

	hookCtx, cancel := context.WithCancelCause(ctx)
	handler := &obligationsHandler{ctx: hookCtx, addrs: addrs}
	err := stateClient.Hook(handler)
	cancel(errors.New("obligations poll complete"))
	if err != nil {
		entry.Error(fmt.Sprintf("watch-obligations: fetch obligations: %s", err))
		return
	}

	em := handler.g.EdgeManager()
	em.Lock()
	defer em.Unlock()

	changed := 0
	for i, t := range obligation.TrackedObligations {
		account := em.UnsafeAccount(addrs[i])
		if account == nil {
			continue
		}
		data := account.Data()
		if len(data) == 0 {
			// Not created yet (e.g. pair trading before its first real
			// open): the graph client reports these as a present-but-
			// empty account rather than a nil one -- see this file's
			// investigation notes. Same "skip, not an error" case the
			// old RPC-based acc == nil check handled.
			continue
		}

		var parsed *obligation.Parsed
		switch t.Protocol {
		case obligation.ProtocolSolend:
			parsed, err = obligation.ParseSolend(data)
		case obligation.ProtocolKamino:
			parsed, err = obligation.ParseKamino(data)
		}
		if err != nil {
			entry.Error(fmt.Sprintf("watch-obligations: parse %s/%s: %s", t.Protocol, t.TradeType, err))
			continue
		}

		for _, e := range parsed.Deposits {
			if recordObligationEntry(db, now, wallet, t.Protocol, t.TradeType, t.ID, "deposit", e, entry) {
				changed++
			}
		}
		for _, e := range parsed.Borrows {
			if recordObligationEntry(db, now, wallet, t.Protocol, t.TradeType, t.ID, "borrow", e, entry) {
				changed++
			}
		}
	}
	// Heartbeat: recordObligationEntry only logs on error, so without
	// this line a perfectly healthy poll cycle (no on-chain changes)
	// produces zero output -- indistinguishable from a hung process by
	// log-watching alone. Logging every cycle makes silence itself the
	// hang signal.
	entry.Info(fmt.Sprintf("watch-obligations: poll ok (%d changed, %s)", changed, time.Since(start)))
}

func recordObligationEntry(
	db *sql.DB, now time.Time, wallet sgo.PublicKey,
	protocol obligation.Protocol, tradeType obligation.TradeType, id uint8,
	kind string, e obligation.Entry, entry *slog.Logger,
) bool {
	changed, err := obligation.RecordIfChanged(db, obligation.Snapshot{
		Time: now, Wallet: wallet, Protocol: protocol, TradeType: tradeType, ObligationID: id,
		Kind: kind, Reserve: e.Reserve, Amount: e.Amount,
	})
	if err != nil {
		entry.Error(fmt.Sprintf("watch-obligations: record %s/%s %s (reserve %s): %s", protocol, tradeType, kind, e.Reserve, err))
		return false
	}
	return changed
}
