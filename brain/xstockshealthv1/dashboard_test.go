package xstockshealthv1

// dashboard_test.go exists because boardPageTemplate.Execute failures
// (a typo'd field name, a template/pageData mismatch) only surface at
// render time -- html/template.Parse (what template.Must checks at
// package init) does not validate field references against a struct.
// go build/go vet catch neither. This exercises buildPageData +
// Execute directly against realistic and edge-case inputs so a broken
// template fails `go test`, not a live demo.

import (
	"bytes"
	"testing"
)

func sampleEntries(n int) []HealthEntry {
	out := make([]HealthEntry, n)
	for i := range out {
		out[i] = HealthEntry{
			AccountID:        uint64(i + 1),
			CollateralUSD:    100.0 + float64(i),
			DebtUSD:          50.0,
			HealthFactor:     1.0 + float64(i)*0.01,
			CollateralTicker: "TSLAx",
			DebtTicker:       "USDC",
		}
	}
	return out
}

func sampleLiquidations(n int) []LiquidationEvent {
	out := make([]LiquidationEvent, n)
	for i := range out {
		out[i] = LiquidationEvent{
			Obligation:         uint64(i + 1),
			RepayTicker:        "USDC",
			WithdrawTicker:     "TSLAx",
			RepayUSD:           2.0,
			HealthFactorBefore: 0.95,
			EstimatedBonusUSD:  0.14,
			Slot:               uint64(1000 + i),
			SentOK:             i%2 == 0,
		}
	}
	return out
}

func TestBoardPageTemplateRenders(t *testing.T) {
	cases := []struct {
		name       string
		total      uint64
		entries    []HealthEntry
		updated    bool
		liqs       []LiquidationEvent
		ticks      []DexTick
		moving     []tickerActivity
		overview   MarketOverview
		overviewOK bool
		boardPage  int
		liqPage    int
	}{
		{name: "nothing received yet"},
		{
			name:    "empty but updated",
			total:   0,
			entries: nil,
			updated: true,
			overview: MarketOverview{
				RiskTierCounts: [4]uint64{0, 0, 0, 0},
			},
			overviewOK: true,
			boardPage:  1,
			liqPage:    1,
		},
		{
			name:    "typical page 1",
			total:   7000,
			entries: sampleEntries(110),
			updated: true,
			liqs:    sampleLiquidations(3),
			ticks: []DexTick{
				{Ticker: "TSLAx", PriceUSD: 420.5, Up: true, Slot: 1},
				{Ticker: "SPYx", PriceUSD: 771.7, Up: false, Slot: 2},
			},
			moving: []tickerActivity{{Ticker: "TSLAx", Count: 5}, {Ticker: "SPYx", Count: 2}},
			overview: MarketOverview{
				ReservesMonitored: 13,
				PoolsMonitored:    10,
				RiskTierCounts:    [4]uint64{3, 12, 40, 55},
				ArbDetectedUSD:    12.34,
				LiquidationPnLUSD: 0,
				ArbDetectionCount: 9,
				ArbByTicker: []ArbTickerAmount{
					{Ticker: "TSLAx", USD: 8.1},
					{Ticker: "SPYx", USD: 4.24},
				},
			},
			overviewOK: true,
			boardPage:  1,
			liqPage:    1,
		},
		{
			name:       "out-of-range and negative pages clamp",
			total:      500,
			entries:    sampleEntries(110),
			updated:    true,
			liqs:       sampleLiquidations(60),
			overview:   MarketOverview{RiskTierCounts: [4]uint64{1, 2, 3, 4}},
			overviewOK: true,
			boardPage:  9999,
			liqPage:    -5,
		},
		{
			name:       "zero total (all-zero risk tiers)",
			total:      0,
			entries:    nil,
			updated:    true,
			overview:   MarketOverview{RiskTierCounts: [4]uint64{0, 0, 0, 0}},
			overviewOK: true,
			boardPage:  1,
			liqPage:    1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := buildPageData(c.total, c.entries, c.updated, c.liqs, c.ticks, c.moving, c.overview, c.overviewOK, c.boardPage, c.liqPage)
			var buf bytes.Buffer
			if err := boardPageTemplate.Execute(&buf, data); err != nil {
				t.Fatalf("template execute failed: %s", err)
			}
			if buf.Len() == 0 {
				t.Fatal("template rendered empty output")
			}
		})
	}
}
