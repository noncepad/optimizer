package phoenix

import (
	"context"
	"database/sql"
	"testing"

	"git.noncepad.com/pkg/bot/state"
	_ "modernc.org/sqlite"
)

func TestFixedMarketsAreWellFormed(t *testing.T) {
	if len(fixedMarkets) == 0 {
		t.Fatal("expected a non-empty fixed market list")
	}
	seenAssetID := make(map[uint32]bool)
	seenMarket := make(map[string]bool)
	for _, m := range fixedMarkets {
		if m.Symbol == "" {
			t.Fatalf("market %+v has an empty symbol", m)
		}
		if len(m.Symbol) > 16 {
			t.Fatalf("market %+v symbol longer than 16 bytes", m)
		}
		if m.MarketAccount.IsZero() {
			t.Fatalf("market %s has a zero market_account", m.Symbol)
		}
		if seenAssetID[m.AssetID] {
			t.Fatalf("duplicate asset_id %d", m.AssetID)
		}
		seenAssetID[m.AssetID] = true
		key := m.MarketAccount.String()
		if seenMarket[key] {
			t.Fatalf("duplicate market_account %s", key)
		}
		seenMarket[key] = true
	}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(Schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestInsertAndLoadMarketsRoundTrip(t *testing.T) {
	db := testDB(t)

	if err := insertMarkets(db, fixedMarkets); err != nil {
		t.Fatal(err)
	}
	n, err := marketCount(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(fixedMarkets) {
		t.Fatalf("expected %d markets persisted, got %d", len(fixedMarkets), n)
	}

	loaded, err := loadMarkets(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(fixedMarkets) {
		t.Fatalf("expected %d markets loaded, got %d", len(fixedMarkets), len(loaded))
	}
	mBySymbol := make(map[string]*Market, len(loaded))
	for _, m := range loaded {
		mBySymbol[m.Symbol] = m
	}
	sol, present := mBySymbol["SOL"]
	if !present {
		t.Fatal("expected SOL market to round-trip")
	}
	if sol.AssetID != 0 || sol.MarketAccount != fixedMarkets[0].MarketAccount {
		t.Fatalf("SOL market mismatch after round-trip: %+v", sol)
	}
}

func TestCreateSkipsReinsertUnlessForced(t *testing.T) {
	db := testDB(t)

	p, err := Create(context.Background(), state.Client{}, db, 128, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Markets) != len(fixedMarkets) {
		t.Fatalf("expected %d markets, got %d", len(fixedMarkets), len(p.Markets))
	}

	// Repeat call without force should just load back from db, not error
	// or duplicate rows.
	p2, err := Create(context.Background(), state.Client{}, db, 128, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Markets) != len(fixedMarkets) {
		t.Fatalf("expected %d markets on reload, got %d", len(fixedMarkets), len(p2.Markets))
	}
	n, err := marketCount(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(fixedMarkets) {
		t.Fatalf("expected no duplicate rows, got count %d", n)
	}
}
