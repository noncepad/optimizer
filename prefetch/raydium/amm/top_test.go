package amm

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

func insertTestPool(t *testing.T, db *sql.DB, id byte, coinBalance, pcBalance uint64) sgo.PublicKey {
	t.Helper()
	pubkey := testPubkey(id)
	coinVault := testPubkey(id + 100)
	pcVault := testPubkey(id + 200)
	coinMint := testPubkey(id + 150)
	pcMint := testPubkey(id + 250)
	_, err := db.Exec(
		`INSERT INTO raydium_amm_pool (pubkey, coin_vault, pc_vault, coin_mint, pc_mint, coin_balance, pc_balance) VALUES (?,?,?,?,?,?,?)`,
		pubkey[:], coinVault[:], pcVault[:], coinMint[:], pcMint[:], int64(coinBalance), int64(pcBalance),
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
			prev := top[i-1].CoinBalance + top[i-1].PcBalance
			cur := top[i].CoinBalance + top[i].PcBalance
			if prev < cur {
				t.Fatalf("pool %d (liquidity %d) ranked above pool %d (liquidity %d)", i-1, prev, i, cur)
			}
		}
		return
	}

	insertTestPool(t, db, 1, 100, 100)           // liquidity 200
	best := insertTestPool(t, db, 2, 5000, 5000) // liquidity 10000
	insertTestPool(t, db, 3, 1, 1)               // liquidity 2
	second := insertTestPool(t, db, 4, 300, 300) // liquidity 600

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
