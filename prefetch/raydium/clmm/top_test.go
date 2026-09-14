package clmm

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

func insertTestConfig(t *testing.T, db *sql.DB, id byte) sgo.PublicKey {
	t.Helper()
	pubkey := testPubkey(id)
	owner := testPubkey(id + 50)
	fundOwner := testPubkey(id + 60)
	_, err := db.Exec(
		`INSERT INTO raydium_clmm_config
			(pubkey, bump, config_index, owner, protocol_fee_rate, trade_fee_rate,
			 tick_spacing, fund_fee_rate, fund_owner)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		pubkey[:], 255, 0, owner[:], 120000, 2500, 60, 40000, fundOwner[:],
	)
	if err != nil {
		t.Fatal(err)
	}
	return pubkey
}

func insertTestPool(t *testing.T, db *sql.DB, id byte, ammConfig sgo.PublicKey, token0Balance, token1Balance uint64) sgo.PublicKey {
	t.Helper()
	pubkey := testPubkey(id)
	owner := testPubkey(id + 40)
	mint0 := testPubkey(id + 100)
	mint1 := testPubkey(id + 101)
	vault0 := testPubkey(id + 102)
	vault1 := testPubkey(id + 103)
	observationKey := testPubkey(id + 104)
	_, err := db.Exec(
		`INSERT INTO raydium_clmm_pool
			(pubkey, amm_config, owner, token_mint0, token_mint1, token_vault0, token_vault1,
			 observation_key, mint_decimals0, mint_decimals1, tick_spacing,
			 liquidity_lo, liquidity_hi, sqrt_price_lo, sqrt_price_hi, tick_current,
			 protocol_fees0, protocol_fees1, status, open_time, token0_balance, token1_balance)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		pubkey[:], ammConfig[:], owner[:], mint0[:], mint1[:], vault0[:], vault1[:],
		observationKey[:], 6, 9, 60,
		0, 0, 0, 0, 0,
		0, 0, 0, 0, int64(token0Balance), int64(token1Balance),
	)
	if err != nil {
		t.Fatal(err)
	}
	return pubkey
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
			prev := top[i-1].Token0Balance + top[i-1].Token1Balance
			cur := top[i].Token0Balance + top[i].Token1Balance
			if prev < cur {
				t.Fatalf("pool %d (liquidity %d) ranked above pool %d (liquidity %d)", i-1, prev, i, cur)
			}
		}
		return
	}

	cfg := insertTestConfig(t, db, 1)
	insertTestPool(t, db, 2, cfg, 100, 100)           // liquidity 200
	best := insertTestPool(t, db, 3, cfg, 5000, 5000) // liquidity 10000
	insertTestPool(t, db, 4, cfg, 1, 1)               // liquidity 2
	second := insertTestPool(t, db, 5, cfg, 300, 300) // liquidity 600

	top, err := TopPools(db, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 {
		t.Fatalf("expected 2 pools, got %d", len(top))
	}
	if !top[0].Pubkey.Equals(best) {
		t.Fatalf("expected highest-liquidity pool first, got %s", top[0].Pubkey)
	}
	if !top[1].Pubkey.Equals(second) {
		t.Fatalf("expected second-highest pool second, got %s", top[1].Pubkey)
	}
}
