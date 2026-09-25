package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// marketwatchchart.go -- the SOL/USDC technical-analysis panel: a
// Braille-resolution price chart with an SMA(20) overlay and
// Bollinger Bands(20, 2σ), plus an RSI(14) oscillator sub-panel below
// it. Built entirely from Store.PriceHistory's already-collected price
// series (taindicators.go's pure math) -- no new data source, no
// changes to the Rust bot or wire protocol.
const (
	// Shared by the SMA overlay and Bollinger Bands -- one period, one
	// computation (bollingerBands' own mid band doubles as the SMA
	// line), not two independently-configurable periods that could
	// silently drift apart.
	smaPeriod   = 20
	bbNumStdDev = 2.0
	rsiPeriod   = 14

	// Used both as renderTAPanel's own fallback (before the terminal's
	// real size is known -- m.height starts at 0 until bubbletea's
	// first WindowSizeMsg arrives) and as taChartHeights' ceiling once
	// it is known, so a very tall terminal doesn't stretch the chart
	// pointlessly far.
	taChartRowsDefault = 12
	taRSIRowsDefault   = 4
	taChartRowsMin     = 4
	taRSIRowsMin       = 2
	// Below this many real price points, SMA/Bollinger/RSI all have too
	// short a warm-up to say anything real yet -- shown as a plain
	// waiting message instead of a chart with mostly-NaN indicators.
	taMinPoints = 5
)

// taChartHeights picks the price-chart and RSI-panel row counts that fit
// renderTAPanel's own non-chart lines ("SYMBOL high $X", the blank line
// after the price chart, "low $X", the legend, a blank line, "RSI(N)",
// the blank line after the RSI chart, and the RSI summary line -- 8
// lines total) plus panelStyle's border+padding (4 lines: 2 border, 2
// padding) inside budget rows. Falls back to the fixed defaults when
// budget is unknown (<=0, i.e. before the terminal's real size is known)
// or too small to fit even the minimums -- same look as before this
// sizing existed, for that first frame or a genuinely tiny terminal.
func taChartHeights(budget int) (priceRows, rsiRows int) {
	const chrome = 8 + 4
	if budget <= 0 {
		return taChartRowsDefault, taRSIRowsDefault
	}
	remaining := budget - chrome
	if remaining < taChartRowsMin+taRSIRowsMin {
		return taChartRowsMin, taRSIRowsMin
	}
	priceRows = remaining * 3 / 4
	if priceRows > taChartRowsDefault {
		priceRows = taChartRowsDefault
	}
	if priceRows < taChartRowsMin {
		priceRows = taChartRowsMin
	}
	rsiRows = remaining - priceRows
	if rsiRows > taRSIRowsDefault {
		rsiRows = taRSIRowsDefault
	}
	if rsiRows < taRSIRowsMin {
		rsiRows = taRSIRowsMin
	}
	return priceRows, rsiRows
}

var (
	priceLineStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#7CE38B"))
	smaLineStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#F0B429"))
	bandLineStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#5B5B75"))
	rsiLineStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#6FA8F5"))
	rsiZoneStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#3A3A55"))
	rsiOverboughtText = lipgloss.NewStyle().Foreground(lipgloss.Color("#E36B6B"))
	rsiOversoldText   = lipgloss.NewStyle().Foreground(lipgloss.Color("#7CE38B"))
	rsiNeutralText    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6FA8F5"))
	axisLabelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#8888A0"))
)

// renderTAPanel renders the full SOL/USDC technical-analysis panel:
// price chart with SMA/Bollinger overlay, then RSI sub-panel, then a
// one-line summary. cols sizes the chart to the caller's available
// width; priceRows/rsiRows size the two charts' heights -- both driven
// by the caller's real terminal dimensions (see
// marketWatchModel.View/taChartHeights), not a fixed constant, so the
// panel never renders taller than the actual terminal.
func renderTAPanel(symbol string, prices []float64, cols, priceRows, rsiRows int) string {
	if len(prices) < taMinPoints {
		return waitingStyle.Render(fmt.Sprintf("watching %s -- not enough price history yet for a technical chart", symbol))
	}
	if cols < 20 {
		cols = 20
	}
	if priceRows < taChartRowsMin {
		priceRows = taChartRowsMin
	}
	if rsiRows < taRSIRowsMin {
		rsiRows = taRSIRowsMin
	}

	// bollingerBands' own mid band *is* the SMA(smaPeriod) overlay --
	// one call computes both instead of calling sma() a second time.
	smaSeries, upper, lower := bollingerBands(prices, smaPeriod, bbNumStdDev)
	rsiSeries := rsi(prices, rsiPeriod)

	lo, hi := seriesRange(prices, upper, lower)

	chart := newBrailleChart(cols, priceRows)
	bandLayer := chart.newLayer(bandLineStyle)
	smaLayer := chart.newLayer(smaLineStyle)
	priceLayer := chart.newLayer(priceLineStyle)

	toY := func(v float64) int {
		frac := (v - lo) / (hi - lo)
		y := int((1 - frac) * float64(chart.dotH-1))
		if y < 0 {
			y = 0
		}
		if y >= chart.dotH {
			y = chart.dotH - 1
		}
		return y
	}
	chart.plotSeries(bandLayer, upper, toY)
	chart.plotSeries(bandLayer, lower, toY)
	chart.plotSeries(smaLayer, smaSeries, toY)
	chart.plotSeries(priceLayer, prices, toY)

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s\n", symbol, axisLabelStyle.Render("high"), formatPrice(hi))
	b.WriteString(chart.render())
	b.WriteString("\n")
	fmt.Fprintf(&b, "%s %s\n", axisLabelStyle.Render("low "), formatPrice(lo))
	b.WriteString(priceLineStyle.Render("── price  "))
	b.WriteString(smaLineStyle.Render(fmt.Sprintf("── SMA(%d)  ", smaPeriod)))
	b.WriteString(bandLineStyle.Render(fmt.Sprintf("·· Bollinger(%d,%.0fσ)", smaPeriod, bbNumStdDev)))
	b.WriteString("\n\n")

	b.WriteString(sectionHeaderStyle.Render(fmt.Sprintf("RSI(%d)", rsiPeriod)))
	b.WriteString("\n")
	b.WriteString(renderRSIPanel(rsiSeries, cols, rsiRows))
	b.WriteString("\n")

	if last, ok := lastReal(rsiSeries); ok {
		label, style := "neutral", rsiNeutralText
		switch {
		case last >= 70:
			label, style = "overbought", rsiOverboughtText
		case last <= 30:
			label, style = "oversold", rsiOversoldText
		}
		b.WriteString(style.Render(fmt.Sprintf("RSI = %.1f (%s)", last, label)))
		b.WriteString("\n")
	}

	return b.String()
}

func renderRSIPanel(rsiSeries []float64, cols, rows int) string {
	chart := newBrailleChart(cols, rows)
	zoneLayer := chart.newLayer(rsiZoneStyle)
	lineLayer := chart.newLayer(rsiLineStyle)

	toY := func(v float64) int {
		frac := v / 100.0
		y := int((1 - frac) * float64(chart.dotH-1))
		if y < 0 {
			y = 0
		}
		if y >= chart.dotH {
			y = chart.dotH - 1
		}
		return y
	}
	// Standard 30/70 overbought/oversold reference lines.
	chart.horizontalLine(zoneLayer, toY(70))
	chart.horizontalLine(zoneLayer, toY(30))
	chart.plotSeries(lineLayer, rsiSeries, toY)
	return chart.render()
}

// seriesRange finds the min/max across every non-NaN value in every
// given series (padded 5% so a plotted line never touches the very top
// or bottom row) -- price and its Bollinger Bands share one scale so
// the bands' real distance from price is visually meaningful, not
// independently rescaled per series.
func seriesRange(series ...[]float64) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, s := range series {
		for _, v := range s {
			if math.IsNaN(v) {
				continue
			}
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
	}
	if math.IsInf(lo, 1) {
		lo, hi = 0, 1
	}
	if hi <= lo {
		hi = lo + 1
	}
	pad := (hi - lo) * 0.05
	return lo - pad, hi + pad
}

func lastReal(series []float64) (float64, bool) {
	for i := len(series) - 1; i >= 0; i-- {
		if !math.IsNaN(series[i]) {
			return series[i], true
		}
	}
	return 0, false
}
