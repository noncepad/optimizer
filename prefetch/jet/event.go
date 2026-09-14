package jet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// fetch discovers Jet Protocol V1 markets and reserves via an active
// Subscribe/ack chain (program -> market, then market -> reserve) instead
// of a single bulk QuerySingleShot -- see orca.fetchWhirlpool for the same
// pattern applied to Orca's program -> config -> pool tree. Jet V1 has no
// Anchor discriminator, so accounts are dispatched by exact byte length
// (marketLen/reserveLen), same as the old QuerySingleShot-based fetch.
// Accounts are accumulated in memory exactly as that old fetch did;
// nothing is persisted to db until Create's caller does its usual bulk
// insertReserves afterward.
func (j *Jet) fetch(parentCtx context.Context, stateClient state.Client, entry *slog.Logger) error {
	ctx, cancel := context.WithCancelCause(parentCtx)
	handler := createHandler(ctx, cancel, entry)
	err := stateClient.Hook(handler)
	cancel(errors.New("complete"))
	if err != nil {
		return err
	}
	j.Reserves = make([]*Reserve, 0, len(handler.mReserve))
	j.mReserve = make(map[sgo.PublicKey]int, len(handler.mReserve))
	for pk, v := range handler.mReserve {
		j.mReserve[pk] = len(j.Reserves)
		j.Reserves = append(j.Reserves, v)
	}
	entry.Info(fmt.Sprintf("jet: found %d reserves", len(j.Reserves)))
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
	mReserve     map[sgo.PublicKey]*Reserve
	// mMarket dedupes market -> reserve subscribes -- pss.Subscribe already
	// dedupes internally too, but this avoids re-deriving the same depth-2
	// subscribe request on every repeat market account push.
	mMarket map[sgo.PublicKey]struct{}
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, entry *slog.Logger) *eventHandler {
	eh := new(eventHandler)
	entry = entry.With("handler", "jet", "program", ProgramID)
	eh.ctx = logger.ToContext(ctx, entry)
	eh.ctx = ctx
	eh.cancel = cancel
	eh.logger = entry
	eh.mReserve = make(map[sgo.PublicKey]*Reserve)
	eh.mMarket = make(map[sgo.PublicKey]struct{})
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
		handler.logger.Info(fmt.Sprintf("jet commit finish: count %d; inFlight %d; queued %d", x[0], x[1], x[2]))
	}
	return false
}

func (handler *eventHandler) Ctx() context.Context {
	return handler.ctx
}

func (handler *eventHandler) Init(g graph.Graph) error {
	handler.g = g
	handler.pss = util.CreatePendingSubscriptionList(handler.ctx, g, 50, 60)
	handler.sendSubCount++
	// Depth 2 = root + direct children: program -> market.
	handler.pss.Subscribe(ProgramID, graph.WeightAll, 2)
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
	if !header.Owner.Equals(ProgramID) {
		return
	}
	body := a.Data()
	switch len(body) {
	case marketLen:
		if _, present := handler.mMarket[header.Pubkey]; present {
			return
		}
		handler.mMarket[header.Pubkey] = struct{}{}
		handler.sendSubCount++
		// Depth 2 again, now rooted at this market: market -> reserve.
		handler.pss.Subscribe(header.Pubkey, graph.WeightAll, 2)
	case reserveLen:
		reserve := parseReserve(header.Pubkey, body)
		if reserve == nil {
			return
		}
		handler.mReserve[header.Pubkey] = reserve
	}
}
