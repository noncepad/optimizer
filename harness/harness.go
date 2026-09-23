package optimizer

import (
	"context"
	"fmt"
	"time"

	"git.noncepad.com/pkg/bot/txbuilder"
	"git.noncepad.com/pkg/optimizer/api"
	"git.noncepad.com/pkg/solpipe-util/graph"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// budgetExternal is treasuryExternal's api.Budget implementation for one
// child wallet -- created via treasuryExternal.Budget, which reuses the
// same getOrCreateChild path Child() does, so a Budget handle and a
// Wallet handle for the same id always observe the same underlying
// singleStore.
//
// Every mutating method here follows the same fire-and-forget pattern
// testperplatencyv1/eval.go's boot transfer already established for this
// codebase: build one txbuilder.Helper transaction, hand it to
// FinishTx() on a background goroutine, and log the outcome -- there is
// no error return on the api.Budget methods themselves for the caller to
// check synchronously.
type budgetExternal struct {
	te    *treasuryExternal
	child *singleStore
}

func (b *budgetExternal) ID() sgo.PublicKey {
	return b.child.id
}

func (b *budgetExternal) Pubkey() sgo.PublicKey {
	return b.child.pubkey
}

// SetPositionCloser registers closer on this child's shared singleStore
// (not on this particular *budgetExternal wrapper -- Budget() hands back
// a fresh wrapper on every call, but they all point at the same
// singleStore, so a closer registered through one call is still visible
// to a later, independently-obtained Budget handle for the same id).
func (b *budgetExternal) SetPositionCloser(closer func(ctx context.Context) error) {
	b.te.mx.Lock()
	b.child.positionCloser = closer
	b.te.mx.Unlock()
}

// SetSOL tops the child wallet up to budget lamports of SOL, funded from
// the parent -- but only once the gap exceeds api.SolDelta, so ordinary
// balance drift (rent/fees trickling out) doesn't trigger a top-up
// transaction on every small change. Does nothing if the child is
// already at or above budget, or if the parent doesn't have topUp
// lamports to send (logged, not silently dropped).
func (b *budgetExternal) SetSOL(budget graph.Lamports) {
	entry := logger.FromContext(b.te.ctx)
	b.te.mx.RLock()
	current := b.child.sol
	parentSOL := b.te.store.parent.sol
	parentPubkey := b.te.store.parent.pubkey
	b.te.mx.RUnlock()

	if current >= budget || budget-current <= api.SolDelta {
		return
	}
	topUp := budget - current
	if parentSOL < topUp {
		entry.Error(fmt.Sprintf(
			"treasury: budget %s -- SetSOL: parent has %d lamports, need %d to reach target %d",
			b.child.id, parentSOL, topUp, budget,
		))
		return
	}

	ctx, cancel := context.WithTimeout(b.te.ctx, 60*time.Second)
	helper, err := b.te.builder.Helper(ctx)
	if err != nil {
		cancel()
		entry.Error(fmt.Sprintf("treasury: budget %s -- SetSOL: failed to create helper: %s", b.child.id, err))
		return
	}
	helper.TransferSOL(parentPubkey, b.child.pubkey, topUp)
	go func() {
		defer cancel()
		sig, slot, err := helper.FinishTx()
		if err != nil {
			entry.Error(fmt.Sprintf("treasury: budget %s -- SetSOL top-up failed: %s", b.child.id, err))
			return
		}
		entry.Info(fmt.Sprintf(
			"treasury: budget %s -- topped up %d lamports SOL (target %d): %s @ slot %d",
			b.child.id, topUp, budget, sig, slot,
		))
	}()
}

// Fund sends amount of mint from the parent to the child wallet,
// creating the child's associated token account first if it doesn't
// already have one for this mint. Declines (logged) if the parent has
// no known token account for mint -- Fund can only spend against a
// balance this treasury has actually observed on-chain, not a mint it's
// never seen the parent hold.
func (b *budgetExternal) Fund(mint sgo.PublicKey, amount uint64) {
	entry := logger.FromContext(b.te.ctx)
	b.te.mx.RLock()
	from, present := b.te.store.parent.tokenAccountFor(mint)
	to, toExists := b.child.tokenAccountFor(mint)
	parentPubkey := b.te.store.parent.pubkey
	b.te.mx.RUnlock()

	if !present {
		entry.Error(fmt.Sprintf("treasury: budget %s -- Fund: parent has no known token account for mint %s", b.child.id, mint))
		return
	}

	ctx, cancel := context.WithTimeout(b.te.ctx, 60*time.Second)
	helper, err := b.te.builder.Helper(ctx)
	if err != nil {
		cancel()
		entry.Error(fmt.Sprintf("treasury: budget %s -- Fund: failed to create helper: %s", b.child.id, err))
		return
	}
	if !toExists {
		to = helper.ATACreate(b.child.pubkey, mint)
	}
	helper.TransferToken(parentPubkey, from, to, amount)
	go func() {
		defer cancel()
		sig, slot, err := helper.FinishTx()
		if err != nil {
			entry.Error(fmt.Sprintf("treasury: budget %s -- Fund %d of mint %s failed: %s", b.child.id, amount, mint, err))
			return
		}
		entry.Info(fmt.Sprintf(
			"treasury: budget %s -- funded %d of mint %s: %s @ slot %d",
			b.child.id, amount, mint, sig, slot,
		))
	}()
}

// Sweep sends the child's balance of mint above minAmount back to the
// parent, creating the parent's associated token account first if it
// doesn't already have one for this mint. Does nothing if the child
// doesn't hold mint at all, or already holds at or below minAmount.
func (b *budgetExternal) Sweep(mint sgo.PublicKey, minAmount uint64) {
	entry := logger.FromContext(b.te.ctx)
	b.te.mx.RLock()
	from, fromExists := b.child.tokenAccountFor(mint)
	balance := b.child.mToken[mint]
	to, toExists := b.te.store.parent.tokenAccountFor(mint)
	parentPubkey := b.te.store.parent.pubkey
	b.te.mx.RUnlock()

	if !fromExists || balance <= minAmount {
		return
	}
	sweepAmount := balance - minAmount

	ctx, cancel := context.WithTimeout(b.te.ctx, 60*time.Second)
	helper, err := b.te.builder.Helper(ctx)
	if err != nil {
		cancel()
		entry.Error(fmt.Sprintf("treasury: budget %s -- Sweep: failed to create helper: %s", b.child.id, err))
		return
	}
	if !toExists {
		to = helper.ATACreate(parentPubkey, mint)
	}
	helper.TransferToken(b.child.pubkey, from, to, sweepAmount)
	go func() {
		defer cancel()
		sig, slot, err := helper.FinishTx()
		if err != nil {
			entry.Error(fmt.Sprintf("treasury: budget %s -- Sweep of mint %s failed: %s", b.child.id, mint, err))
			return
		}
		entry.Info(fmt.Sprintf(
			"treasury: budget %s -- swept %d of mint %s, left %d: %s @ slot %d",
			b.child.id, sweepAmount, mint, minAmount, sig, slot,
		))
	}()
}

// Close first invokes whatever position closer was registered via
// SetPositionCloser, to unwind this child's protocol-specific positions
// (lending deposits, perps, LP, etc.) -- if none has been registered,
// this step is skipped entirely and only SOL/SPL token balances are
// swept, same as before SetPositionCloser existed. Aborts (logged,
// without touching any balance) if the closer itself returns an error,
// since sweeping while a position is still half-unwound risks leaving
// something stranded.
//
// It then sweeps every token balance this treasury knows the child
// holds back to the parent and closes those token accounts, then
// sweeps the child's remaining SOL back to the parent too -- all in one
// transaction, since SPL's CloseAccount instruction only requires a
// zero balance at execution time, not a separately-confirmed prior
// transaction.
//
// Real limitation: that sweep step uses whatever balances this treasury
// has already observed via its own graph subscription -- everything
// this package tracks is push-based (see OnSol/OnToken), not a
// synchronous on-chain read. If the position closer just unwound a
// position into a fresh token balance, that update may not have
// arrived here yet by the time the sweep below runs. Call Close again
// once it does to sweep the rest.
//
// Still does NOT use Jupiter to power sweeps, despite api.Budget.Close's
// doc comment mentioning it -- swapping to a common asset before
// sweeping would need a swap-quote/route dependency this package
// doesn't have. A real gap, left for whoever needs it, rather than
// faked here.
//
// If everything doesn't fit in one transaction (many distinct mints),
// stops early and logs how much is left -- call Close again once the
// first transaction confirms to finish the rest.
func (b *budgetExternal) Close() error {
	entry := logger.FromContext(b.te.ctx)

	b.te.mx.RLock()
	closer := b.child.positionCloser
	b.te.mx.RUnlock()
	if closer != nil {
		// Generous relative to the sweep transaction's own 60s budget
		// below -- unwinding a real lending/perp/LP position can need
		// several sequential, individually-confirmed transactions (see
		// e.g. catscope-rust-bot's testperpv1 refresh_obligation-then-
		// withdraw sequence for Kamino), not just one.
		closeCtx, closeCancel := context.WithTimeout(b.te.ctx, 5*time.Minute)
		err := closer(closeCtx)
		closeCancel()
		if err != nil {
			err = fmt.Errorf("treasury: budget %s -- Close: protocol position closer failed, aborting before sweeping any balance: %w", b.child.id, err)
			entry.Error(err.Error())
			return err
		}
		entry.Info(fmt.Sprintf("treasury: budget %s -- protocol positions closed, proceeding to sweep SOL/token balances", b.child.id))
	} else {
		entry.Info(fmt.Sprintf("treasury: budget %s -- Close: no protocol position closer registered, sweeping SOL/token balances only", b.child.id))
	}

	type mintInfo struct {
		account sgo.PublicKey
		amount  uint64
	}
	b.te.mx.RLock()
	mMint := make(map[sgo.PublicKey]mintInfo, len(b.child.mTokenByAccount))
	for account, byMint := range b.child.mTokenByAccount {
		for mint, amount := range byMint {
			mMint[mint] = mintInfo{account: account, amount: amount}
		}
	}
	mParentAccountByMint := make(map[sgo.PublicKey]sgo.PublicKey, len(b.te.store.parent.mTokenByAccount))
	for account, byMint := range b.te.store.parent.mTokenByAccount {
		for mint := range byMint {
			mParentAccountByMint[mint] = account
		}
	}
	childSOL := b.child.sol
	childPubkey := b.child.pubkey
	parentPubkey := b.te.store.parent.pubkey
	b.te.mx.RUnlock()

	if len(mMint) == 0 && childSOL == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(b.te.ctx, 60*time.Second)
	helper, err := b.te.builder.Helper(ctx)
	if err != nil {
		cancel()
		err = fmt.Errorf("treasury: budget %s -- Close: failed to create helper: %w", b.child.id, err)
		entry.Error(err.Error())
		return err
	}

	closedCount := 0
	for mint, info := range mMint {
		if txbuilder.TxMaxSize < helper.EstimatedSize() {
			entry.Info(fmt.Sprintf(
				"treasury: budget %s -- Close: stopping early, %d/%d token account(s) closed this pass -- call Close again for the rest",
				b.child.id, closedCount, len(mMint),
			))
			break
		}
		to, exists := mParentAccountByMint[mint]
		if !exists {
			to = helper.ATACreate(parentPubkey, mint)
			mParentAccountByMint[mint] = to
		}
		if info.amount > 0 {
			helper.TransferToken(childPubkey, info.account, to, info.amount)
		}
		helper.TokenClose(childPubkey, info.account, parentPubkey)
		closedCount++
	}
	if childSOL > 0 {
		helper.TransferSOL(childPubkey, parentPubkey, childSOL)
	}

	go func() {
		defer cancel()
		sig, slot, err := helper.FinishTx()
		if err != nil {
			entry.Error(fmt.Sprintf("treasury: budget %s -- Close failed: %s", b.child.id, err))
			return
		}
		entry.Info(fmt.Sprintf(
			"treasury: budget %s -- closed %d token account(s), swept %d lamports SOL: %s @ slot %d",
			b.child.id, closedCount, childSOL, sig, slot,
		))
	}()
	return nil
}
