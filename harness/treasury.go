package optimizer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"git.noncepad.com/pkg/bot/solpipe"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/optimizer/api"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/common"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

type treasuryExternal struct {
	ctx       context.Context
	builder   *txbuilder.BuildManager
	mx        *sync.RWMutex
	errorC    chan error
	g         *util.PendingSubscriptionStatus
	store     *localStore
	parentKey sgo.PrivateKey
	// readyC closes once treasuryHook.Init has actually run and set g --
	// see Create's own doc comment for why callers must wait on this.
	readyC chan struct{}
}

// readyTimeout bounds how long Create waits for the hook's Init to run
// (see readyC) before giving up -- real, live-hit failure mode
// (2026-09-15): the local bidder-proxy socket wasn't reachable (daemon
// not running under this process's own user), so DetachHook's stream
// never connected and Init never fired; without this bound, Create
// would have hung forever rather than returning a diagnosable error.
const readyTimeout = 30 * time.Second

func Create(ctx context.Context, client state.Client, builder *txbuilder.BuildManager, parentKey sgo.PrivateKey) (api.Treasury, error) {
	te := new(treasuryExternal)
	te.ctx = ctx
	te.builder = builder
	te.builder.AppendKey(parentKey)
	te.mx = &sync.RWMutex{}
	te.store = createStore(parentKey.PublicKey())
	te.parentKey = parentKey
	te.errorC = make(chan error, 10)
	te.readyC = make(chan struct{})
	hook, err := solpipe.New(ctx, &treasuryHook{te: te})
	if err != nil {
		return nil, fmt.Errorf("failed to construct solpipe hook: %s", err)
	}
	go client.DetachHook(te.errorC, hook)

	// Real, live-hit bug (2026-09-15): Create used to return immediately
	// here, before treasuryHook.Init (which DetachHook only calls once
	// its own connection actually succeeds) had run -- g stays nil until
	// then, and every Child/Budget call reads it unconditionally
	// (getOrCreateChild's te.g.Subscribe), so a caller that used the
	// Treasury right away nil-panicked instead of getting a clean error.
	readyCtx, readyCancel := context.WithTimeout(ctx, readyTimeout)
	defer readyCancel()
	select {
	case <-te.readyC:
	case err := <-te.errorC:
		return nil, fmt.Errorf("treasury hook failed before becoming ready: %s", err)
	case <-readyCtx.Done():
		return nil, fmt.Errorf(
			"treasury: hook did not become ready within %s (is the bidder-proxy daemon running and reachable?): %w",
			readyTimeout, readyCtx.Err(),
		)
	}
	return te, nil
}

func (te *treasuryExternal) Parent() api.Wallet {
	return te.store.parent
}

func (te *treasuryExternal) Child(id sgo.PublicKey) api.Wallet {
	te.mx.Lock()
	x := te.getOrCreateChild(id)
	te.mx.Unlock()
	return x
}

// Children returns the ids of every child wallet Child/Budget has
// derived so far -- there's no way to discover a child that's never been
// asked for by id (a fresh HKDF-derived key has no on-chain footprint of
// its own to enumerate from), so this only ever reflects what this
// process itself has already touched.
func (te *treasuryExternal) Children() []sgo.PublicKey {
	te.mx.RLock()
	ids := make([]sgo.PublicKey, 0, len(te.store.mChild))
	for id := range te.store.mChild {
		ids = append(ids, id)
	}
	te.mx.RUnlock()
	return ids
}

// Budget returns the budget-management handle for id's child wallet,
// creating it first if this is the first time id has been seen -- same
// as Child, just returning the fund-management surface instead of the
// read-only balance surface.
func (te *treasuryExternal) Budget(id sgo.PublicKey) api.Budget {
	te.mx.Lock()
	x := te.getOrCreateChild(id)
	te.mx.Unlock()
	return &budgetExternal{te: te, child: x}
}

// getOrCreateChild resolves id to its singleStore, deriving and
// registering a fresh child wallet key the first time id is seen.
// Caller must hold te.mx.
func (te *treasuryExternal) getOrCreateChild(id sgo.PublicKey) *singleStore {
	x, present := te.store.mChild[id]
	if !present {
		key := common.DeriveChildKeyV2(te.parentKey, id)
		x = createSingleStore(id, key.PublicKey())
		te.builder.AppendKey(key)
		te.store.mChild[id] = x
		// Real bug (2026-09-15): this omitted WeightDirect (Parent's own
		// subscribe, in treasuryHook.Init, already correctly includes
		// it: `WeightDirect|WeightSPLTokenOwner`) -- confirmed live: with
		// only WeightSPLTokenOwner, a real child wallet's token accounts
		// never arrived at all, while the parent's (subscribed with both
		// flags) did. Matches util.FetchWalletBalance's own proven
		// pattern too (`graph.WeightAll` -- every flag, not a subset).
		te.g.Subscribe(x.pubkey, graph.WeightDirect|graph.WeightSPLTokenOwner, 2)
		te.store.mAccountWallet[x.pubkey] = id
	}
	return x
}

func (te *treasuryExternal) SOLBalance() uint64 {
	te.mx.RLock()
	sol := te.store.parent.sol
	te.mx.RUnlock()
	return sol
}

func (te *treasuryExternal) TokenBalance() map[sgo.PublicKey]uint64 {
	te.mx.RLock()
	mToken := te.store.parent.TokenBalance()
	te.mx.RUnlock()
	return mToken
}

func (s *singleStore) ID() sgo.PublicKey {
	return s.id
}

func (s *singleStore) PublicKey() sgo.PublicKey {
	return s.pubkey
}

// SOLBalance returns the current SOL balance in lamports
func (s *singleStore) SOLBalance() uint64 {
	return s.sol
}

// TokenBalance returns the current balance of tokens
func (s *singleStore) TokenBalance() map[sgo.PublicKey]uint64 {
	mToken := make(map[sgo.PublicKey]uint64, len(s.mToken))
	for k, v := range s.mToken {
		mToken[k] = v
	}
	return mToken
}

// tokenAccountFor returns one of this wallet's own token accounts
// already known (via a prior OnToken) to hold mint, if any -- used by
// Budget's Fund/Sweep/Close to reuse an existing ATA instead of issuing
// a Create instruction against an account that's already live on-chain,
// which fails outright (this codebase's ATACreate helper wraps the
// plain, non-idempotent associated-token-account Create instruction).
func (s *singleStore) tokenAccountFor(mint sgo.PublicKey) (sgo.PublicKey, bool) {
	for account, byMint := range s.mTokenByAccount {
		if _, present := byMint[mint]; present {
			return account, true
		}
	}
	return sgo.PublicKey{}, false
}

// recomputeTokenTotals rebuilds the aggregated per-mint view (`mToken`)
// from the per-account breakdown (`mTokenByAccount`), which stays the
// source of truth -- a wallet can hold the same mint across more than one
// token account, so the aggregate can't just be updated in place from a
// single account's new balance. Cheap enough to redo in full on every
// `OnToken`/`OnDelete`: a wallet's own token-account count is small and
// bounded, not proportional to chain activity.
func (s *singleStore) recomputeTokenTotals() {
	mToken := make(map[sgo.PublicKey]uint64, len(s.mToken))
	for _, byMint := range s.mTokenByAccount {
		for mint, amount := range byMint {
			mToken[mint] += amount
		}
	}
	s.mToken = mToken
}
