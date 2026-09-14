package perpfunding

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dbFp := filepath.Join(t.TempDir(), "prefetch.db")
	db, err := sql.Open("sqlite", dbFp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if _, err = db.Exec(Schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSetAndGetTargetAllocation(t *testing.T) {
	db := testDB(t)

	if _, ok, err := GetTargetAllocation(db, "SOL"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("expected no row before any Set call")
	}

	if err := SetTargetAllocation(db, "SOL", 0.30); err != nil {
		t.Fatal(err)
	}
	got, ok, err := GetTargetAllocation(db, "SOL")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected a row after Set")
	}
	if got != 0.30 {
		t.Fatalf("got %v, want 0.30", got)
	}
}

func TestSetTargetAllocationOverwritesPreviousValue(t *testing.T) {
	db := testDB(t)

	if err := SetTargetAllocation(db, "SOL", 0.30); err != nil {
		t.Fatal(err)
	}
	if err := SetTargetAllocation(db, "SOL", 0.45); err != nil {
		t.Fatal(err)
	}
	got, _, err := GetTargetAllocation(db, "SOL")
	if err != nil {
		t.Fatal(err)
	}
	if got != 0.45 {
		t.Fatalf("got %v, want 0.45 (latest write should win)", got)
	}
}

func TestGetAllTargetAllocations(t *testing.T) {
	db := testDB(t)

	want := map[string]float64{"SOL": 0.30, "BTC": 0.10, "ETH": 0.05}
	for symbol, allocationPct := range want {
		if err := SetTargetAllocation(db, symbol, allocationPct); err != nil {
			t.Fatal(err)
		}
	}
	got, err := GetAllTargetAllocations(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for symbol, allocationPct := range want {
		if got[symbol] != allocationPct {
			t.Fatalf("symbol %s: got %v, want %v", symbol, got[symbol], allocationPct)
		}
	}
}
