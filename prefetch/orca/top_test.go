package orca

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	sgo "github.com/gagliardetto/solana-go"
	_ "modernc.org/sqlite"
)

func testPubkey(b byte) sgo.PublicKey {
	var buf [32]byte
	for i := range buf {
		buf[i] = b
	}
	return sgo.PublicKeyFromBytes(buf[:])
}

// realPrefetchDBPath returns the path to the operator's actual prefetch
// database, if one has already been downloaded.
func realPrefetchDBPath() string {
	return filepath.Join(os.Getenv("HOME"), ".optimizer", "prefetch.db")
}

// openTestDB opens the real prefetch.db if one already exists (so the test
// also exercises TopPools against real, large-scale data), otherwise falls
// back to a synthetic in-memory database with just this package's schema.
func openTestDB(t *testing.T) (db *sql.DB, isReal bool) {
	t.Helper()
	if _, err := os.Stat(realPrefetchDBPath()); err == nil {
		db, err = sql.Open("sqlite", realPrefetchDBPath())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db, true
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(Schema); err != nil {
		t.Fatal(err)
	}
	return db, false
}

// insertPoolsForTest seeds pools directly for tests -- production code no
// longer bulk-inserts (event.go persists each pool immediately as it's
// discovered), so this replaces the old exported insertPools helper,
// test-scoped since nothing else needs it anymore.
func insertPoolsForTest(db *sql.DB, pools []*Whirlpool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO orca_whirlpool_pool
		(pubkey, whirlpools_config, mint_a, mint_b, vault_a, vault_b,
		 sqrt_price_lo, sqrt_price_hi, liquidity_lo, liquidity_hi,
		 tick_current_index, tick_spacing, fee_rate)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, p := range pools {
		if _, err = stmt.Exec(
			p.Pubkey[:], p.WhirlpoolsConfig[:], p.TokenMintA[:], p.TokenMintB[:],
			p.VaultA[:], p.VaultB[:],
			int64(p.SqrtPriceLo), int64(p.SqrtPriceHi),
			int64(p.LiquidityLo), int64(p.LiquidityHi),
			p.TickCurrentIndex, p.TickSpacing, p.FeeRate,
		); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return err
		}
	}
	if err = stmt.Close(); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func testWhirlpool(id byte, liquidityLo, liquidityHi uint64) *Whirlpool {
	return &Whirlpool{
		Pubkey:           testPubkey(id),
		WhirlpoolsConfig: testPubkey(id + 40),
		TokenMintA:       testPubkey(id + 100),
		TokenMintB:       testPubkey(id + 101),
		VaultA:           testPubkey(id + 102),
		VaultB:           testPubkey(id + 103),
		LiquidityLo:      liquidityLo,
		LiquidityHi:      liquidityHi,
	}
}

func TestTopPools(t *testing.T) {
	db, isReal := openTestDB(t)
	if isReal {
		top, err := TopPools(db, 10)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("using real prefetch.db at %s: got %d pools", realPrefetchDBPath(), len(top))
		for i := 1; i < len(top); i++ {
			prev := liquidityFloat(top[i-1].LiquidityLo, top[i-1].LiquidityHi)
			cur := liquidityFloat(top[i].LiquidityLo, top[i].LiquidityHi)
			if prev < cur {
				t.Fatalf("pool %d (liquidity %f) ranked above pool %d (liquidity %f)", i-1, prev, i, cur)
			}
		}
		return
	}

	small := testWhirlpool(1, 200, 0)
	best := testWhirlpool(2, 10_000, 0)
	tiny := testWhirlpool(3, 2, 0)
	// A nonzero high word makes this pool's true u128 liquidity vastly
	// larger than any of the low-word-only pools above, even though its
	// low word alone (100) is the smallest — this is what distinguishes
	// TopPools from a naive sort on liquidity_lo.
	huge := testWhirlpool(4, 100, 1)

	if err := insertPoolsForTest(db, []*Whirlpool{small, best, tiny, huge}); err != nil {
		t.Fatal(err)
	}

	top, err := TopPools(db, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 {
		t.Fatalf("expected 2 pools, got %d", len(top))
	}
	if !top[0].Pubkey.Equals(huge.Pubkey) {
		t.Fatalf("expected the pool with a nonzero high word first, got %s", top[0].Pubkey)
	}
	if !top[1].Pubkey.Equals(best.Pubkey) {
		t.Fatalf("expected second-highest pool second, got %s", top[1].Pubkey)
	}
}
