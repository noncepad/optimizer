package clmm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	bin "github.com/gagliardetto/binary"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

// errFetchComplete is the cancellation cause used when the fetch finishes
// successfully. Download() checks context.Cause(ctx) for this specific
// sentinel — canceling with a plain nil cause would make context.Cause
// report context.Canceled, which is indistinguishable from a real
// cancellation/failure once it races through Client.Hook()'s error paths.
var errFetchComplete = errors.New("clmm: fetch complete")

type eventHandler struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	g      graph.Graph
	slot   graph.Slot
	logger *slog.Logger
	pss    *util.PendingSubscriptionStatus
	// sawAny is set once any real account (beyond the initial program-wide
	// subscribe ack) has actually been processed. Guards against Check()
	// reporting count==0 just because discovery hasn't started yet.
	sawAny             bool
	maxSubscriberCount int
	totalCount         int
	counter            [3]int
	db                 *sql.DB
	tx                 *sql.Tx
	mintTracker        *mintinfo.Tracker
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, entry *slog.Logger, maxSubscriberCount int, db *sql.DB, mintTracker *mintinfo.Tracker) *eventHandler {
	eh := new(eventHandler)
	entry = entry.With("handler", "clmm")
	eh.ctx = logger.ToContext(ctx, entry)
	eh.cancel = cancel
	eh.logger = entry
	eh.slot = 0
	eh.maxSubscriberCount = maxSubscriberCount
	eh.db = db
	eh.mintTracker = mintTracker
	return eh
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

// isConfigFresh reports whether this config was already fetched within the
// last SevenDaysOfSlots, per raydium_clmm_config.last_slot.
func (handler *eventHandler) isConfigFresh(pubkey sgo.PublicKey) bool {
	if handler.db == nil {
		return false
	}
	var lastSlot uint64
	err := handler.dbQuery()(`SELECT last_slot FROM raydium_clmm_config WHERE pubkey = ?`, pubkey[:]).Scan(&lastSlot)
	if err != nil {
		return false
	}
	return lastSlot+SevenDaysOfSlots > uint64(handler.slot)
}

// configExists reports whether pubkey has a raydium_clmm_config row at all
// (regardless of freshness) -- the same sanity check the old mAmmConfig
// presence-check provided: a pool referencing an unknown config is a
// genuine anomaly worth logging, not just a stale-cache miss.
func (handler *eventHandler) configExists(pubkey sgo.PublicKey) bool {
	if handler.db == nil {
		return false
	}
	var one int
	return handler.dbQuery()(`SELECT 1 FROM raydium_clmm_config WHERE pubkey = ?`, pubkey[:]).Scan(&one) == nil
}

// isPoolFresh reports whether this pool was already fetched within the
// last SevenDaysOfSlots, per raydium_clmm_pool.last_slot.
func (handler *eventHandler) isPoolFresh(pubkey sgo.PublicKey) bool {
	if handler.db == nil {
		return false
	}
	var lastSlot uint64
	err := handler.dbQuery()(`SELECT last_slot FROM raydium_clmm_pool WHERE pubkey = ?`, pubkey[:]).Scan(&lastSlot)
	if err != nil {
		return false
	}
	return lastSlot+SevenDaysOfSlots > uint64(handler.slot)
}

// findPoolByVault returns the pool owning a token_vault0/token_vault1 and
// which side it is, by querying the row that already references it --
// replaces the old in-memory mVault reverse-lookup.
func (handler *eventHandler) findPoolByVault(vault sgo.PublicKey) (pool sgo.PublicKey, isVault0 bool, found bool) {
	var pubkey, vault0 []byte
	err := handler.dbQuery()(
		`SELECT pubkey, token_vault0 FROM raydium_clmm_pool WHERE token_vault0 = ?1 OR token_vault1 = ?1`,
		vault[:],
	).Scan(&pubkey, &vault0)
	if err != nil {
		return sgo.PublicKey{}, false, false
	}
	return sgo.PublicKeyFromBytes(pubkey), sgo.PublicKeyFromBytes(vault0).Equals(vault), true
}

func (handler *eventHandler) CommitStart(slot graph.Slot) {
	handler.slot = slot
	if handler.db == nil {
		return
	}
	tx, err := handler.db.Begin()
	if err != nil {
		handler.cancel(fmt.Errorf("clmm CommitStart: %w", err))
		return
	}
	handler.tx = tx
}

func (handler *eventHandler) CommitFinish() bool {
	if handler.tx == nil {
		return false
	}
	if err := handler.tx.Commit(); err != nil {
		handler.cancel(fmt.Errorf("clmm CommitFinish: %w", err))
		return false
	}
	handler.tx = nil

	if handler.pss != nil {
		// Check() must always run (it's what actually sends queued
		// subscribes and drains acks) — sawAny only gates whether a
		// count==0 result is trusted as "actually done" vs. "hasn't
		// started discovering anything yet".
		done := handler.pss.Check(handler.slot)
		if done && handler.sawAny {
			handler.logger.Info("clmm fetch complete")
			return true
		} else {
			p := handler.pss.Count()
			handler.logger.Info(fmt.Sprintf("handler.slot %d; count %d inFlight %d queued %d; pending %d", handler.slot, p[0], p[1], p[2], len(handler.pss.PendingRoots())))
		}
	}
	return false
}

func (handler *eventHandler) Ctx() context.Context {
	return handler.ctx
}

func (handler *eventHandler) Init(g graph.Graph) error {
	handler.g = g
	handler.pss = util.CreatePendingSubscriptionList(handler.ctx, g, 20, 10)
	handler.pss.Subscribe(ProgramID, graph.WeightAll, 2)
	return nil
}

func (handler *eventHandler) OnSlot(slot graph.Slot, status graph.SlotStatus) {
	_ = slot
	_ = status
}

// SevenDaysOfSlots assumes ~400ms/slot (2.5 slots/sec), the same fallback
// bot/state/clock.go itself uses before real slot-timing data arrives --
// see OldSlot in raydium/cpmm for the established precedent of this
// convention.
const SevenDaysOfSlots = 7 * 24 * 60 * 60 * 25 / 10 // 1,512,000 slots

func (handler *eventHandler) OnAccount(a graph.Account, isNew bool) {
	header := a.Header()
	if graph.AccountIsDeleted(a) {
		return
	}
	if header.DataSize < 8 {
		return
	}
	var ammConfig *AmmConfig
	var poolState *PoolState
	var tokenAccount *sgotkn.Account
	handler.totalCount++
	var err error
	if ProgramID.Equals(header.Owner) {
		switch header.DataSize - 8 {
		case AmmConfigBodySize:
			handler.counter[0]++
			d := a.Data()
			ammConfig, err = ParseAmmConfig(header.Pubkey, d[8:])
		case PoolStateBodySize:
			handler.counter[1]++
			d := a.Data()
			poolState, err = ParsePoolState(header.Pubkey, d[8:])
		default:
		}
	} else if sgotkn.ProgramID.Equals(header.Owner) {
		handler.counter[2]++
		tokenAccount = new(sgotkn.Account)
		err = bin.UnmarshalBorsh(tokenAccount, a.Data())
		if err != nil {
			err = nil
			tokenAccount = nil
		}
	}
	if err != nil {
		handler.logger.With("err", err).Error("exiting handler")
		handler.cancel(fmt.Errorf("failed OnAccount: %s", err))
		return
	}
	if ammConfig != nil || poolState != nil || tokenAccount != nil {
		handler.sawAny = true
	}
	if handler.totalCount%2_000 == 0 {
		handler.logger.Info(fmt.Sprintf("count %+v", handler.counter))
	}

	if ammConfig != nil {
		if !handler.isConfigFresh(header.Pubkey) {
			handler.pss.Subscribe(header.Pubkey, graph.WeightDirect, 2)
		}
		if handler.tx != nil {
			c := ammConfig
			if _, err2 := handler.tx.Exec(`
				INSERT OR REPLACE INTO raydium_clmm_config
				(pubkey, bump, config_index, owner, protocol_fee_rate,
				 trade_fee_rate, tick_spacing, fund_fee_rate, fund_owner, last_slot)
				VALUES (?,?,?,?,?,?,?,?,?,?)`,
				header.Pubkey[:],
				int(c.Bump), int(c.Index), c.Owner[:],
				int64(c.ProtocolFeeRate), int64(c.TradeFeeRate),
				int(c.TickSpacing), int64(c.FundFeeRate),
				c.FundOwner[:], handler.slot,
			); err2 != nil {
				handler.cancel(fmt.Errorf("clmm insert config: %w", err2))
				return
			}
		}
	}

	if poolState != nil {
		if !handler.configExists(poolState.AmmConfig) {
			handler.logger.Error(fmt.Sprintf("missing config for amm %s for pool %s", poolState.AmmConfig, header.Pubkey))
		} else {
			if !handler.isPoolFresh(header.Pubkey) {
				for _, t := range []sgo.PublicKey{poolState.TokenVault0, poolState.TokenVault1} {
					handler.pss.Subscribe(t, 0, 1)
				}
			}
			if handler.tx != nil {
				p := poolState
				if _, err2 := handler.tx.Exec(`
					INSERT OR REPLACE INTO raydium_clmm_pool
					(pubkey, amm_config, owner, token_mint0, token_mint1,
					 token_vault0, token_vault1, observation_key,
					 mint_decimals0, mint_decimals1, tick_spacing,
					 liquidity_lo, liquidity_hi, sqrt_price_lo, sqrt_price_hi,
					 tick_current, protocol_fees0, protocol_fees1,
					 status, open_time, last_slot)
					VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
					header.Pubkey[:], p.AmmConfig[:], p.Owner[:],
					p.TokenMint0[:], p.TokenMint1[:],
					p.TokenVault0[:], p.TokenVault1[:],
					p.ObservationKey[:],
					int(p.MintDecimals0), int(p.MintDecimals1), int(p.TickSpacing),
					int64(p.Liquidity.Lo), int64(p.Liquidity.Hi),
					int64(p.SqrtPriceX64.Lo), int64(p.SqrtPriceX64.Hi),
					int64(p.TickCurrent),
					int64(p.ProtocolFeesToken0), int64(p.ProtocolFeesToken1),
					int(p.Status), int64(p.OpenTime), handler.slot,
				); err2 != nil {
					handler.cancel(fmt.Errorf("clmm insert pool: %w", err2))
					return
				}
				// CLMM already has decimals for free from the pool account
				// itself -- save them opportunistically so AMM/CPMM/Orca can
				// skip a redundant Mint-account fetch for a shared mint.
				// Uses SaveTx (not Save) because we're inside the open
				// per-slot transaction: store.DB has a single-connection
				// pool, so a plain db.Exec here would deadlock waiting for
				// a second connection this same goroutine already holds.
				if err2 := handler.mintTracker.SaveTx(handler.tx, p.TokenMint0, p.MintDecimals0); err2 != nil {
					handler.logger.Warn("clmm mint save failed", "pubkey", p.TokenMint0, "err", err2)
				}
				if err2 := handler.mintTracker.SaveTx(handler.tx, p.TokenMint1, p.MintDecimals1); err2 != nil {
					handler.logger.Warn("clmm mint save failed", "pubkey", p.TokenMint1, "err", err2)
				}
			}
		}
	}

	if tokenAccount != nil {
		if pool, isVault0, found := handler.findPoolByVault(header.Pubkey); found && handler.tx != nil {
			col := "token1_balance"
			if isVault0 {
				col = "token0_balance"
			}
			if _, err2 := handler.tx.Exec(
				`UPDATE raydium_clmm_pool SET `+col+` = ?, last_slot = ? WHERE pubkey = ?`,
				int64(tokenAccount.Amount), handler.slot, pool[:],
			); err2 != nil {
				handler.cancel(fmt.Errorf("clmm update vault: %w", err2))
				return
			}
		}
	}
}
