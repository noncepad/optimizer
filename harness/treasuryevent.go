package optimizer

import (
	"context"

	"git.noncepad.com/pkg/optimizer/util"
	solpipe_treasury "git.noncepad.com/pkg/safejar"
	solpipe_cba "git.noncepad.com/pkg/solpipe"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/token"
)

type treasuryHook struct {
	te *treasuryExternal
	// slot is set by CommitStart and read by CommitFinish -- CommitFinish
	// itself receives no slot argument (see state.Client.Hook's loop: it
	// calls `hook.CommitStart(slot)` then later `hook.CommitFinish()`
	// with no parameter), but te.g.Check needs one. Same pattern
	// util.FetchWalletBalance's own walletBalanceHandler already uses.
	slot graph.Slot
}
type localStore struct {
	// map token accounts to wallet account to track deleted accouts
	mAccountWallet map[sgo.PublicKey]sgo.PublicKey
	parent         *singleStore
	mChild         map[sgo.PublicKey]*singleStore
}

func createStore(parentPubkey sgo.PublicKey) *localStore {
	ls := new(localStore)
	ls.mAccountWallet = make(map[sgo.PublicKey]sgo.PublicKey)
	// Real bug (2026-09-15): this was never initialized, so the very
	// first real treasuryExternal.Child() call would panic with
	// "assignment to entry in nil map" the moment it tried
	// `mChild[id] = x`.
	ls.mChild = make(map[sgo.PublicKey]*singleStore)
	ls.parent = createSingleStore(sgo.SystemProgramID, parentPubkey)
	return ls
}

// storeByID resolves a wallet id (a child's own id, or `sgo.SystemProgramID`
// for the parent -- see createStore/createSingleStore) back to its
// singleStore. Returns nil if id isn't the parent's and isn't a known
// child -- callers treat that as "not one of ours."
func (ls *localStore) storeByID(id sgo.PublicKey) *singleStore {
	if ls.parent.id.Equals(id) {
		return ls.parent
	}
	return ls.mChild[id]
}

// storeByOwner resolves an SPL token account's owner (the wallet pubkey
// that actually holds the tokens) to its singleStore. Unlike storeByID,
// the key here is a wallet's own pubkey, not its id -- the same
// convention `mAccountWallet` already uses for `Child()`'s
// `mAccountWallet[x.pubkey] = id` entry, so this reuses that map rather
// than adding a second one.
func (ls *localStore) storeByOwner(owner sgo.PublicKey) *singleStore {
	if ls.parent.pubkey.Equals(owner) {
		return ls.parent
	}
	if id, present := ls.mAccountWallet[owner]; present {
		return ls.mChild[id]
	}
	return nil
}

type singleStore struct {
	id              sgo.PublicKey
	pubkey          sgo.PublicKey
	sol             uint64
	mTokenByAccount map[sgo.PublicKey]map[sgo.PublicKey]uint64
	mToken          map[sgo.PublicKey]uint64
	// positionCloser unwinds whatever DeFi positions (lending deposits,
	// perps, LP, etc.) this wallet holds -- see
	// budgetExternal.SetPositionCloser's doc comment. Lives on singleStore
	// (shared/cached via localStore.mChild), not on the ephemeral
	// budgetExternal wrapper Budget() hands back -- a second Budget(id)
	// call for the same id must still see a closer registered through an
	// earlier one.
	positionCloser func(context.Context) error
}

func createSingleStore(id sgo.PublicKey, pubkey sgo.PublicKey) *singleStore {
	return &singleStore{
		id:              id,
		pubkey:          pubkey,
		sol:             0,
		mTokenByAccount: make(map[sgo.PublicKey]map[sgo.PublicKey]uint64),
		mToken:          make(map[sgo.PublicKey]uint64),
	}
}

func (th *treasuryHook) CommitStart(slot graph.Slot) {
	th.te.mx.Lock()
	th.slot = slot
}

func (th *treasuryHook) CommitFinish() bool {
	// Real bug (2026-09-15): this was commented out, so `CommitStart`'s
	// `Lock()` was never released -- the very next `CommitStart` call
	// (or any `SOLBalance()`/`TokenBalance()` reader, via their own
	// `RLock()`) would block forever. Balance tracking only ever worked
	// for the first commit before this.
	//
	// Third real bug found the same day, live (2026-09-15) -- the actual
	// one blocking a real sweep: `util.PendingSubscriptionStatus.Subscribe`
	// (called from Init/getOrCreateChild) only ever queues a pending
	// subscription; it never issues the real `graph.Graph.Subscribe`
	// call. That only happens inside `Check`, which callers are expected
	// to call every commit (see util.FetchWalletBalance's own
	// walletBalanceHandler.CommitFinish: `return h.pss.Check(h.slot)`).
	// Without this, every Subscribe call this package ever makes sits in
	// that queue forever, and not one account update for the parent or
	// any child pubkey is ever actually requested from the server --
	// confirmed live: 79 real commits observed over 25s, zero of them
	// containing any account this treasury had "subscribed" to.
	if th.te.g != nil {
		th.te.g.Check(th.slot)
	}
	th.te.mx.Unlock()
	// Second real bug found the same day, live (2026-09-15): this
	// returned `true` -- state.Client.Hook's event loop reads
	// CommitFinish's return value as `isDone` and stops entirely once
	// it's true (see bot/state/hook.go: `for !isDone { ... isDone =
	// hook.CommitFinish() }`). Treasury is a long-running balance
	// tracker, not a one-shot query like util.FetchWalletBalance's
	// walletBalanceHandler (which legitimately returns `pss.Check(slot)`
	// and wants to stop once its subscription's quiet-window closes) --
	// returning `true` here silently stopped ALL further account
	// delivery after the very first commit, for the rest of the
	// process's life. Invisible in this package's own unit tests (they
	// call OnSol/OnToken directly, never going through Hook's loop at
	// all) -- only surfaced running a real sweep live: balances stayed
	// frozen at whatever (usually nothing) arrived in that first commit.
	return false
}

func (th *treasuryHook) Ctx() context.Context {
	return th.te.ctx
}

func (th *treasuryHook) Init(g graph.Graph) error {
	th.te.mx.Lock()
	th.te.g = util.CreatePendingSubscriptionList(th.te.ctx, g, 10, 10)
	th.te.g.Subscribe(th.te.parentKey.PublicKey(), graph.WeightDirect|graph.WeightSPLTokenOwner, 2)
	th.te.mx.Unlock()
	// Signals Create's own wait that te.g is now safe to use -- see
	// readyC's doc comment.
	close(th.te.readyC)
	return nil
}

func (th *treasuryHook) OnAccount(account graph.Account, isNew bool)                           {}
func (th *treasuryHook) OnAgent(a *graph.AnchorAccount[*solpipe_cba.Agent])                    {}
func (th *treasuryHook) OnBidList(a *graph.AnchorAccount[*solpipe_cba.BidList])                {}
func (th *treasuryHook) OnControllerAPI(a *graph.AnchorAccount[*solpipe_cba.ControllerApi])    {}
func (th *treasuryHook) OnDelegation(a *graph.AnchorAccount[*solpipe_treasury.Delegation])     {}
func (th *treasuryHook) OnJar(a *graph.AnchorAccount[*solpipe_treasury.Controller])            {}
func (th *treasuryHook) OnMarket(a *graph.AnchorAccount[*solpipe_cba.Controller])              {}
func (th *treasuryHook) OnMint(a graph.Account, mint *token.Mint)                              {}
func (th *treasuryHook) OnPayout(a *graph.AnchorAccount[*solpipe_cba.Payout])                  {}
func (th *treasuryHook) OnPeriodRing(a *graph.AnchorAccount[*solpipe_cba.PeriodRing])          {}
func (th *treasuryHook) OnPipeline(a *graph.AnchorAccount[*solpipe_cba.Pipeline])              {}
func (th *treasuryHook) OnRefund(a *graph.AnchorAccount[*solpipe_cba.Refunds])                 {}
func (th *treasuryHook) OnSlot(slot graph.Slot, status graph.SlotStatus)                       {}
func (th *treasuryHook) OnSpendRequest(a *graph.AnchorAccount[*solpipe_treasury.SpendRequest]) {}

// an account has been deleted -- clears whichever balance
// (SOL-account or SPL token-account) `header.Pubkey` was tracking, via
// the same `mAccountWallet` reverse-lookup `OnSol`/`OnToken` populate.
func (th *treasuryHook) OnDelete(header graph.AccountHeader) {
	id, present := th.te.store.mAccountWallet[header.Pubkey]
	if !present {
		return
	}
	delete(th.te.store.mAccountWallet, header.Pubkey)
	s := th.te.store.storeByID(id)
	if s == nil {
		return
	}
	if s.pubkey.Equals(header.Pubkey) {
		// the wallet's own SOL-holding account was closed
		s.sol = 0
		return
	}
	// one of the wallet's SPL token accounts was closed
	delete(s.mTokenByAccount, header.Pubkey)
	s.recomputeTokenTotals()
}

// fill in sol
func (th *treasuryHook) OnSol(header graph.AccountHeader) {
	if th.te.store.parent.pubkey.Equals(header.Pubkey) {
		th.te.store.parent.sol = header.Lamports
		return
	}
	lookupKey := th.te.store.mAccountWallet[header.Pubkey]
	s, present := th.te.store.mChild[lookupKey]
	if present {
		s.sol = header.Lamports
	}
}

// fill in token -- `tokenAccount.Owner` is the wallet that actually holds
// these tokens, `a.Header().Pubkey` is the SPL token account's own
// address (an ATA, not the wallet's own pubkey -- a wallet can hold
// several, one per mint). Records the balance under both, so
// `TokenBalance()` (aggregated across all of a wallet's token accounts)
// and `OnDelete` (per-account cleanup once one of them closes) both stay
// correct as accounts come and go.
func (th *treasuryHook) OnToken(a graph.Account, tokenAccount *token.Account) {
	s := th.te.store.storeByOwner(tokenAccount.Owner)
	if s == nil {
		// not a wallet we track
		return
	}
	accountPubkey := a.Header().Pubkey
	th.te.store.mAccountWallet[accountPubkey] = s.id
	if s.mTokenByAccount[accountPubkey] == nil {
		s.mTokenByAccount[accountPubkey] = make(map[sgo.PublicKey]uint64)
	}
	s.mTokenByAccount[accountPubkey][tokenAccount.Mint] = tokenAccount.Amount
	s.recomputeTokenTotals()
}
