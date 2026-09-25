package marketwatchv1

import (
	"encoding/binary"
	"math"
	"testing"
)

// TestPairIndexCoversEveryPairExactlyOnce mirrors
// catscope-rust-bot's trader::market_stats::tests::pair_index_covers_every_pair_exactly_once
// -- the two implementations must agree, since PairIndex is how this
// side interprets MarketStatsSnapshot.Correlations' fixed layout.
func TestPairIndexCoversEveryPairExactlyOnce(t *testing.T) {
	seen := make([]bool, NPairs)
	for i := 0; i < NSymbols; i++ {
		for j := i + 1; j < NSymbols; j++ {
			k := PairIndex(i, j)
			if k < 0 || k >= NPairs {
				t.Fatalf("PairIndex(%d,%d) = %d out of range [0,%d)", i, j, k, NPairs)
			}
			if seen[k] {
				t.Fatalf("PairIndex(%d,%d) = %d collides with an earlier pair", i, j, k)
			}
			seen[k] = true
		}
	}
	for k, ok := range seen {
		if !ok {
			t.Fatalf("PairIndex left a gap at %d", k)
		}
	}
}

func TestPairIndexPanicsWhenNotOrdered(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected PairIndex(2,1) to panic")
		}
	}()
	PairIndex(2, 1)
}

// TestPairIndexMatchesRustLayout pins PairIndex's first few outputs
// against catscope-rust-bot's own pair_index -- both implement "standard
// row-major upper-triangle packing," but this hardcodes the actual
// expected indices so a future refactor on either side that silently
// changes the packing order gets caught here instead of only showing up
// as scrambled correlations on a live dashboard.
func TestPairIndexMatchesRustLayout(t *testing.T) {
	want := map[[2]int]int{
		{0, 1}: 0, {0, 2}: 1, {0, 3}: 2, {0, 4}: 3, {0, 5}: 4,
		{1, 2}: 5, {1, 3}: 6, {1, 4}: 7, {1, 5}: 8,
		{2, 3}: 9, {2, 4}: 10, {2, 5}: 11,
		{3, 4}: 12, {3, 5}: 13,
		{4, 5}: 14,
	}
	for pair, expected := range want {
		got := PairIndex(pair[0], pair[1])
		if got != expected {
			t.Errorf("PairIndex(%d,%d) = %d, want %d", pair[0], pair[1], got, expected)
		}
	}
}

// TestMarketStatsSnapshotParseRoundTrip builds a wire-format value the
// same way catscope-rust-bot's marketwatchv1::message::CustomMessageOutbound::MarketStats
// serializes it (8-byte LE slot, 8-byte LE f64 updates-per-second,
// 8-byte LE u64 total updates, then NSymbols LE f64 prices, NSymbols LE
// f64 volatilities, NPairs LE f64 correlations) and confirms Parse
// decodes it back exactly -- the real risk in a hand-rolled binary
// protocol shared across two languages, verified here instead of only
// ever being exercised by an actual live bot.
func TestMarketStatsSnapshotParseRoundTrip(t *testing.T) {
	var buf []byte
	putU64 := func(v uint64) {
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], v)
		buf = append(buf, b[:]...)
	}
	putF64 := func(v float64) {
		putU64(math.Float64bits(v))
	}

	const wantSlot = uint64(123456)
	putU64(wantSlot)

	const wantUpdatesPerSec = 12345.5
	putF64(wantUpdatesPerSec)
	const wantTotalUpdates = uint64(987654321)
	putU64(wantTotalUpdates)

	wantPrices := [NSymbols]float64{100.5, 65000.25, 3200.1, 0.55, 600.0, 1.75}
	for _, p := range wantPrices {
		putF64(p)
	}
	wantVols := [NSymbols]float64{0.01, 0.02, 0.015, 0.03, 0.025, 0.04}
	for _, v := range wantVols {
		putF64(v)
	}
	wantCorr := [NPairs]float64{}
	for i := range wantCorr {
		wantCorr[i] = float64(i) / 100.0
	}
	for _, c := range wantCorr {
		putF64(c)
	}

	if len(buf) != marketStatsValueSize {
		t.Fatalf("test built %d bytes, expected %d (fixture/const drifted)", len(buf), marketStatsValueSize)
	}

	var snap MarketStatsSnapshot
	n, err := snap.Parse(buf)
	if err != nil {
		t.Fatalf("Parse failed: %s", err)
	}
	if n != marketStatsValueSize {
		t.Fatalf("Parse consumed %d bytes, want %d", n, marketStatsValueSize)
	}
	if snap.Slot != wantSlot {
		t.Errorf("Slot = %d, want %d", snap.Slot, wantSlot)
	}
	if snap.UpdatesPerSec != wantUpdatesPerSec {
		t.Errorf("UpdatesPerSec = %v, want %v", snap.UpdatesPerSec, wantUpdatesPerSec)
	}
	if snap.TotalUpdates != wantTotalUpdates {
		t.Errorf("TotalUpdates = %d, want %d", snap.TotalUpdates, wantTotalUpdates)
	}
	if snap.Prices != wantPrices {
		t.Errorf("Prices = %v, want %v", snap.Prices, wantPrices)
	}
	if snap.Volatilities != wantVols {
		t.Errorf("Volatilities = %v, want %v", snap.Volatilities, wantVols)
	}
	if snap.Correlations != wantCorr {
		t.Errorf("Correlations = %v, want %v", snap.Correlations, wantCorr)
	}
}

func TestMarketStatsSnapshotParseTooShort(t *testing.T) {
	var snap MarketStatsSnapshot
	_, err := snap.Parse(make([]byte, marketStatsValueSize-1))
	if err == nil {
		t.Fatal("expected an error for a too-short value")
	}
}

func TestCorrelationSymmetricAndSelfIsZero(t *testing.T) {
	var snap MarketStatsSnapshot
	snap.Correlations[PairIndex(1, 3)] = 0.42
	if got := snap.Correlation(1, 3); got != 0.42 {
		t.Errorf("Correlation(1,3) = %v, want 0.42", got)
	}
	if got := snap.Correlation(3, 1); got != 0.42 {
		t.Errorf("Correlation(3,1) = %v, want 0.42 (must be order-independent)", got)
	}
	if got := snap.Correlation(2, 2); got != 0.0 {
		t.Errorf("Correlation(2,2) = %v, want 0.0", got)
	}
}
