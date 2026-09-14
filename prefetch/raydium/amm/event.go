package amm

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
var errFetchComplete = errors.New("amm: fetch complete")

// SevenDaysOfSlots assumes ~400ms/slot (2.5 slots/sec), the same fallback
// bot/state/clock.go itself uses before real slot-timing data arrives --
// see OldSlot in raydium/cpmm for the established precedent of this
// convention.
const SevenDaysOfSlots = 7 * 24 * 60 * 60 * 25 / 10 // 1,512,000 slots

type eventHandler struct {
	onAccountCount int
	ctx            context.Context
	cancel         context.CancelCauseFunc
	g              graph.Graph
	slot           graph.Slot
	logger         *slog.Logger
	pss            *util.PendingSubscriptionStatus
	// sawAny is set once any real account (beyond the initial program-wide
	// subscribe ack) has actually been processed. Guards against Check()
	// reporting count==0 just because discovery hasn't started yet.
	sawAny               bool
	maxSubscriptionCount int
	// mPendingToken holds a vault's parsed token-account body when it
	// arrives *before* its owning pool has been discovered/inserted --
	// same-batch stream reordering, not a dedup/routing cache, so unlike
	// mPool/mTokenPool/mMarket this one can't be replaced by a DB lookup
	// (the pool row genuinely doesn't exist yet at that point either way).
	// map token owner -> token id -> token account
	mPendingToken map[sgo.PublicKey]*sgotkn.Account
	db            *sql.DB
	tx            *sql.Tx
	mintTracker   *mintinfo.Tracker
}

func createHandler(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	entry *slog.Logger,
	maxSubsriptionCount int,
	db *sql.DB,
	mintTracker *mintinfo.Tracker,
) *eventHandler {
	eh := new(eventHandler)
	entry = entry.With("handler", "amm", "program", ProgramID)
	eh.ctx = logger.ToContext(ctx, entry)
	eh.cancel = cancel
	eh.logger = entry
	eh.slot = 0
	eh.mPendingToken = make(map[sgo.PublicKey]*sgotkn.Account)
	eh.maxSubscriptionCount = maxSubsriptionCount
	eh.db = db
	eh.mintTracker = mintTracker
	return eh
}

func (handler *eventHandler) CommitStart(slot graph.Slot) {
	handler.slot = slot
	if handler.db == nil {
		return
	}
	tx, err := handler.db.Begin()
	if err != nil {
		handler.cancel(fmt.Errorf("amm CommitStart: %w", err))
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
		handler.cancel(fmt.Errorf("amm CommitFinish: %w", err))
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
			handler.logger.Info("amm fetch complete")
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
	// amm v4 has a far larger pool count than cpmm/clmm (hundreds of
	// thousands of pools, 2 vault subscribes each), so it needs much more
	// concurrency here than 10 to make real forward progress on balances.
	handler.pss = util.CreatePendingSubscriptionList(handler.ctx, g, 50, 10)
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

// isPoolFresh reports whether this pool was already fetched within the
// last SevenDaysOfSlots, per raydium_amm_pool.last_slot.
func (handler *eventHandler) isPoolFresh(pubkey sgo.PublicKey) bool {
	if handler.db == nil {
		return false
	}
	var lastSlot uint64
	err := handler.dbQuery()(`SELECT last_slot FROM raydium_amm_pool WHERE pubkey = ?`, pubkey[:]).Scan(&lastSlot)
	if err != nil {
		return false
	}
	return lastSlot+SevenDaysOfSlots > uint64(handler.slot)
}

// findPoolByVault returns the pool owning a coin/pc vault and which side it
// is, by querying the row that already references it -- replaces the old
// in-memory mTokenPool reverse-lookup. Only ever misses for a vault whose
// pool hasn't been inserted yet, which mPendingToken handles separately.
func (handler *eventHandler) findPoolByVault(vault sgo.PublicKey) (pool sgo.PublicKey, isCoin bool, found bool) {
	var pubkey, coinVault []byte
	err := handler.dbQuery()(
		`SELECT pubkey, coin_vault FROM raydium_amm_pool WHERE coin_vault = ?1 OR pc_vault = ?1`,
		vault[:],
	).Scan(&pubkey, &coinVault)
	if err != nil {
		return sgo.PublicKey{}, false, false
	}
	return sgo.PublicKeyFromBytes(pubkey), sgo.PublicKeyFromBytes(coinVault).Equals(vault), true
}

// findPoolByMarket returns the pool owning an OpenBook/Serum market
// account, by querying the row that already references it -- replaces the
// old in-memory mMarket reverse-lookup.
func (handler *eventHandler) findPoolByMarket(market sgo.PublicKey) (pool sgo.PublicKey, found bool) {
	var pubkey []byte
	err := handler.dbQuery()(`SELECT pubkey FROM raydium_amm_pool WHERE market = ?`, market[:]).Scan(&pubkey)
	if err != nil {
		return sgo.PublicKey{}, false
	}
	return sgo.PublicKeyFromBytes(pubkey), true
}

func (handler *eventHandler) tokenOnAccount(a graph.Account, isNew bool) {
	_ = isNew
	header := a.Header()
	body := a.Data()
	if 165 <= len(body) && len(body) < 200 {
		b := new(sgotkn.Account)
		err := bin.NewBorshDecoder(body).Decode(b)
		if err != nil {
			handler.cancel(fmt.Errorf("failed to parse token account: %s", err))
			return
		}
		handler.sawAny = true
		// update coin_balance or pc_balance
		if pool, isCoin, found := handler.findPoolByVault(header.Pubkey); found {
			handler.saveToken(header.Pubkey, b, pool, isCoin)
		} else {
			handler.mPendingToken[header.Pubkey] = b
		}
	} else if len(body) == mintinfo.MintAccountSize {
		if handler.tx == nil {
			return
		}
		decimals, err := mintinfo.DecodeDecimals(body)
		if err != nil {
			handler.logger.Warn("amm mint decode failed", "pubkey", header.Pubkey, "err", err)
			return
		}
		if err = handler.mintTracker.SaveTx(handler.tx, header.Pubkey, decimals); err != nil {
			handler.logger.Warn("amm mint save failed", "pubkey", header.Pubkey, "err", err)
		}
	}
}

// marketOnAccount handles an OpenBook/Serum market account belonging to a
// tracked AMM pool: it parses the bids/asks/event-queue/vault accounts,
// derives the vault-signer PDA, and persists them all onto the pool's row.
func (handler *eventHandler) marketOnAccount(header graph.AccountHeader, body []byte, pool sgo.PublicKey) {
	market, err := ParseOpenBookMarket(header.Pubkey, body)
	if err != nil {
		handler.logger.Warn("openbook market parse failed", "pubkey", header.Pubkey, "err", err)
		return
	}
	handler.sawAny = true
	vaultSigner, err := DeriveVaultSigner(header.Pubkey, market.VaultSignerNonce, header.Owner)
	if err != nil {
		handler.logger.Warn("openbook vault signer derivation failed", "pubkey", header.Pubkey, "err", err)
		return
	}
	if handler.tx == nil {
		return
	}
	if _, err2 := handler.tx.Exec(`
		UPDATE raydium_amm_pool
		SET market_bids = ?, market_asks = ?, market_event_queue = ?,
		    market_coin_vault = ?, market_pc_vault = ?, market_vault_signer = ?, last_slot = ?
		WHERE pubkey = ?`,
		market.Bids[:], market.Asks[:], market.EventQueue[:],
		market.CoinVault[:], market.PcVault[:], vaultSigner[:], handler.slot,
		pool[:],
	); err2 != nil {
		handler.cancel(fmt.Errorf("amm update market: %w", err2))
		return
	}
}

// if isCoin is false, then this is a PcVault
func (handler *eventHandler) saveToken(pubkey sgo.PublicKey, account *sgotkn.Account, pool sgo.PublicKey, isCoin bool) {
	_ = pubkey
	if handler.tx == nil {
		return
	}
	balCol, mintCol := "pc_balance", "pc_mint"
	if isCoin {
		balCol, mintCol = "coin_balance", "coin_mint"
	}
	if _, err := handler.tx.Exec(
		`UPDATE raydium_amm_pool SET `+balCol+` = ?, `+mintCol+` = ?, last_slot = ? WHERE pubkey = ?`,
		int64(account.Amount), account.Mint[:], handler.slot, pool[:],
	); err != nil {
		handler.cancel(fmt.Errorf("amm update vault: %w", err))
		return
	}
	if !handler.mintTracker.Known(account.Mint) && handler.mintTracker.MarkPending(account.Mint) {
		handler.pss.Subscribe(account.Mint, 0, 1)
	}
}

func (handler *eventHandler) OnAccount(a graph.Account, isNew bool) {
	handler.onAccountCount++
	header := a.Header()
	if graph.AccountIsDeleted(a) {
		// Nothing to clean up in memory anymore -- the DB row (if any)
		// stays as the last-known state, same as every other deleted
		// account this codebase tracks.
		return
	}
	if sgotkn.ProgramID.Equals(header.Owner) {
		handler.tokenOnAccount(a, isNew)
		return
	}
	if ProgramID.Equals(header.Pubkey) {
		return
	}
	if pool, found := handler.findPoolByMarket(header.Pubkey); found {
		handler.marketOnAccount(header, a.Data(), pool)
		return
	}
	if !ProgramID.Equals(header.Owner) {
		handler.logger.Debug(fmt.Sprintf("program wrong: %d vs %d", ProgramID, header.Owner))
		return
	}
	data := a.Data()
	if len(data) != Size {
		return
	}
	info, err := Parse(header.Pubkey, data)
	if err != nil {
		handler.logger.Warn("raydium amm parse failed", "pubkey", header.Pubkey, "err", err)
		return
	}
	handler.sawAny = true
	if handler.isPoolFresh(header.Pubkey) {
		return
	}
	ammPubkey := header.Pubkey
	handler.pss.Subscribe(info.CoinVault, 0, 1)
	handler.pss.Subscribe(info.PcVault, 0, 1)
	handler.pss.Subscribe(info.Market, 0, 1)
	if handler.tx == nil {
		handler.logger.Error("missing tx handler")
		handler.cancel(errors.New("missing tx handler"))
		return
	} else {
		if _, err2 := handler.tx.Exec(`
			INSERT OR REPLACE INTO raydium_amm_pool
			(pubkey, coin_vault, pc_vault, coin_balance, pc_balance, market, last_slot)
			VALUES (?,?,?,NULL,NULL,?,?)`,
			header.Pubkey[:], info.CoinVault[:], info.PcVault[:], info.Market[:], handler.slot,
		); err2 != nil {
			handler.cancel(fmt.Errorf("amm insert pool: %w", err2))
			return
		}
	}
	t1, present := handler.mPendingToken[info.CoinVault]
	delete(handler.mPendingToken, info.CoinVault)
	if present {
		handler.saveToken(info.CoinVault, t1, ammPubkey, true)
	}
	t2, present := handler.mPendingToken[info.PcVault]
	delete(handler.mPendingToken, info.PcVault)
	if present {
		handler.saveToken(info.PcVault, t2, ammPubkey, false)
	}
}
