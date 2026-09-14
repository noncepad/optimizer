package lstyield_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	lstyield "git.noncepad.com/pkg/optimizer/prefetch/lst-yield"
	sgo "github.com/gagliardetto/solana-go"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "prefetch.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %s", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(lstyield.Schema); err != nil {
		t.Fatalf("migrate: %s", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestEstimateAPYNoHistoryReturnsNil(t *testing.T) {
	db := openTestDB(t)
	mint := sgo.NewWallet().PublicKey()

	apy, err := lstyield.EstimateAPY(db, mint, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("EstimateAPY: %s", err)
	}
	if apy != nil {
		t.Fatalf("apy = %v, want nil (no snapshots yet)", *apy)
	}
}

func TestEstimateAPYSingleSnapshotReturnsNil(t *testing.T) {
	db := openTestDB(t)
	mint := sgo.NewWallet().PublicKey()
	if err := lstyield.RecordSnapshot(db, mint, time.Now(), 1.10); err != nil {
		t.Fatalf("RecordSnapshot: %s", err)
	}

	apy, err := lstyield.EstimateAPY(db, mint, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("EstimateAPY: %s", err)
	}
	if apy != nil {
		t.Fatalf("apy = %v, want nil (only one snapshot -- no rate of change)", *apy)
	}
}

func TestEstimateAPYTooCloseInTimeReturnsNil(t *testing.T) {
	db := openTestDB(t)
	mint := sgo.NewWallet().PublicKey()
	base := time.Now().Add(-30 * time.Minute)
	if err := lstyield.RecordSnapshot(db, mint, base, 1.10); err != nil {
		t.Fatalf("RecordSnapshot: %s", err)
	}
	// only 30m apart -- below minEstimateSpan (1h)
	if err := lstyield.RecordSnapshot(db, mint, base.Add(30*time.Minute), 1.1001); err != nil {
		t.Fatalf("RecordSnapshot: %s", err)
	}

	apy, err := lstyield.EstimateAPY(db, mint, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("EstimateAPY: %s", err)
	}
	if apy != nil {
		t.Fatalf("apy = %v, want nil (snapshots too close together in time)", *apy)
	}
}

func TestEstimateAPYAnnualizesRealSpread(t *testing.T) {
	db := openTestDB(t)
	mint := sgo.NewWallet().PublicKey()
	base := time.Now().Add(-30 * 24 * time.Hour)

	// 1.000 -> 1.006 over exactly 30 days is ~7.3% annualized simple growth
	// (0.006 * 365/30), well within a plausible real LST staking yield.
	if err := lstyield.RecordSnapshot(db, mint, base, 1.000); err != nil {
		t.Fatalf("RecordSnapshot start: %s", err)
	}
	if err := lstyield.RecordSnapshot(db, mint, base.Add(15*24*time.Hour), 1.003); err != nil {
		t.Fatalf("RecordSnapshot middle: %s", err)
	}
	if err := lstyield.RecordSnapshot(db, mint, base.Add(30*24*time.Hour), 1.006); err != nil {
		t.Fatalf("RecordSnapshot end: %s", err)
	}

	apy, err := lstyield.EstimateAPY(db, mint, 60*24*time.Hour)
	if err != nil {
		t.Fatalf("EstimateAPY: %s", err)
	}
	if apy == nil {
		t.Fatal("apy = nil, want a real estimate")
	}
	want := (1.006/1.000 - 1.0) * (365.0 / 30.0)
	if diff := *apy - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("apy = %v, want %v (first-vs-latest snapshot only, middle one ignored)", *apy, want)
	}
}

func TestEstimateAPYWindowExcludesOldSnapshots(t *testing.T) {
	db := openTestDB(t)
	mint := sgo.NewWallet().PublicKey()
	base := time.Now().Add(-60 * 24 * time.Hour)

	// This old snapshot is outside a 7d window and must be ignored.
	if err := lstyield.RecordSnapshot(db, mint, base, 0.500); err != nil {
		t.Fatalf("RecordSnapshot old: %s", err)
	}
	if err := lstyield.RecordSnapshot(db, mint, time.Now().Add(-6*24*time.Hour), 1.000); err != nil {
		t.Fatalf("RecordSnapshot recent start: %s", err)
	}
	if err := lstyield.RecordSnapshot(db, mint, time.Now(), 1.002); err != nil {
		t.Fatalf("RecordSnapshot recent end: %s", err)
	}

	apy, err := lstyield.EstimateAPY(db, mint, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("EstimateAPY: %s", err)
	}
	if apy == nil {
		t.Fatal("apy = nil, want a real estimate from the two recent snapshots")
	}
	if *apy < 0 {
		t.Errorf("apy = %v, want positive (rate went up 1.000->1.002, old 0.500 row must be excluded)", *apy)
	}
}

func TestTrackedLSTsLabelFallsBackToMintPrefix(t *testing.T) {
	unlabeled := lstyield.TrackedLST{Mint: sgo.NewWallet().PublicKey()}
	if unlabeled.Label() == "" {
		t.Fatal("Label() = \"\", want a non-empty fallback for an unlabeled mint")
	}
	named := lstyield.TrackedLST{Symbol: "jitoSOL", Mint: sgo.NewWallet().PublicKey()}
	if named.Label() != "jitoSOL" {
		t.Errorf("Label() = %q, want %q", named.Label(), "jitoSOL")
	}
}
