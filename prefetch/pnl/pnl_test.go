package pnl_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"git.noncepad.com/pkg/optimizer/prefetch/pnl"
	sgo "github.com/gagliardetto/solana-go"
	_ "modernc.org/sqlite"
)

func f64(v float64) *float64 { return &v }
func u8(v uint8) *uint8      { return &v }

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "prefetch.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %s", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(pnl.Schema); err != nil {
		t.Fatalf("migrate: %s", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestRecordIfChangedSkipsUnchangedBalance(t *testing.T) {
	db := openTestDB(t)
	wallet := sgo.NewWallet().PublicKey()
	mint := sgo.NewWallet().PublicKey()
	base := time.Now().Add(-time.Hour)

	changed, err := pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: base, Wallet: wallet, Mint: mint, Balance: 1000, Decimals: u8(6), USDPrice: f64(1),
	})
	if err != nil {
		t.Fatalf("first record: %s", err)
	}
	if !changed {
		t.Fatal("first record: want changed=true (no prior row)")
	}

	changed, err = pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: base.Add(time.Minute), Wallet: wallet, Mint: mint, Balance: 1000, Decimals: u8(6), USDPrice: f64(1.5),
	})
	if err != nil {
		t.Fatalf("second record: %s", err)
	}
	if changed {
		t.Fatal("second record: want changed=false (same balance)")
	}

	changed, err = pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: base.Add(2 * time.Minute), Wallet: wallet, Mint: mint, Balance: 2000, Decimals: u8(6), USDPrice: f64(1.5),
	})
	if err != nil {
		t.Fatalf("third record: %s", err)
	}
	if !changed {
		t.Fatal("third record: want changed=true (balance moved)")
	}

	var rowCount int
	if err := db.QueryRow(`SELECT count(*) FROM pnl_position_snapshot WHERE wallet = ? AND mint = ?`, wallet.Bytes(), mint.Bytes()).Scan(&rowCount); err != nil {
		t.Fatalf("count rows: %s", err)
	}
	if rowCount != 2 {
		t.Fatalf("row count = %d, want 2 (unchanged balance shouldn't have inserted a row)", rowCount)
	}
}

func TestPositionsPnL(t *testing.T) {
	db := openTestDB(t)
	wallet := sgo.NewWallet().PublicKey()
	mint := sgo.NewWallet().PublicKey()
	base := time.Now().Add(-time.Hour)

	// starting snapshot: 2 whole tokens (9 decimals) at $10 -> $20
	if _, err := pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: base, Wallet: wallet, Mint: mint, Balance: 2_000_000_000, Decimals: u8(9), USDPrice: f64(10),
	}); err != nil {
		t.Fatalf("insert start: %s", err)
	}
	// a snapshot in between shouldn't affect start/latest picking
	if _, err := pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: base.Add(20 * time.Minute), Wallet: wallet, Mint: mint, Balance: 2_500_000_000, Decimals: u8(9), USDPrice: f64(11),
	}); err != nil {
		t.Fatalf("insert middle: %s", err)
	}
	// latest snapshot: 3 whole tokens at $12 -> $36
	if _, err := pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: base.Add(40 * time.Minute), Wallet: wallet, Mint: mint, Balance: 3_000_000_000, Decimals: u8(9), USDPrice: f64(12),
	}); err != nil {
		t.Fatalf("insert latest: %s", err)
	}

	positions, err := pnl.Positions(db, wallet)
	if err != nil {
		t.Fatalf("Positions: %s", err)
	}
	if len(positions) != 1 {
		t.Fatalf("len(positions) = %d, want 1", len(positions))
	}
	p := positions[0]
	if !p.Mint.Equals(mint) {
		t.Errorf("mint = %s, want %s", p.Mint, mint)
	}
	if p.StartUSDValue == nil || *p.StartUSDValue != 20 {
		t.Errorf("StartUSDValue = %v, want 20", p.StartUSDValue)
	}
	if p.LatestUSDValue == nil || *p.LatestUSDValue != 36 {
		t.Errorf("LatestUSDValue = %v, want 36", p.LatestUSDValue)
	}
	if p.DeltaUSDValue == nil || *p.DeltaUSDValue != 16 {
		t.Errorf("DeltaUSDValue = %v, want 16", p.DeltaUSDValue)
	}
}

func TestPositionsBetween(t *testing.T) {
	db := openTestDB(t)
	wallet := sgo.NewWallet().PublicKey()
	mint := sgo.NewWallet().PublicKey()
	base := time.Now().Add(-4 * time.Hour)

	// before the window -- shouldn't be picked as the window's start value
	if _, err := pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: base, Wallet: wallet, Mint: mint, Balance: 1_000_000_000, Decimals: u8(9), USDPrice: f64(5),
	}); err != nil {
		t.Fatalf("insert before-window: %s", err)
	}
	// at the window's start: 2 whole tokens at $10 -> $20
	if _, err := pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: base.Add(time.Hour), Wallet: wallet, Mint: mint, Balance: 2_000_000_000, Decimals: u8(9), USDPrice: f64(10),
	}); err != nil {
		t.Fatalf("insert at-start: %s", err)
	}
	// at the window's end: 3 whole tokens at $12 -> $36
	if _, err := pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: base.Add(2 * time.Hour), Wallet: wallet, Mint: mint, Balance: 3_000_000_000, Decimals: u8(9), USDPrice: f64(12),
	}); err != nil {
		t.Fatalf("insert at-end: %s", err)
	}
	// after the window -- shouldn't be picked as the window's end value
	if _, err := pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: base.Add(3 * time.Hour), Wallet: wallet, Mint: mint, Balance: 9_000_000_000, Decimals: u8(9), USDPrice: f64(99),
	}); err != nil {
		t.Fatalf("insert after-window: %s", err)
	}

	positions, err := pnl.PositionsBetween(db, wallet, base.Add(time.Hour), base.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("PositionsBetween: %s", err)
	}
	if len(positions) != 1 {
		t.Fatalf("len(positions) = %d, want 1", len(positions))
	}
	p := positions[0]
	if p.StartUSDValue == nil || *p.StartUSDValue != 20 {
		t.Errorf("StartUSDValue = %v, want 20", p.StartUSDValue)
	}
	if p.LatestUSDValue == nil || *p.LatestUSDValue != 36 {
		t.Errorf("LatestUSDValue = %v, want 36", p.LatestUSDValue)
	}
	if p.DeltaUSDValue == nil || *p.DeltaUSDValue != 16 {
		t.Errorf("DeltaUSDValue = %v, want 16", p.DeltaUSDValue)
	}
}

func TestPositionsBetweenSkipsMintWithNoDataBeforeStart(t *testing.T) {
	db := openTestDB(t)
	wallet := sgo.NewWallet().PublicKey()
	mint := sgo.NewWallet().PublicKey()
	base := time.Now().Add(-time.Hour)

	// only recorded after the window's start -- the position didn't exist yet
	if _, err := pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: base.Add(30 * time.Minute), Wallet: wallet, Mint: mint, Balance: 1000, Decimals: u8(6), USDPrice: f64(1),
	}); err != nil {
		t.Fatalf("insert: %s", err)
	}

	positions, err := pnl.PositionsBetween(db, wallet, base, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("PositionsBetween: %s", err)
	}
	if len(positions) != 0 {
		t.Fatalf("len(positions) = %d, want 0 (no snapshot at or before start)", len(positions))
	}
}

func TestPositionsUnknownPriceStaysNil(t *testing.T) {
	db := openTestDB(t)
	wallet := sgo.NewWallet().PublicKey()
	mint := sgo.NewWallet().PublicKey()

	if _, err := pnl.RecordIfChanged(db, pnl.Snapshot{
		Time: time.Now(), Wallet: wallet, Mint: mint, Balance: 1000, // no Decimals/USDPrice yet
	}); err != nil {
		t.Fatalf("insert: %s", err)
	}
	positions, err := pnl.Positions(db, wallet)
	if err != nil {
		t.Fatalf("Positions: %s", err)
	}
	if len(positions) != 1 {
		t.Fatalf("len(positions) = %d, want 1", len(positions))
	}
	if positions[0].DeltaUSDValue != nil {
		t.Errorf("DeltaUSDValue = %v, want nil (price unknown)", *positions[0].DeltaUSDValue)
	}
}

func TestWallets(t *testing.T) {
	db := openTestDB(t)
	walletA := sgo.NewWallet().PublicKey()
	walletB := sgo.NewWallet().PublicKey()
	mint := sgo.NewWallet().PublicKey()

	for _, s := range []pnl.Snapshot{
		{Time: time.Now(), Wallet: walletA, Mint: mint, Balance: 1},
		{Time: time.Now(), Wallet: walletB, Mint: mint, Balance: 2},
	} {
		if _, err := pnl.RecordIfChanged(db, s); err != nil {
			t.Fatalf("insert: %s", err)
		}
	}

	wallets, err := pnl.Wallets(db)
	if err != nil {
		t.Fatalf("Wallets: %s", err)
	}
	if len(wallets) != 2 {
		t.Fatalf("len(wallets) = %d, want 2", len(wallets))
	}
}
