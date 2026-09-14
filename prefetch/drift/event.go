package drift

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

// fetch discovers Drift v2 SpotMarket accounts via an active
// Subscribe/ack chain (program -> spot market, depth 1 -- SpotMarket is
// directly reachable from the program root) instead of a single bulk
// QuerySingleShot -- see orca.fetchWhirlpool for the same pattern applied
// to a deeper (program -> config -> pool) tree. Accounts are accumulated
// in memory exactly as the old QuerySingleShot-based fetch did; nothing is
// persisted to db until Create's caller does its usual bulk
// insertSpotMarkets afterward.
func (d *Drift) fetch(parentCtx context.Context, stateClient state.Client, entry *slog.Logger) error {
	ctx, cancel := context.WithCancelCause(parentCtx)
	handler := createHandler(ctx, cancel, entry)
	err := stateClient.Hook(handler)
	cancel(errors.New("complete"))
	if err != nil {
		return err
	}
	d.SpotMarkets = make([]*SpotMarket, 0, len(handler.mMarket))
	d.mMarket = make(map[sgo.PublicKey]int, len(handler.mMarket))
	for pk, v := range handler.mMarket {
		d.mMarket[pk] = len(d.SpotMarkets)
		d.SpotMarkets = append(d.SpotMarkets, v)
	}
	entry.Info(fmt.Sprintf("drift: found %d spot markets", len(d.SpotMarkets)))
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
	mMarket      map[sgo.PublicKey]*SpotMarket
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, entry *slog.Logger) *eventHandler {
	eh := new(eventHandler)
	entry = entry.With("handler", "drift", "program", ProgramID)
	eh.ctx = logger.ToContext(ctx, entry)
	eh.ctx = ctx
	eh.cancel = cancel
	eh.logger = entry
	eh.mMarket = make(map[sgo.PublicKey]*SpotMarket)
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
		handler.logger.Info(fmt.Sprintf("drift commit finish: count %d; inFlight %d; queued %d", x[0], x[1], x[2]))
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
	// Depth 1 = root only: program -> spot market is a direct edge, no
	// intermediate hop to walk.
	handler.pss.Subscribe(ProgramID, graph.WeightAll, 1)
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
	var discriminator [8]byte
	if len(body) < 8 {
		return
	}
	copy(discriminator[:], body[0:8])
	if discriminator != DiscSpotMarket {
		return
	}
	market := parseSpotMarket(header.Pubkey, body)
	if market == nil {
		return
	}
	handler.mMarket[header.Pubkey] = market
}
