package pumpswap

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

// pumpswapQuietSlots is the same generous quiet window Pump.fun needed
// (see optimizer/prefetch/pumpfun/event.go's pumpfunQuietSlots doc
// comment) -- this is the same "one big depth-3 walk, no incremental
// re-subscribes" shape that caused Pump.fun's premature-cutoff bug with
// the 60-slot default, so start with real patience from the outset rather
// than rediscovering the same issue empirically again.
const pumpswapQuietSlots = 4500

// fetch discovers PumpSwap pools via an active Subscribe/ack chain -- see
// orca.fetchWhirlpool for the same pattern applied to a real
// program-account tree, and pumpfun's own fetch doc comment for why a
// single deep Subscribe call (rather than Orca's incremental
// config-then-pool cascade) is used here.
//
// Unlike Pump.fun's BondingCurve, a Pool account's own data already
// contains both mints AND both vault pubkeys (see pool.go's parsePool),
// so there's no mint-discovery problem here -- a single
// Subscribe(globalConfigPDA, WeightAll, 3) walks global_config -> pool
// (edge-generator's unconditional `global_config -> pool` edge) -> {base
// vault, quote vault} (the generic SolToken `owner -> token_account` edge)
// in one registered walk. Only vault *balances* can arrive before or
// after their owning Pool account (handled the same way orca/event.go's
// mPendingToken handles a vault arriving before its pool).
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

func (p *Pumpswap) fetch(parentCtx context.Context, stateClient state.Client, logger_ *slog.Logger, maxSubscriptionCount int, db *sql.DB) error {
	if err := fetch(parentCtx, stateClient, logger_, maxSubscriptionCount, db); err != nil {
		return err
	}
	pools, err := loadPools(db)
	if err != nil {
		return fmt.Errorf("failed to load pumpswap data after fetch: %s", err)
	}
	p.Pools = pools
	return nil
}

// vaultRef identifies which pool (and which side) a vault pubkey belongs
// to -- populated the moment its owning Pool account is parsed.
type vaultRef struct {
	pool   sgo.PublicKey
	isBase bool
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
	mVaultToPool         map[sgo.PublicKey]vaultRef
	// mPendingBalance holds a vault's parsed balance when it arrives
	// before its owning Pool account -- same reordering issue
	// orca/event.go's mPendingToken solves, just keyed by vault pubkey
	// directly since we don't need findPoolByVault's db lookup (the Pool
	// account itself tells us the vault pubkeys once parsed).
	mPendingBalance map[sgo.PublicKey]uint64
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, doneSignalC chan<- struct{}, entry *slog.Logger, maxSubscriptionCount int, db *sql.DB) graph.Hook {
	eh := new(eventHandler)
	entry = entry.With("handler", "pumpswap", "program", ProgramID)
	eh.ctx = logger.ToContext(ctx, entry)
	eh.ctx = ctx
	eh.cancel = cancel
	eh.doneSignalC = doneSignalC
	eh.logger = entry
	eh.maxSubscriptionCount = maxSubscriptionCount
	eh.db = db
	eh.mVaultToPool = make(map[sgo.PublicKey]vaultRef, 10*1_024)
	eh.mPendingBalance = make(map[sgo.PublicKey]uint64)
	return eh
}

func (handler *eventHandler) CommitStart(slot graph.Slot) {
	handler.slot = slot
	if handler.db == nil {
		return
	}
	tx, err := handler.db.Begin()
	if err != nil {
		handler.cancel(fmt.Errorf("pumpswap CommitStart: %w", err))
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
		handler.cancel(fmt.Errorf("pumpswap CommitFinish: %w", err))
		return false
	}
	handler.tx = nil

	if handler.sendSubCount == 0 {
		return false
	}
	if handler.pss.Check(handler.slot) {
		handler.cancel(nil)
		return true
	}
	x := handler.pss.Count()
	if handler.slot%50 == 0 {
		handler.logger.Info(fmt.Sprintf("pumpswap commit finish: count %d; inFlight %d; queued %d", x[0], x[1], x[2]))
	}
	return false
}

func (handler *eventHandler) Ctx() context.Context {
	return handler.ctx
}

func (handler *eventHandler) Init(g graph.Graph) error {
	handler.g = g
	handler.pss = util.CreatePendingSubscriptionList(handler.ctx, g, handler.maxSubscriptionCount, pumpswapQuietSlots)
	handler.sendSubCount++
	handler.pss.Subscribe(globalConfigPDA, graph.WeightAll, 3)
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
	body := a.Data()

	if header.Owner.Equals(ProgramID) {
		if len(body) < 8 {
			return
		}
		var discriminator [8]byte
		copy(discriminator[:], body[0:8])
		if discriminator != poolDiscriminator {
			return
		}
		pool, err := parsePool(header.Pubkey, body)
		if err != nil {
			handler.logger.Warn(fmt.Sprintf("pumpswap: failed to parse pool %s: %s", header.Pubkey, err))
			return
		}
		if handler.tx == nil {
			handler.logger.Error("missing tx handler")
			handler.cancel(errors.New("missing tx handler"))
			return
		}
		if err = upsertPool(handler.tx, pool); err != nil {
			handler.cancel(err)
			return
		}
		handler.mVaultToPool[pool.BaseVault] = vaultRef{pool: pool.Pool, isBase: true}
		handler.mVaultToPool[pool.QuoteVault] = vaultRef{pool: pool.Pool, isBase: false}
		for _, vault := range []sgo.PublicKey{pool.BaseVault, pool.QuoteVault} {
			if amount, present := handler.mPendingBalance[vault]; present {
				delete(handler.mPendingBalance, vault)
				ref := handler.mVaultToPool[vault]
				if err = saveBalance(handler.tx, ref.pool, ref.isBase, amount); err != nil {
					handler.cancel(err)
					return
				}
			}
		}
		return
	}

	if !sgotkn.ProgramID.Equals(header.Owner) {
		return
	}
	if len(body) < 165 || 200 <= len(body) {
		return
	}
	b := new(sgotkn.Account)
	if err := bin.NewBorshDecoder(body).Decode(b); err != nil {
		handler.logger.Warn(fmt.Sprintf("pumpswap: failed to parse vault %s: %s", header.Pubkey, err))
		return
	}
	ref, present := handler.mVaultToPool[header.Pubkey]
	if !present {
		handler.mPendingBalance[header.Pubkey] = b.Amount
		return
	}
	if handler.tx == nil {
		handler.logger.Error("missing tx handler")
		handler.cancel(errors.New("missing tx handler"))
		return
	}
	if err := saveBalance(handler.tx, ref.pool, ref.isBase, b.Amount); err != nil {
		handler.cancel(err)
	}
}
