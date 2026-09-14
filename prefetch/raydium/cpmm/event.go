package cpmm

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
var errFetchComplete = errors.New("cpmm: fetch complete")

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
	oldPoolCount       int
	maxSubscriberCount int
	totalCount         int
	counter            [3]int
	db                 *sql.DB
	tx                 *sql.Tx
	stmtConfig         *sql.Stmt
	stmtPool           *sql.Stmt
	stmtVault0         *sql.Stmt
	stmtVault1         *sql.Stmt
	mintTracker        *mintinfo.Tracker
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, entry *slog.Logger, maxSubscriberCount int, db *sql.DB, mintTracker *mintinfo.Tracker) *eventHandler {
	eh := new(eventHandler)
	entry = entry.With("handler", "cpmm")
	eh.ctx = logger.ToContext(ctx, entry)
	eh.cancel = cancel
	eh.logger = entry
	eh.slot = 0
	eh.maxSubscriberCount = maxSubscriberCount
	eh.db = db
	eh.mintTracker = mintTracker
	if db != nil {
		var err error
		eh.stmtConfig, err = db.Prepare(`
			INSERT OR REPLACE INTO raydium_cpmm_config
			(pubkey, bump, disable_create_pool, config_index, trade_fee_rate,
			 protocol_fee_rate, fund_fee_rate, create_pool_fee,
			 protocol_owner, fund_owner, creator_fee_rate, last_slot)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			cancel(fmt.Errorf("cpmm prepare config: %w", err))
			return eh
		}
		eh.stmtPool, err = db.Prepare(`
			INSERT OR REPLACE INTO raydium_cpmm_pool
			(pubkey, amm_config, token0_mint, token1_mint,
			 token0_vault, token1_vault, lp_mint,
			 token0_program, token1_program, observation_key,
			 lp_supply, open_time, last_slot)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			cancel(fmt.Errorf("cpmm prepare pool: %w", err))
			return eh
		}
		eh.stmtVault0, err = db.Prepare(
			`UPDATE raydium_cpmm_pool SET token0_balance = ?, last_slot = ? WHERE pubkey = ?`)
		if err != nil {
			cancel(fmt.Errorf("cpmm prepare vault0: %w", err))
			return eh
		}
		eh.stmtVault1, err = db.Prepare(
			`UPDATE raydium_cpmm_pool SET token1_balance = ?, last_slot = ? WHERE pubkey = ?`)
		if err != nil {
			cancel(fmt.Errorf("cpmm prepare vault1: %w", err))
			return eh
		}
	}
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

// vaultBalancesFromDB returns the cached vault balances for a pool if both are
// already stored in the database, allowing the subscription to be skipped.
func (handler *eventHandler) vaultBalancesFromDB(pool sgo.PublicKey) (bal0, bal1 uint64, ok bool) {
	if handler.db == nil {
		return 0, 0, false
	}
	var b0, b1 sql.NullInt64
	err := handler.dbQuery()(
		`SELECT token0_balance, token1_balance FROM raydium_cpmm_pool WHERE pubkey = ?`,
		pool[:],
	).Scan(&b0, &b1)
	if err != nil || !b0.Valid || !b1.Valid {
		return 0, 0, false
	}
	return uint64(b0.Int64), uint64(b1.Int64), true
}

// isConfigFresh reports whether this config was already fetched within the
// last SevenDaysOfSlots, per raydium_cpmm_config.last_slot.
func (handler *eventHandler) isConfigFresh(pubkey sgo.PublicKey) bool {
	if handler.db == nil {
		return false
	}
	var lastSlot uint64
	err := handler.dbQuery()(`SELECT last_slot FROM raydium_cpmm_config WHERE pubkey = ?`, pubkey[:]).Scan(&lastSlot)
	if err != nil {
		return false
	}
	return lastSlot+SevenDaysOfSlots > uint64(handler.slot)
}

// configExists reports whether pubkey has a raydium_cpmm_config row at all
// (regardless of freshness) -- used as the same sanity check the old
// mAmmConfig presence-check provided: a pool referencing an unknown config
// is a genuine anomaly worth logging, not just a stale-cache miss.
func (handler *eventHandler) configExists(pubkey sgo.PublicKey) bool {
	if handler.db == nil {
		return false
	}
	var one int
	return handler.dbQuery()(`SELECT 1 FROM raydium_cpmm_config WHERE pubkey = ?`, pubkey[:]).Scan(&one) == nil
}

// isPoolFresh reports whether this pool was already fetched within the
// last SevenDaysOfSlots, per raydium_cpmm_pool.last_slot.
func (handler *eventHandler) isPoolFresh(pubkey sgo.PublicKey) bool {
	if handler.db == nil {
		return false
	}
	var lastSlot uint64
	err := handler.dbQuery()(`SELECT last_slot FROM raydium_cpmm_pool WHERE pubkey = ?`, pubkey[:]).Scan(&lastSlot)
	if err != nil {
		return false
	}
	return lastSlot+SevenDaysOfSlots > uint64(handler.slot)
}

// findPoolByVault returns the pool owning a token0/token1 vault and which
// side it is, by querying the row that already references it -- replaces
// the old in-memory mVault reverse-lookup.
func (handler *eventHandler) findPoolByVault(vault sgo.PublicKey) (pool sgo.PublicKey, isToken0 bool, found bool) {
	var pubkey, token0Vault []byte
	err := handler.dbQuery()(
		`SELECT pubkey, token0_vault FROM raydium_cpmm_pool WHERE token0_vault = ?1 OR token1_vault = ?1`,
		vault[:],
	).Scan(&pubkey, &token0Vault)
	if err != nil {
		return sgo.PublicKey{}, false, false
	}
	return sgo.PublicKeyFromBytes(pubkey), sgo.PublicKeyFromBytes(token0Vault).Equals(vault), true
}

func (handler *eventHandler) CommitStart(slot graph.Slot) {
	handler.slot = slot
	if handler.db == nil {
		return
	}
	tx, err := handler.db.Begin()
	if err != nil {
		handler.cancel(fmt.Errorf("cpmm CommitStart: %w", err))
		return
	}
	handler.tx = tx
}

func (handler *eventHandler) CommitFinish() bool {
	if handler.tx == nil {
		return false
	}
	if err := handler.tx.Commit(); err != nil {
		handler.cancel(fmt.Errorf("cpmm CommitFinish: %w", err))
	}
	handler.tx = nil

	if handler.pss != nil {
		// Check() must always run (it's what actually sends queued
		// subscribes and drains acks) — sawAny only gates whether a
		// count==0 result is trusted as "actually done" vs. "hasn't
		// started discovering anything yet".
		done := handler.pss.Check(handler.slot)
		if done && handler.sawAny {
			handler.logger.Info("cpmm fetch complete")
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
	// quietSlots raised from 10 to 60 -- with cpmm's own pool backlog, a
	// 10-slot lull between discovery bursts was being mistaken for "done"
	// (Check() reporting the queue empty mid-lull, well before the real
	// backfill finished), truncating results the same way it did for amm's
	// much larger backlog. See the comment on amm's Init for the full
	// mechanism.
	handler.pss = util.CreatePendingSubscriptionList(handler.ctx, g, 50, 60)
	handler.pss.Subscribe(ProgramID, graph.WeightAll, 2)
	return nil
}

// closeStatements closes the prepared statements. Only call once the Hook()
// event loop has fully exited — it shares these statements with OnAccount,
// which keeps running (on a different goroutine) until the loop stops.
func (handler *eventHandler) closeStatements() error {
	for _, s := range []*sql.Stmt{handler.stmtConfig, handler.stmtPool, handler.stmtVault0, handler.stmtVault1} {
		if s == nil {
			continue
		}
		if err := s.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (handler *eventHandler) OnSlot(slot graph.Slot, status graph.SlotStatus) {
	_ = slot
	_ = status
}

// SevenDaysOfSlots assumes ~400ms/slot (2.5 slots/sec), the same fallback
// bot/state/clock.go itself uses before real slot-timing data arrives.
const SevenDaysOfSlots = 7 * 24 * 60 * 60 * 25 / 10 // 1,512,000 slots

// 8 hours
const OldSlot = 8 * 60 * 60 * 25 / 10

func (handler *eventHandler) OnAccount(a graph.Account, isNew bool) {
	// i
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
	} else if sgotkn.ProgramID.Equals(header.Owner) && header.DataSize == mintinfo.MintAccountSize {
		if handler.tx != nil {
			decimals, derr := mintinfo.DecodeDecimals(a.Data())
			if derr != nil {
				handler.logger.Warn("cpmm mint decode failed", "pubkey", header.Pubkey, "err", derr)
			} else if serr := handler.mintTracker.SaveTx(handler.tx, header.Pubkey, decimals); serr != nil {
				handler.logger.Warn("cpmm mint save failed", "pubkey", header.Pubkey, "err", serr)
			}
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
	if handler.totalCount%1_000 == 0 {
		handler.logger.Info(fmt.Sprintf("count %+v", handler.counter))
	}

	if ammConfig != nil {
		if !handler.isConfigFresh(header.Pubkey) {
			handler.logger.Info(fmt.Sprintf("ammConfig - send subscription %s", header.Pubkey))
			handler.pss.Subscribe(header.Pubkey, graph.WeightDirect, 2)
		}
		if handler.tx != nil {
			c := ammConfig
			if _, err2 := handler.tx.Stmt(handler.stmtConfig).Exec(
				header.Pubkey[:],
				int(c.Bump), boolToInt(c.DisableCreatePool), int(c.Index),
				int64(c.TradeFeeRate), int64(c.ProtocolFeeRate),
				int64(c.FundFeeRate), int64(c.CreatePoolFee),
				c.ProtocolOwner[:], c.FundOwner[:],
				int64(c.CreatorFeeRate), handler.slot,
			); err2 != nil {
				handler.cancel(fmt.Errorf("cpmm insert config: %w", err2))
				return
			}
		}
	}

	// only handle pools that are new
	if poolState != nil && handler.slot > header.Slot+OldSlot {
		if handler.oldPoolCount%1_000 == 0 {
			handler.logger.Info(fmt.Sprintf("bad pool; too old; pool %s; slot %d; amm config %s", header.Pubkey, header.Slot, poolState.AmmConfig))
		}
		handler.oldPoolCount++
	} else if poolState != nil {
		handler.logger.Info(fmt.Sprintf("processing pool %s; ammconfig %s", header.Pubkey, poolState.AmmConfig))
		if !handler.configExists(poolState.AmmConfig) {
			handler.logger.Error(fmt.Sprintf("missing config for amm %s for pool %s", poolState.AmmConfig, header.Pubkey))
		} else {
			if !handler.isPoolFresh(header.Pubkey) {
				if bal0, bal1, ok := handler.vaultBalancesFromDB(header.Pubkey); !ok {
					_ = bal0
					_ = bal1
					handler.pss.Subscribe(poolState.Token0Vault, 0, 1)
					handler.pss.Subscribe(poolState.Token1Vault, 0, 1)
				}
				for _, m := range []sgo.PublicKey{poolState.Token0Mint, poolState.Token1Mint} {
					if !handler.mintTracker.Known(m) && handler.mintTracker.MarkPending(m) {
						handler.pss.Subscribe(m, 0, 1)
					}
				}
			}
			if handler.tx != nil {
				p := poolState
				if _, err2 := handler.tx.Stmt(handler.stmtPool).Exec(
					header.Pubkey[:], p.AmmConfig[:],
					p.Token0Mint[:], p.Token1Mint[:],
					p.Token0Vault[:], p.Token1Vault[:],
					p.LpMint[:],
					p.Token0Program[:], p.Token1Program[:],
					p.ObservationKey[:],
					int64(p.LpSupply), int64(p.OpenTime), handler.slot,
				); err2 != nil {
					err2 = fmt.Errorf("cpmm insert pool: %w", err2)
					handler.logger.Info(err2.Error())
					handler.cancel(err2)
					return
				}
			}
		}
		handler.logger.Info(fmt.Sprintf("processed pool %s; ammconfig %s", header.Pubkey, poolState.AmmConfig))
	}

	if tokenAccount != nil {
		if pool, isToken0, found := handler.findPoolByVault(header.Pubkey); found && handler.tx != nil {
			stmt := handler.stmtVault1
			if isToken0 {
				stmt = handler.stmtVault0
			}
			if _, err2 := handler.tx.Stmt(stmt).Exec(
				int64(tokenAccount.Amount), handler.slot, pool[:],
			); err2 != nil {
				handler.cancel(fmt.Errorf("cpmm update vault: %w", err2))
				return
			}
		}
	}
}
