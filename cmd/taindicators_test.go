package main

import (
	"math"
	"testing"
)

func TestSMAKnownValues(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5, 6}
	got := sma(values, 3)
	want := []float64{math.NaN(), math.NaN(), 2, 3, 4, 5}
	for i := range want {
		if math.IsNaN(want[i]) {
			if !math.IsNaN(got[i]) {
				t.Errorf("sma[%d] = %v, want NaN", i, got[i])
			}
			continue
		}
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("sma[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSMAShorterThanPeriodIsAllNaN(t *testing.T) {
	got := sma([]float64{1, 2}, 5)
	for i, v := range got {
		if !math.IsNaN(v) {
			t.Errorf("sma[%d] = %v, want NaN (not enough data)", i, v)
		}
	}
}

func TestBollingerBandsOrderingAndMidIsSMA(t *testing.T) {
	values := []float64{10, 12, 9, 14, 11, 13, 8, 15, 10, 12}
	mid, upper, lower := bollingerBands(values, 5, 2.0)
	wantMid := sma(values, 5)
	for i := range values {
		if math.IsNaN(mid[i]) {
			if !math.IsNaN(upper[i]) || !math.IsNaN(lower[i]) {
				t.Errorf("index %d: mid is NaN but a band isn't", i)
			}
			continue
		}
		if math.Abs(mid[i]-wantMid[i]) > 1e-9 {
			t.Errorf("mid[%d] = %v, want sma() = %v", i, mid[i], wantMid[i])
		}
		if !(lower[i] <= mid[i] && mid[i] <= upper[i]) {
			t.Errorf("index %d: bands out of order: lower=%v mid=%v upper=%v", i, lower[i], mid[i], upper[i])
		}
	}
}

func TestBollingerBandsZeroVarianceFloorsToMinWidth(t *testing.T) {
	values := make([]float64, 10)
	for i := range values {
		values[i] = 42.0
	}
	mid, upper, lower := bollingerBands(values, 5, 2.0)
	wantHalfWidth := 42.0 * minBandWidthFrac
	for i := 4; i < len(values); i++ {
		if upper[i] == mid[i] || lower[i] == mid[i] {
			t.Errorf("index %d: expected floored non-zero-width bands for constant price, got mid=%v upper=%v lower=%v", i, mid[i], upper[i], lower[i])
		}
		if math.Abs((upper[i]-mid[i])-wantHalfWidth) > 1e-9 {
			t.Errorf("index %d: upper half-width = %v, want floor %v", i, upper[i]-mid[i], wantHalfWidth)
		}
		if math.Abs((mid[i]-lower[i])-wantHalfWidth) > 1e-9 {
			t.Errorf("index %d: lower half-width = %v, want floor %v", i, mid[i]-lower[i], wantHalfWidth)
		}
	}
}

func TestBollingerBandsRealVolatilityExceedsFloorIsUnaffected(t *testing.T) {
	// A real, sizeable move should produce bands well wider than the
	// floor -- the floor must not override genuine volatility.
	values := []float64{10, 12, 9, 14, 11, 13, 8, 15, 10, 12}
	mid, upper, _ := bollingerBands(values, 5, 2.0)
	for i := 4; i < len(values); i++ {
		floorHalfWidth := math.Abs(mid[i]) * minBandWidthFrac
		if (upper[i] - mid[i]) <= floorHalfWidth {
			t.Errorf("index %d: expected real volatility to exceed the floor, got half-width %v <= floor %v", i, upper[i]-mid[i], floorHalfWidth)
		}
	}
}

func TestRSIAllGainsIsOneHundred(t *testing.T) {
	values := make([]float64, 20)
	for i := range values {
		values[i] = float64(i + 1) // strictly increasing
	}
	got := rsi(values, 14)
	last, ok := lastReal(got)
	if !ok {
		t.Fatal("expected a real RSI value")
	}
	if last != 100 {
		t.Errorf("RSI on an all-gains series = %v, want 100", last)
	}
}

func TestRSIAllLossesIsZero(t *testing.T) {
	values := make([]float64, 20)
	for i := range values {
		values[i] = float64(20 - i) // strictly decreasing
	}
	got := rsi(values, 14)
	last, ok := lastReal(got)
	if !ok {
		t.Fatal("expected a real RSI value")
	}
	if last != 0 {
		t.Errorf("RSI on an all-losses series = %v, want 0", last)
	}
}

func TestRSIConstantPriceIsHundredNotDivideByZeroNaN(t *testing.T) {
	// Wilder's RSI defines loss==0 as RSI=100 (no losses at all, however
	// small the gains) -- a real edge case (a pool whose live quote
	// hasn't moved at all across the whole window) that must not come
	// out as NaN/Inf from a 0/0 division.
	values := make([]float64, 20)
	for i := range values {
		values[i] = 5.0
	}
	got := rsi(values, 14)
	last, ok := lastReal(got)
	if !ok {
		t.Fatal("expected a real RSI value")
	}
	if last != 100 {
		t.Errorf("RSI on a flat series = %v, want 100 (zero losses)", last)
	}
}

func TestRSITooShortIsAllNaN(t *testing.T) {
	got := rsi([]float64{1, 2, 3}, 14)
	for i, v := range got {
		if !math.IsNaN(v) {
			t.Errorf("rsi[%d] = %v, want NaN (not enough data)", i, v)
		}
	}
}

func TestSeriesRangePadsAndHandlesAllNaN(t *testing.T) {
	lo, hi := seriesRange([]float64{1, 5, 3}, []float64{math.NaN(), 10})
	if hi <= 10 || lo >= 1 {
		t.Errorf("expected padding beyond [1,10], got [%v,%v]", lo, hi)
	}

	lo2, hi2 := seriesRange([]float64{math.NaN(), math.NaN()})
	if math.IsNaN(lo2) || math.IsNaN(hi2) || hi2 <= lo2 {
		t.Errorf("expected a safe fallback range for an all-NaN input, got [%v,%v]", lo2, hi2)
	}
}

func TestTAChartHeightsUnknownBudgetUsesDefaults(t *testing.T) {
	price, rsi := taChartHeights(0)
	if price != taChartRowsDefault || rsi != taRSIRowsDefault {
		t.Errorf("taChartHeights(0) = (%d,%d), want defaults (%d,%d)", price, rsi, taChartRowsDefault, taRSIRowsDefault)
	}
	price, rsi = taChartHeights(-5)
	if price != taChartRowsDefault || rsi != taRSIRowsDefault {
		t.Errorf("taChartHeights(-5) = (%d,%d), want defaults (%d,%d)", price, rsi, taChartRowsDefault, taRSIRowsDefault)
	}
}

func TestTAChartHeightsTinyBudgetUsesMinimums(t *testing.T) {
	price, rsi := taChartHeights(1)
	if price != taChartRowsMin || rsi != taRSIRowsMin {
		t.Errorf("taChartHeights(1) = (%d,%d), want minimums (%d,%d)", price, rsi, taChartRowsMin, taRSIRowsMin)
	}
}

func TestTAChartHeightsNeverExceedsBudget(t *testing.T) {
	// The real property that fixes the "top of the screen cut off" bug:
	// for any budget, renderTAPanel's own chrome (8 lines) plus
	// panelStyle's border+padding (4 lines) plus the chart rows
	// themselves must never exceed the budget it was given -- otherwise
	// the panel would still overflow whatever View() computed as
	// "what's actually left."
	for budget := -3; budget <= 60; budget++ {
		price, rsi := taChartHeights(budget)
		if price < taChartRowsMin || rsi < taRSIRowsMin {
			t.Fatalf("budget=%d: got (%d,%d), below minimums (%d,%d)", budget, price, rsi, taChartRowsMin, taRSIRowsMin)
		}
		if budget > 0 && budget >= taChartRowsMin+taRSIRowsMin+8+4 {
			used := price + rsi + 8 + 4
			if used > budget {
				t.Errorf("budget=%d: chart+chrome=%d exceeds budget", budget, used)
			}
		}
	}
}

func TestTAChartHeightsCapsAtDefaultsForLargeBudget(t *testing.T) {
	price, rsi := taChartHeights(1000)
	if price != taChartRowsDefault || rsi != taRSIRowsDefault {
		t.Errorf("taChartHeights(1000) = (%d,%d), want capped at defaults (%d,%d)", price, rsi, taChartRowsDefault, taRSIRowsDefault)
	}
}
