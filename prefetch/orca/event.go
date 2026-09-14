package orca

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	bin "github.com/gagliardetto/binary"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

// SevenDaysOfSlots assumes ~400ms/slot (2.5 slots/sec), the same fallback
// bot/state/clock.go itself uses before real slot-timing data arrives --
// see raydium/cpmm's own OldSlot for the established precedent of this
// convention.
const SevenDaysOfSlots = 7 * 24 * 60 * 60 * 25 / 10 // 1,512,000 slots

// fetchWhirlpool discovers WhirlpoolConfigs and Whirlpools via an active,
// per-node Subscribe/ack chain (program -> config, then config -> pool) --
// see marginfi/event.go's fetch for the same pattern applied to
// marginfi-v2. Configs/pools already fetched within the last
// SevenDaysOfSlots are skipped (see eventHandler.isConfigFresh/isPoolFresh),
// unless force is true, which walks every config and pool regardless of
// how recently it was last seen -- prefetch.db itself is the dedup/
// staleness cache now, not an in-memory map, so persistence happens
// immediately per-account (handler.tx) rather than being collected and
// handed back over a channel at the end.
func (orca *Orca) fetchWhirlpool(parentCtx context.Context, stateClient state.Client, logger *slog.Logger, maxSubscriptionCount int, db *sql.DB, mintTracker *mintinfo.Tracker, force bool) error {
	ctx, cancel := context.WithCancelCause(parentCtx)
	doneSignalC := make(chan struct{}, 1)
	err := stateClient.Hook(createHandler(ctx, cancel, doneSignalC, logger, maxSubscriptionCount, db, mintTracker, force))
	cancel(errors.New("complete"))
	if err != nil {
		return fmt.Errorf("hook failed: %s", err)
	}
	orca.Pools, orca.mPool, err = loadPools(db)
	if err != nil {
		return fmt.Errorf("failed to load orca data after fetch: %s", err)
	}
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
	db                   *sql.DB
	mRoot                map[sgo.PublicKey]struct{}
	tx                   *sql.Tx
	wg                   *sync.WaitGroup
	sendSubCount         uint32
	pss                  *util.PendingSubscriptionStatus
	finished             bool
	mintTracker          *mintinfo.Tracker
	// mPendingToken holds a vault's parsed token-account body when it
	// arrives *before* its owning Whirlpool has been discovered/inserted --
	// same-batch stream reordering, not a dedup/routing cache (see
	// findPoolByVault, which is the actual reverse lookup once the pool
	// row exists) -- mirrors raydium/amm/event.go's field of the same name.
	mPendingToken map[sgo.PublicKey]*sgotkn.Account
	// force, when true, bypasses isConfigFresh/isPoolFresh so a resync
	// actually re-walks every config and pool instead of skipping
	// everything already inside the SevenDaysOfSlots freshness window.
	force bool
}

func createHandler(ctx context.Context, cancel context.CancelCauseFunc, doneSignalC chan<- struct{}, entry *slog.Logger, maxSubsriptionCount int, db *sql.DB, mintTracker *mintinfo.Tracker, force bool) graph.Hook {
	eh := new(eventHandler)
	entry = entry.With("handler", "orca", "program", ProgramID)
	eh.ctx = logger.ToContext(ctx, entry)
	eh.ctx = ctx
	eh.cancel = cancel
	eh.doneSignalC = doneSignalC
	eh.logger = entry
	eh.slot = 0
	eh.mRoot = make(map[sgo.PublicKey]struct{}, 10*1_024)
	eh.maxSubscriptionCount = maxSubsriptionCount
	eh.wg = &sync.WaitGroup{}
	eh.sendSubCount = 0
	eh.db = db
	eh.mintTracker = mintTracker
	eh.force = force
	eh.mPendingToken = make(map[sgo.PublicKey]*sgotkn.Account)
	return eh
}

func (handler *eventHandler) CommitStart(slot graph.Slot) {
	handler.slot = slot
	if handler.db == nil {
		return
	}
	tx, err := handler.db.Begin()
	if err != nil {
		handler.cancel(fmt.Errorf("orca CommitStart: %w", err))
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
		handler.cancel(fmt.Errorf("orca CommitFinish: %w", err))
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
		handler.logger.Info(fmt.Sprintf("orca commit finish: count %d; inFlight %d; queued %d", x[0], x[1], x[2]))
	}
	return false
}

func (handler *eventHandler) Ctx() context.Context {
	return handler.ctx
}

func (handler *eventHandler) Init(g graph.Graph) error {
	handler.g = g
	// fetch whirlpool configs and whirlpools
	handler.pss = util.CreatePendingSubscriptionList(handler.ctx, g, 50, 60)
	handler.logger.Info("init - 1")
	handler.sendSubCount++
	handler.mRoot[ProgramID] = struct{}{}
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

// isConfigFresh reports whether this WhirlpoolConfig's pools were already
// walked within the last SevenDaysOfSlots, per orca_whirlpool_config_seen.
// Always false under handler.force -- Create's force parameter is meant to
// trigger a full re-walk (e.g. to re-run every pool through a fixed
// sanitizeLiquidity), and that has to override this cache or a forced
// resync silently no-ops whenever the top-level configs are still fresh
// from a prior run, never even reaching individual pools.
func (handler *eventHandler) isConfigFresh(pubkey sgo.PublicKey) bool {
	if handler.force {
		return false
	}
	if handler.db == nil {
		return false
	}
	var lastSlot uint64
	err := handler.dbQuery()(`SELECT last_slot FROM orca_whirlpool_config_seen WHERE pubkey = ?`, pubkey[:]).Scan(&lastSlot)
	if err != nil {
		return false
	}
	return lastSlot+SevenDaysOfSlots > handler.slot
}

// isPoolFresh reports whether this Whirlpool was already fetched within
// the last SevenDaysOfSlots, per orca_whirlpool_pool.last_slot. Always
// false under handler.force -- see isConfigFresh's doc comment.
func (handler *eventHandler) isPoolFresh(pubkey sgo.PublicKey) bool {
	if handler.force {
		return false
	}
	if handler.db == nil {
		return false
	}
	var lastSlot uint64
	err := handler.dbQuery()(`SELECT last_slot FROM orca_whirlpool_pool WHERE pubkey = ?`, pubkey[:]).Scan(&lastSlot)
	if err != nil {
		return false
	}
	return lastSlot+SevenDaysOfSlots > handler.slot
}

// findPoolByVault returns the Whirlpool owning a vault_a/vault_b account
// and which side it is, by querying the row that already references it --
// same pattern as raydium/amm/event.go's findPoolByVault. Only ever misses
// for a vault whose pool hasn't been inserted yet, which mPendingToken
// handles separately.
func (handler *eventHandler) findPoolByVault(vault sgo.PublicKey) (pool sgo.PublicKey, isA bool, found bool) {
	var pubkey, vaultA []byte
	err := handler.dbQuery()(
		`SELECT pubkey, vault_a FROM orca_whirlpool_pool WHERE vault_a = ?1 OR vault_b = ?1`,
		vault[:],
	).Scan(&pubkey, &vaultA)
	if err != nil {
		return sgo.PublicKey{}, false, false
	}
	return sgo.PublicKeyFromBytes(pubkey), sgo.PublicKeyFromBytes(vaultA).Equals(vault), true
}

// saveToken persists a vault's real SPL token balance onto its owning
// Whirlpool row -- the actual current reserve, as opposed to the CLMM
// liquidity/sqrt_price-derived "virtual reserve" (see build.rs's
// max_liquidity_usd doc comment for why that virtual figure isn't a
// reliable stand-in for real economic size). if isA is false, vault is
// vault_b.
func (handler *eventHandler) saveToken(vault sgo.PublicKey, account *sgotkn.Account, pool sgo.PublicKey, isA bool) {
	_ = vault
	if handler.tx == nil {
		return
	}
	balCol := "vault_b_balance"
	if isA {
		balCol = "vault_a_balance"
	}
	if _, err := handler.tx.Exec(
		`UPDATE orca_whirlpool_pool SET `+balCol+` = ?, last_slot = ? WHERE pubkey = ?`,
		int64(account.Amount), handler.slot, pool[:],
	); err != nil {
		handler.cancel(fmt.Errorf("orca update vault: %w", err))
		return
	}
}

func (handler *eventHandler) OnAccount(a graph.Account, isNew bool) {
	header := a.Header()
	body := a.Data()
	if sgotkn.ProgramID.Equals(header.Owner) {
		if 165 <= len(body) && len(body) < 200 {
			b := new(sgotkn.Account)
			if err := bin.NewBorshDecoder(body).Decode(b); err != nil {
				handler.cancel(fmt.Errorf("orca: failed to parse token account: %s", err))
				return
			}
			if pool, isA, found := handler.findPoolByVault(header.Pubkey); found {
				handler.saveToken(header.Pubkey, b, pool, isA)
			} else {
				handler.mPendingToken[header.Pubkey] = b
			}
			return
		}
		if len(body) == mintinfo.MintAccountSize {
			if handler.tx == nil {
				return
			}
			decimals, err := mintinfo.DecodeDecimals(body)
			if err != nil {
				handler.logger.Warn(fmt.Sprintf("orca mint decode failed: pubkey %s: %s", header.Pubkey, err))
				return
			}
			if err = handler.mintTracker.SaveTx(handler.tx, header.Pubkey, decimals); err != nil {
				handler.logger.Warn(fmt.Sprintf("orca mint save failed: pubkey %s: %s", header.Pubkey, err))
			}
		}
		return
	}
	var discriminator [8]byte
	if len(body) < 8 {
		return
	}
	copy(discriminator[:], body[0:8])
	if !header.Owner.Equals(ProgramID) {
		return
	}
	var err error
	var c *WhirlpoolConfig
	var p *Whirlpool
	switch discriminator {
	case DiscriminatorWhirlpoolConfig:
		c, err = parseWhirlpoolConfig(header.Pubkey, body)
	case DiscriminatorWhirlpool:
		p, err = parseWhirlpool(header.Pubkey, body)
	case DiscriminatorTickArray:
	default:
	}
	if err != nil {
		handler.cancel(fmt.Errorf("parsing failed: %s", err))
		return
	}
	if c != nil {
		if handler.isConfigFresh(header.Pubkey) {
			return
		}
		if handler.tx == nil {
			handler.logger.Error("missing tx handler")
			handler.cancel(errors.New("missing tx handler"))
			return
		}
		if _, err = handler.tx.Exec(
			`INSERT OR REPLACE INTO orca_whirlpool_config_seen (pubkey, last_slot) VALUES (?, ?)`,
			c.Pubkey[:], handler.slot,
		); err != nil {
			handler.cancel(fmt.Errorf("orca insert config_seen: %w", err))
			return
		}
		handler.sendSubCount++
		_, present := handler.mRoot[header.Pubkey]
		if !present {
			handler.mRoot[header.Pubkey] = struct{}{}
			handler.pss.Subscribe(header.Pubkey, graph.WeightAll, 2)
		}

	}
	if p != nil {
		if handler.isPoolFresh(header.Pubkey) {
			return
		}
		if handler.tx == nil {
			handler.logger.Error("missing tx handler")
			handler.cancel(errors.New("missing tx handler"))
			return
		}
		if _, err = handler.tx.Exec(`
			INSERT OR REPLACE INTO orca_whirlpool_pool
			(pubkey, whirlpools_config, mint_a, mint_b, vault_a, vault_b,
			 sqrt_price_lo, sqrt_price_hi, liquidity_lo, liquidity_hi,
			 tick_current_index, tick_spacing, fee_rate, last_slot)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.Pubkey[:], p.WhirlpoolsConfig[:], p.TokenMintA[:], p.TokenMintB[:],
			p.VaultA[:], p.VaultB[:],
			int64(p.SqrtPriceLo), int64(p.SqrtPriceHi),
			int64(p.LiquidityLo), int64(p.LiquidityHi),
			p.TickCurrentIndex, p.TickSpacing, p.FeeRate, handler.slot,
		); err != nil {
			handler.cancel(fmt.Errorf("orca insert pool: %w", err))
			return
		}
		var present bool
		for _, mint := range []sgo.PublicKey{p.TokenMintA, p.TokenMintB} {
			if handler.mintTracker.Known(mint) || !handler.mintTracker.MarkPending(mint) {
				continue
			}
			_, present = handler.mRoot[mint]
			if !present {
				handler.mRoot[mint] = struct{}{}
				handler.pss.Subscribe(mint, graph.WeightAll, 1)
			}
		}
		// Subscribe to the real vault token accounts too -- see saveToken's
		// doc comment for why the raw liquidity/sqrt_price fields alone
		// aren't a reliable stand-in for actual reserves. Weight 0, same
		// convention as raydium/amm/event.go's vault subscribes: these are
		// balance-tracking only, not further graph exploration.
		for i, vault := range []sgo.PublicKey{p.VaultA, p.VaultB} {
			_, present = handler.mRoot[vault]
			if !present {
				handler.mRoot[vault] = struct{}{}
				handler.pss.Subscribe(vault, 0, 1)
			}
			t, pending := handler.mPendingToken[vault]
			if pending {
				delete(handler.mPendingToken, vault)
				handler.saveToken(vault, t, p.Pubkey, i == 0)
			}
		}
	}
}
