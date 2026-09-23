package optimizer

import (
	"sync"
	"testing"

	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/token"
)

// fakeAccount is the minimal graph.Account this package's OnToken ever
// touches (just Header()) -- avoids pulling in a real subscription/graph
// setup just to unit-test the event handlers below.
type fakeAccount struct {
	header graph.AccountHeader
}

func (f fakeAccount) Copy() graph.Account                  { return f }
func (f fakeAccount) Header() graph.AccountHeader          { return f.header }
func (f fakeAccount) ID() graph.AccountID                  { return f.header.AccountID }
func (f fakeAccount) Data() []byte                         { return nil }
func (f fakeAccount) AnchorData(d [8]byte) ([]byte, error) { return nil, nil }

func newTestTreasury(t *testing.T) (*treasuryExternal, *treasuryHook) {
	t.Helper()
	parentPubkey := sgo.NewWallet().PublicKey()
	te := &treasuryExternal{
		mx:    &sync.RWMutex{},
		store: createStore(parentPubkey),
	}
	return te, &treasuryHook{te: te}
}

// addChild registers a child wallet the same way treasuryExternal.Child
// does, minus the real subscription call -- this test exercises the
// event handlers, not the subscription plumbing.
func addChild(te *treasuryExternal, id, pubkey sgo.PublicKey) *singleStore {
	s := createSingleStore(id, pubkey)
	te.store.mChild[id] = s
	te.store.mAccountWallet[pubkey] = id
	return s
}

func TestOnTokenAggregatesAcrossAccountsForParent(t *testing.T) {
	te, th := newTestTreasury(t)
	mintA := sgo.NewWallet().PublicKey()
	mintB := sgo.NewWallet().PublicKey()
	ataA := sgo.NewWallet().PublicKey()
	ataB := sgo.NewWallet().PublicKey()

	th.OnToken(fakeAccount{header: graph.AccountHeader{Pubkey: ataA}}, &token.Account{
		Mint: mintA, Owner: te.store.parent.pubkey, Amount: 100,
	})
	th.OnToken(fakeAccount{header: graph.AccountHeader{Pubkey: ataB}}, &token.Account{
		Mint: mintB, Owner: te.store.parent.pubkey, Amount: 250,
	})

	got := te.store.parent.TokenBalance()
	if got[mintA] != 100 || got[mintB] != 250 {
		t.Fatalf("unexpected balances: %v", got)
	}

	// A second account for the SAME mint must add, not overwrite.
	ataA2 := sgo.NewWallet().PublicKey()
	th.OnToken(fakeAccount{header: graph.AccountHeader{Pubkey: ataA2}}, &token.Account{
		Mint: mintA, Owner: te.store.parent.pubkey, Amount: 50,
	})
	got = te.store.parent.TokenBalance()
	if got[mintA] != 150 {
		t.Fatalf("expected mintA=150 after second account, got %d", got[mintA])
	}
}

func TestOnTokenRoutesToCorrectChild(t *testing.T) {
	te, th := newTestTreasury(t)
	childID := sgo.NewWallet().PublicKey()
	childPubkey := sgo.NewWallet().PublicKey()
	child := addChild(te, childID, childPubkey)

	mint := sgo.NewWallet().PublicKey()
	ata := sgo.NewWallet().PublicKey()
	th.OnToken(fakeAccount{header: graph.AccountHeader{Pubkey: ata}}, &token.Account{
		Mint: mint, Owner: childPubkey, Amount: 42,
	})

	if got := child.TokenBalance()[mint]; got != 42 {
		t.Fatalf("expected child balance 42, got %d", got)
	}
	if got := te.store.parent.TokenBalance()[mint]; got != 0 {
		t.Fatalf("expected parent unaffected, got %d", got)
	}

	// An owner that isn't the parent or any known child is silently
	// ignored (not one of ours), rather than panicking or polluting a
	// wallet's balance.
	th.OnToken(fakeAccount{header: graph.AccountHeader{Pubkey: sgo.NewWallet().PublicKey()}}, &token.Account{
		Mint: mint, Owner: sgo.NewWallet().PublicKey(), Amount: 999,
	})
	if got := child.TokenBalance()[mint]; got != 42 {
		t.Fatalf("untracked owner must not affect known child balance, got %d", got)
	}
}

func TestOnDeleteClearsTokenAccountNotWholeWallet(t *testing.T) {
	te, th := newTestTreasury(t)
	childID := sgo.NewWallet().PublicKey()
	childPubkey := sgo.NewWallet().PublicKey()
	child := addChild(te, childID, childPubkey)

	mint := sgo.NewWallet().PublicKey()
	ata := sgo.NewWallet().PublicKey()
	th.OnToken(fakeAccount{header: graph.AccountHeader{Pubkey: ata}}, &token.Account{
		Mint: mint, Owner: childPubkey, Amount: 7,
	})
	child.sol = 12345

	th.OnDelete(graph.AccountHeader{Pubkey: ata})

	if got := child.TokenBalance()[mint]; got != 0 {
		t.Fatalf("expected token balance cleared after delete, got %d", got)
	}
	if child.SOLBalance() != 12345 {
		t.Fatalf("deleting a token account must not touch the wallet's SOL balance")
	}
	if _, present := te.store.mAccountWallet[ata]; present {
		t.Fatalf("expected mAccountWallet entry for deleted account to be removed")
	}
}

func TestOnDeleteOfWalletAccountZeroesSOL(t *testing.T) {
	te, th := newTestTreasury(t)
	childID := sgo.NewWallet().PublicKey()
	childPubkey := sgo.NewWallet().PublicKey()
	child := addChild(te, childID, childPubkey)
	child.sol = 999

	th.OnDelete(graph.AccountHeader{Pubkey: childPubkey})

	if child.SOLBalance() != 0 {
		t.Fatalf("expected SOL balance zeroed after wallet account deletion, got %d", child.SOLBalance())
	}
}

func TestCommitStartFinishRoundTrips(t *testing.T) {
	// Real, live-confirmed bug #1: CommitFinish used to never call
	// Unlock, so a second CommitStart would deadlock. Guard against a
	// regression with a bounded number of round trips instead of relying
	// on a live run to notice a hang.
	//
	// Real, live-confirmed bug #2 (found the same day, only via an
	// actual live run -- invisible here since these tests never go
	// through state.Client.Hook's event loop): CommitFinish must return
	// false. state.Client.Hook treats a true return as "isDone" and
	// stops its whole event loop for good after just one commit -- fatal
	// for a long-running balance tracker, which needs to keep observing
	// commits for the life of the process.
	_, th := newTestTreasury(t)
	for i := 0; i < 3; i++ {
		th.CommitStart(0)
		if th.CommitFinish() {
			t.Fatalf("CommitFinish returned true -- state.Client.Hook would stop its event loop here")
		}
	}
}
