package marketwatchv1

import "testing"

// TestUpdateSkipsUnpricedSymbolsInHistory pins the real, live-observed
// bug store.go's update doc comment describes: a symbol's very first
// snapshot(s), pushed before its pool subscriptions finish warming up,
// must never let the wire protocol's 0.0 "not priced yet" sentinel (see
// MarketStatsSnapshot.Prices's own doc comment) into PriceHistory --
// that 0.0 would otherwise sit in the bounded window for the rest of a
// session, corrupting the sparkline and the TA panel's Bollinger-derived
// "low" axis label.
func TestUpdateSkipsUnpricedSymbolsInHistory(t *testing.T) {
	s := newStore()

	var unpriced MarketStatsSnapshot // every Prices[i] defaults to 0.0
	s.update(unpriced)

	var priced MarketStatsSnapshot
	priced.Prices[0] = 120.5
	priced.Volatilities[0] = 0.01
	s.update(priced)

	hist := s.PriceHistory(0)
	if len(hist) != 1 {
		t.Fatalf("expected the unpriced tick to be skipped entirely, got %d history points: %v", len(hist), hist)
	}
	if hist[0] != 120.5 {
		t.Errorf("expected the sole history point to be the real price 120.5, got %v", hist[0])
	}

	volHist := s.VolatilityHistory(0)
	if len(volHist) != 1 {
		t.Fatalf("expected the unpriced tick's volatility to be skipped too, got %d points: %v", len(volHist), volHist)
	}
}

// TestUpdateTracksEachSymbolIndependently confirms one symbol pricing
// later than another (real, live-observed: pool subscriptions warm up
// at different rates per symbol) doesn't block or corrupt an
// already-priced symbol's own history.
func TestUpdateTracksEachSymbolIndependently(t *testing.T) {
	s := newStore()

	var first MarketStatsSnapshot
	first.Prices[0] = 120.0 // symbol 0 priced already
	// symbol 1 stays at the 0.0 sentinel -- not priced yet
	s.update(first)

	var second MarketStatsSnapshot
	second.Prices[0] = 121.0
	second.Prices[1] = 0.55 // symbol 1 now priced too
	s.update(second)

	hist0 := s.PriceHistory(0)
	if len(hist0) != 2 {
		t.Fatalf("symbol 0: expected 2 real history points, got %d: %v", len(hist0), hist0)
	}
	hist1 := s.PriceHistory(1)
	if len(hist1) != 1 || hist1[0] != 0.55 {
		t.Fatalf("symbol 1: expected exactly 1 real history point (0.55), got %v", hist1)
	}
}

// TestUpdateStillRecordsThroughputAndLatestEvenWhenNoSymbolPricedYet
// confirms the unpriced-symbol skip is scoped to per-symbol price/vol
// history only -- Latest() and the throughput history (which don't have
// a "not yet priced" concept at all) must still update every tick.
func TestUpdateStillRecordsThroughputAndLatestEvenWhenNoSymbolPricedYet(t *testing.T) {
	s := newStore()

	var snap MarketStatsSnapshot
	snap.UpdatesPerSec = 1234.5
	s.update(snap)

	_, ok := s.Latest()
	if !ok {
		t.Fatal("expected Latest() to report a snapshot has arrived")
	}
	th := s.ThroughputHistory()
	if len(th) != 1 || th[0] != 1234.5 {
		t.Fatalf("expected throughput history to record every tick regardless of pricing, got %v", th)
	}
}
