package pumpfun

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
	// get their own private :memory: database (same reason store.Open sets
	// this on the real prefetch.db connection).
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(Schema); err != nil {
		t.Fatal(err)
	}
	h := &eventHandler{
		logger:   slog.Default(),
		db:       db,
		mPending: make(map[sgo.PublicKey]*pendingCurve),
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	h.tx = tx
	return h, db
}

func syntheticBondingCurveBody(complete bool, creator, quoteMint sgo.PublicKey) []byte {
	data := make([]byte, bondingCurveMinLen)
	copy(data[0:8], bondingCurveDiscriminator[:])
	if complete {
		data[offBcComplete] = 1
	}
	copy(data[offBcCreator:], creator[:])
	copy(data[offBcQuoteMint:], quoteMint[:])
	return data
}

func syntheticTokenAccountBody(mint, owner sgo.PublicKey) []byte {
	data := make([]byte, 165)
	copy(data[0:32], mint[:])
	copy(data[32:64], owner[:])
	// state = initialized, so bin's decoder doesn't choke on an
	// AccountState enum value it treats as meaningful.
	data[108] = 1
	return data
}

// queryer is satisfied by both *sql.DB and *sql.Tx -- countCurves must
// query through whichever one currently holds prefetch.db's single
// connection (SetMaxOpenConns(1)), or it deadlocks waiting for a
// connection an open, uncommitted tx is still holding.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func countCurves(t *testing.T, q queryer) int {
	t.Helper()
	var n int
	if err := q.QueryRow(`SELECT COUNT(*) FROM pumpfun_bonding_curve`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTryFinalizeMergesEitherDeliveryOrder(t *testing.T) {
	mint := testPubkey(1)
	bondingCurve, err := bondingCurveAddress(mint)
	if err != nil {
		t.Fatal(err)
	}
	creator := testPubkey(7)
	quoteMint := testPubkey(8)

	t.Run("curve then token account", func(t *testing.T) {
		h, db := testHandler(t)
		h.pending(bondingCurve).curveData = syntheticBondingCurveBody(false, creator, quoteMint)
		h.tryFinalize(bondingCurve)
		if countCurves(t, h.tx) != 0 {
			t.Fatal("should not finalize with only curveData set")
		}
		h.pending(bondingCurve).mint = mint
		h.tryFinalize(bondingCurve)
		if err = h.tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if n := countCurves(t, db); n != 1 {
			t.Fatalf("expected 1 curve persisted, got %d", n)
		}
	})

	t.Run("token account then curve", func(t *testing.T) {
		h, db := testHandler(t)
		h.pending(bondingCurve).mint = mint
		h.tryFinalize(bondingCurve)
		if countCurves(t, h.tx) != 0 {
			t.Fatal("should not finalize with only mint set")
		}
		h.pending(bondingCurve).curveData = syntheticBondingCurveBody(false, creator, quoteMint)
		h.tryFinalize(bondingCurve)
		if err = h.tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if n := countCurves(t, db); n != 1 {
			t.Fatalf("expected 1 curve persisted, got %d", n)
		}
	})
}

func TestTryFinalizeRejectsMintCurveMismatch(t *testing.T) {
	mint := testPubkey(1)
	wrongBondingCurve := testPubkey(99) // not PDA("bonding-curve", mint)
	h, db := testHandler(t)
	h.pending(wrongBondingCurve).curveData = syntheticBondingCurveBody(false, testPubkey(7), testPubkey(8))
	h.pending(wrongBondingCurve).mint = mint
	h.tryFinalize(wrongBondingCurve)
	if err := h.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countCurves(t, db); n != 0 {
		t.Fatalf("expected mismatched pair to be rejected, got %d rows", n)
	}
}

func TestOnAccountRoutesByOwner(t *testing.T) {
	mint := testPubkey(1)
	bondingCurve, err := bondingCurveAddress(mint)
	if err != nil {
		t.Fatal(err)
	}
	h, db := testHandler(t)

	h.OnAccount(fakeAccount{
		pubkey: bondingCurve,
		owner:  ProgramID,
		data:   syntheticBondingCurveBody(false, testPubkey(7), testPubkey(8)),
	}, true)
	h.OnAccount(fakeAccount{
		pubkey: testPubkey(50), // the associated_bonding_curve's own address, irrelevant to routing
		owner:  sgotkn.ProgramID,
		data:   syntheticTokenAccountBody(mint, bondingCurve),
	}, true)

	if err = h.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countCurves(t, db); n != 1 {
		t.Fatalf("expected 1 curve persisted via OnAccount, got %d", n)
	}
}
