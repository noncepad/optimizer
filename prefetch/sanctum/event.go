package sanctum

import (
	"context"
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

// fetch discovers the Sanctum S Controller's pool_state and
// lst_state_list PDAs via an active Subscribe/ack chain instead of a
// single bulk QuerySingleShot -- see orca.fetchWhirlpool for the same
// pattern applied to a real program-account tree. Once lst_state_list
// arrives, its LST mints are known, so each LST's pool-reserves ATA
// (FindPoolReservesAddress) is derived and subscribed to in a second wave
// -- same cascading-discovery shape as orca/event.go's pool -> vault
// subscription, just triggered off one parsed account instead of a
// stream of newly-inserted pool rows. Nothing is persisted to db until
// Create's caller does its usual bulk insertLsts afterward.
func (s *Sanctum) fetch(parentCtx context.Context, stateClient state.Client, entry *slog.Logger) error {
	ctx, cancel := context.WithCancelCause(parentCtx)
	handler := createHandler(ctx, cancel, entry)
	err := stateClient.Hook(handler)
	cancel(errors.New("complete"))
	if err != nil {
		return err
	}
	if handler.lstData == nil {
		return fmt.Errorf("lst_state_list account %s not found", LstStateListPDA)
	}
	s.Lsts = handler.lsts
	entry.Info(fmt.Sprintf("sanctum: found %d LSTs", len(s.Lsts)))
	reg, err := poolStateRegistry()
	if err != nil {
		entry.Warn(fmt.Sprintf("sanctum: failed to load vendored lst list, pool_state will be empty: %s", err))
	} else {
		for _, e := range s.Lsts {
			if pool, ok := reg[e.Mint]; ok {
				e.PoolState = pool
			}
		}
	}
	return nil
}

type eventHandler struct {
	ctx          context.Context
	cancel       context.CancelCauseFunc
	g            graph.Graph
	slot         uint64
	logger       *slog.Logger
	pss          *util.PendingSubscriptionStatus
	sendSubCount uint32
	lstData      []byte
	// lsts is populated once, the moment lstData is parsed (see
	// subscribeReserves) -- fetch() reads it back after the hook loop
	// completes instead of re-parsing lstData itself.
	lsts []*LstEntry
	// mReserve maps a derived pool-reserves ATA back to the LstEntry it
	// belongs to, so OnAccount's token-account branch can find which
	// entry to update -- same purpose as orca/event.go's findPoolByVault,
	// just a map instead of a db lookup since there's no pool table row
	// to query here.
	mReserve map[sgo.PublicKey]*LstEntry
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, entry *slog.Logger) *eventHandler {
	eh := new(eventHandler)
	entry = entry.With("handler", "sanctum", "program", ProgramID)
	eh.ctx = logger.ToContext(ctx, entry)
	eh.ctx = ctx
	eh.cancel = cancel
	eh.logger = entry
	return eh
}

func (handler *eventHandler) CommitStart(slot graph.Slot) {
	handler.slot = slot
}

func (handler *eventHandler) CommitFinish() bool {
	if handler.sendSubCount == 0 {
		return false
	}
	if handler.pss.Check(handler.slot) {
		return true
	}
	x := handler.pss.Count()
	if handler.slot%50 == 0 {
		handler.logger.Info(fmt.Sprintf("sanctum commit finish: count %d; inFlight %d; queued %d", x[0], x[1], x[2]))
	}
	return false
}

func (handler *eventHandler) Ctx() context.Context {
	return handler.ctx
}

func (handler *eventHandler) Init(g graph.Graph) error {
	handler.g = g
	handler.pss = util.CreatePendingSubscriptionList(handler.ctx, g, 50, 60)
	handler.sendSubCount += 2
	handler.pss.Subscribe(poolStatePDA, graph.WeightAll, 1)
	handler.pss.Subscribe(LstStateListPDA, graph.WeightAll, 1)
	handler.logger.Warn("...........Init...................")
	return nil
}

func (handler *eventHandler) OnSlot(slot graph.Slot, status graph.SlotStatus) {
	_ = slot
	_ = status
}

func (handler *eventHandler) OnAccount(a graph.Account, isNew bool) {
	_ = isNew
	header := a.Header()
	if header.Pubkey.Equals(LstStateListPDA) {
		handler.lstData = a.Data()
		handler.subscribeReserves()
		return
	}
	if entry, ok := handler.mReserve[header.Pubkey]; ok {
		if !sgotkn.ProgramID.Equals(header.Owner) {
			return
		}
		body := a.Data()
		if len(body) < 165 {
			return
		}
		b := new(sgotkn.Account)
		if err := bin.NewBorshDecoder(body).Decode(b); err != nil {
			handler.cancel(fmt.Errorf("sanctum: failed to parse reserve token account %s: %w", header.Pubkey, err))
			return
		}
		entry.Reserve = b.Amount
	}
}

// subscribeReserves parses lstData the moment it arrives (rather than
// waiting for fetch() to do it after the hook loop completes) so each
// LST's pool-reserves ATA can be derived and subscribed to in this same
// run -- mirrors orca/event.go's pool -> vault cascading-subscribe
// pattern. Guarded by handler.lsts so a duplicate/updated delivery of
// lst_state_list doesn't re-subscribe everything a second time.
func (handler *eventHandler) subscribeReserves() {
	if handler.lsts != nil {
		return
	}
	entries := ParseLstStateList(handler.lstData)
	handler.lsts = entries
	handler.mReserve = make(map[sgo.PublicKey]*LstEntry, len(entries))
	for _, e := range entries {
		reservePk, err := FindPoolReservesAddress(e.Mint)
		if err != nil {
			handler.logger.Warn(fmt.Sprintf("sanctum: failed to derive pool_reserves for mint %s: %s", e.Mint, err))
			continue
		}
		handler.mReserve[reservePk] = e
		handler.sendSubCount++
		handler.pss.Subscribe(reservePk, 0, 1)
	}
}
