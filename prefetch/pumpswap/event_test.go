package pumpswap

import (
	"database/sql"
	"errors"
	"log/slog"
	"testing"

	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
	_ "modernc.org/sqlite"
)

// fakeAccount is a minimal graph.Account for driving OnAccount directly in
// tests, without a live validator/graph connection.
type fakeAccount struct {
	pubkey sgo.PublicKey
	owner  sgo.PublicKey
	data   []byte
}

func (f fakeAccount) Copy() graph.Account { return f }
func (f fakeAccount) Header() graph.AccountHeader {
	return graph.AccountHeader{Pubkey: f.pubkey, Owner: f.owner, DataSize: uint32(len(f.data))}
}
func (f fakeAccount) ID() graph.AccountID { return 0 }
func (f fakeAccount) Data() []byte        { return f.data }
func (f fakeAccount) AnchorData(d [8]byte) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func testHandler(t *testing.T) (*eventHandler, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// A single shared connection -- separate pooled connections would each
	// get their own private :memory: database.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(Schema); err != nil {
		t.Fatal(err)
	}
	h := &eventHandler{
		logger:          slog.Default(),
		db:              db,
		mVaultToPool:    make(map[sgo.PublicKey]vaultRef),
		mPendingBalance: make(map[sgo.PublicKey]uint64),
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	h.tx = tx
	return h, db
}

func syntheticVaultBody(amount uint64) []byte {
	data := make([]byte, 165)
	// mint(32) + owner(32) already zero, amount at offset 64.
	le := func(v uint64) []byte {
		b := make([]byte, 8)
		for i := range b {
			b[i] = byte(v >> (8 * i))
		}
		return b
	}
	copy(data[64:72], le(amount))
	data[108] = 1 // state = initialized
	return data
}

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func poolBalances(t *testing.T, q queryer, pool sgo.PublicKey) (base, quote uint64) {
	t.Helper()
	if err := q.QueryRow(`SELECT base_balance, quote_balance FROM pumpswap_pool WHERE pool = ?`, pool[:]).
		Scan(&base, &quote); err != nil {
		t.Fatal(err)
	}
	return
}

func TestPoolThenVaultBalances(t *testing.T) {
	pool := testPubkey(1)
	baseMint, quoteMint := testPubkey(2), testPubkey(3)
	baseVault, quoteVault := testPubkey(4), testPubkey(5)
	coinCreator := testPubkey(6)
	h, db := testHandler(t)

	h.OnAccount(fakeAccount{
		pubkey: pool, owner: ProgramID,
		data: syntheticPoolBody(baseMint, quoteMint, baseVault, quoteVault, coinCreator),
	}, true)
	h.OnAccount(fakeAccount{pubkey: baseVault, owner: sgotkn.ProgramID, data: syntheticVaultBody(1_000_000)}, true)
	h.OnAccount(fakeAccount{pubkey: quoteVault, owner: sgotkn.ProgramID, data: syntheticVaultBody(2_000_000)}, true)

	if err := h.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	base, quote := poolBalances(t, db, pool)
	if base != 1_000_000 || quote != 2_000_000 {
		t.Fatalf("got base=%d quote=%d", base, quote)
	}
}

func TestVaultBeforePool(t *testing.T) {
	pool := testPubkey(1)
	baseMint, quoteMint := testPubkey(2), testPubkey(3)
	baseVault, quoteVault := testPubkey(4), testPubkey(5)
	coinCreator := testPubkey(6)
	h, db := testHandler(t)

	// Vault balances arrive first -- held pending, no row exists yet.
	h.OnAccount(fakeAccount{pubkey: baseVault, owner: sgotkn.ProgramID, data: syntheticVaultBody(1_000_000)}, true)
	h.OnAccount(fakeAccount{pubkey: quoteVault, owner: sgotkn.ProgramID, data: syntheticVaultBody(2_000_000)}, true)
	if len(h.mPendingBalance) != 2 {
		t.Fatalf("expected 2 pending balances, got %d", len(h.mPendingBalance))
	}

	// Pool arrives -- should backfill both pending balances immediately.
	h.OnAccount(fakeAccount{
		pubkey: pool, owner: ProgramID,
		data: syntheticPoolBody(baseMint, quoteMint, baseVault, quoteVault, coinCreator),
	}, true)

	if err := h.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	base, quote := poolBalances(t, db, pool)
	if base != 1_000_000 || quote != 2_000_000 {
		t.Fatalf("got base=%d quote=%d", base, quote)
	}
	if len(h.mPendingBalance) != 0 {
		t.Fatalf("expected pending balances drained, got %d left", len(h.mPendingBalance))
	}
}
