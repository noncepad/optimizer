package optimizer

import (
	"context"
	"errors"
	"testing"

	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/token"
)

// newTestBudget builds a budgetExternal with a nil builder -- safe as
// long as the test only exercises a path that returns before ever
// reaching b.te.builder.Helper(...), i.e. the no-op/early-return
// decisions below. Anything that actually needs to build a transaction
// belongs in a live/integration test, not here.
func newTestBudget(t *testing.T) (*treasuryExternal, *budgetExternal) {
	t.Helper()
	te, _ := newTestTreasury(t)
	te.ctx = context.Background()
	childID := sgo.NewWallet().PublicKey()
	child := addChild(te, childID, sgo.NewWallet().PublicKey())
	return te, &budgetExternal{te: te, child: child}
}

func TestBudgetIDAndPubkey(t *testing.T) {
	_, b := newTestBudget(t)
	if b.ID() != b.child.id {
		t.Fatalf("ID() mismatch")
	}
	if b.Pubkey() != b.child.pubkey {
		t.Fatalf("Pubkey() mismatch")
	}
}

func TestSetSOLNoOpWhenAlreadyAtOrAboveBudget(t *testing.T) {
	_, b := newTestBudget(t)
	b.child.sol = 1_000_000
	// builder is nil -- if SetSOL didn't return early, this would panic.
	b.SetSOL(1_000_000)
	b.SetSOL(500_000)
}

func TestSetSOLNoOpWhenGapBelowSolDelta(t *testing.T) {
	_, b := newTestBudget(t)
	b.child.sol = 1_000_000
	// gap of 1 lamport is well under api.SolDelta -- must not attempt a
	// top-up (nil builder would panic if it tried).
	b.SetSOL(1_000_001)
}

func TestFundNoOpWhenParentHasNoKnownAccountForMint(t *testing.T) {
	_, b := newTestBudget(t)
	mint := sgo.NewWallet().PublicKey()
	// parent has never been observed holding this mint -- Fund must
	// decline rather than reach the nil builder.
	b.Fund(mint, 100)
}

func TestSweepNoOpWhenBelowMinAmount(t *testing.T) {
	te, b := newTestBudget(t)
	mint := sgo.NewWallet().PublicKey()
	ata := sgo.NewWallet().PublicKey()
	th := &treasuryHook{te: te}
	th.OnToken(fakeAccount{header: graph.AccountHeader{Pubkey: ata}}, &token.Account{
		Mint: mint, Owner: b.child.pubkey, Amount: 50,
	})
	// balance (50) is at minAmount -- nothing to sweep.
	b.Sweep(mint, 50)
}

func TestCloseNoOpWhenNothingToClose(t *testing.T) {
	_, b := newTestBudget(t)
	// no token accounts, zero SOL -- Close must return before reaching
	// the nil builder.
	b.Close()
}

func TestCloseInvokesRegisteredPositionCloser(t *testing.T) {
	_, b := newTestBudget(t)
	called := 0
	b.SetPositionCloser(func(ctx context.Context) error {
		called++
		return nil
	})
	// still nothing to sweep -- Close must call the closer, then still
	// return before reaching the nil builder.
	b.Close()
	if called != 1 {
		t.Fatalf("expected position closer to be called once, got %d", called)
	}
}

func TestCloseAbortsWhenPositionCloserFails(t *testing.T) {
	te, b := newTestBudget(t)
	mint := sgo.NewWallet().PublicKey()
	ata := sgo.NewWallet().PublicKey()
	th := &treasuryHook{te: te}
	th.OnToken(fakeAccount{header: graph.AccountHeader{Pubkey: ata}}, &token.Account{
		Mint: mint, Owner: b.child.pubkey, Amount: 100,
	})
	b.SetPositionCloser(func(ctx context.Context) error {
		return errors.New("simulated unwind failure")
	})
	// If Close pressed on despite the closer's error, it would reach the
	// nil builder (there's a real token balance to sweep above) and
	// panic -- reaching here at all is the assertion.
	b.Close()
	if got := b.child.TokenBalance()[mint]; got != 100 {
		t.Fatalf("expected balance untouched after aborted Close, got %d", got)
	}
}

func TestPositionCloserPersistsAcrossBudgetHandles(t *testing.T) {
	te, b := newTestBudget(t)
	called := 0
	b.SetPositionCloser(func(ctx context.Context) error {
		called++
		return nil
	})
	// A fresh handle for the same child (as te.Budget(id) would hand
	// back on a second call) must still see the closer -- it's stored on
	// the shared singleStore, not this particular *budgetExternal.
	fresh := &budgetExternal{te: te, child: b.child}
	fresh.Close()
	if called != 1 {
		t.Fatalf("expected closer registered via one handle to be visible to another, got %d calls", called)
	}
}
