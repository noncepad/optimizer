package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// throughputchart.go -- a small panel proving this bot's own low-latency
// data pipeline is fast, not just that the prices it reports are
// correct: a headline "updates/sec" rate plus a lifetime total, backed
// by a compact Braille-resolution line chart of the rate over recent
// history (marketwatchv1.Store.ThroughputHistory). Data source:
// catscope-rust-bot's own low_latency_account_count, diffed against
// real wall-clock time between pushes -- see
// trader::market_stats::updates_per_second's own doc comment.
const (
	throughputChartRowsDefault = 6
	throughputChartRowsMin     = 3
)

var (
	throughputLineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#5B4FE0"))
	throughputBigStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7CE38B"))
	throughputSubStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#8888A0"))
)

// formatCount comma-groups n for readability -- "1048502" -> "1,048,502".
func formatCount(n uint64) string {
	s := strconv.FormatUint(n, 10)
	if len(s) <= 3 {
		return s
	}
	lead := len(s) % 3
	if lead == 0 {
		lead = 3
	}
	var b strings.Builder
	b.WriteString(s[:lead])
	for i := lead; i < len(s); i += 3 {
		b.WriteString(",")
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// renderThroughputPanel renders the headline rate + lifetime total line,
// then a small live line chart of hist (oldest first, UpdatesPerSec
// samples). cols/rows follow the same caller-supplied-real-terminal-size
// convention as renderTAPanel; unlike that panel this one uses a fixed
// default row count rather than a dynamically-computed one (throughput
// is a secondary, at-a-glance panel, not the dashboard's main chart), so
// callers just clamp to throughputChartRowsMin instead of calling a
// taChartHeights-style budget function.
func renderThroughputPanel(hist []float64, currentRate float64, totalUpdates uint64, cols, rows int) string {
	if cols < 20 {
		cols = 20
	}
	if rows < throughputChartRowsMin {
		rows = throughputChartRowsMin
	}

	var b strings.Builder
	b.WriteString(throughputBigStyle.Render(fmt.Sprintf("%s updates/sec", formatCount(uint64(currentRate)))))
	b.WriteString("   ")
	b.WriteString(throughputSubStyle.Render(fmt.Sprintf("%s low-latency account updates processed this session", formatCount(totalUpdates))))
	b.WriteString("\n")

	if len(hist) < 2 {
		b.WriteString(waitingStyle.Render("collecting throughput samples..."))
		return b.String()
	}

	lo, hi := seriesRange(hist)
	if lo < 0 {
		lo = 0
	}
	chart := newBrailleChart(cols, rows)
	layer := chart.newLayer(throughputLineStyle)
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
	chart.plotSeries(layer, hist, toY)
	b.WriteString(chart.render())
	return b.String()
}
