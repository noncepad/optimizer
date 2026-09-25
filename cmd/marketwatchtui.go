package main

import (
	"fmt"
	"strings"
	"time"

	"git.noncepad.com/pkg/optimizer/brain/marketwatchv1"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// marketWatchModel is the bubbletea model for `optimizer marketwatch`'s
// live dashboard. It never mutates marketwatchv1.Store -- it's a pure
// reader, polling Latest()/PriceHistory()/VolatilityHistory() on a fixed
// tick and rendering whatever it finds. There is deliberately no error
// state to show beyond "waiting for data": Store.Latest's own (zero,
// false) return until the bot's first push already covers "nothing yet,"
// and every other failure mode (bot upload failing, handshake timing
// out) surfaces as a real process error from marketwatch.go's own Run,
// not something this view needs to duplicate.
type marketWatchModel struct {
	store         *marketwatchv1.Store
	width, height int
}

func newMarketWatchModel(store *marketwatchv1.Store) marketWatchModel {
	return marketWatchModel{store: store}
}

// chartCols sizes the technical-analysis chart to the real terminal
// width once known (tea.WindowSizeMsg), leaving room for panelStyle's
// own border+padding (2 border cols + 4 padding cols) plus a small
// margin; falls back to a reasonable fixed width before the first
// WindowSizeMsg arrives, and never goes below a floor where the chart
// would be unreadable.
func (m marketWatchModel) chartCols() int {
	const fallback = 56
	const minCols = 20
	const reserved = 10
	if m.width <= 0 {
		return fallback
	}
	cols := m.width - reserved
	if cols < minCols {
		return minCols
	}
	return cols
}

// symbolIndex finds symbol's position in marketwatchv1.TrackedSymbols,
// or 0 (SOL's own real position today, and a harmless default even if
// the tracked set ever changes and no longer contains symbol) if not
// found.
func symbolIndex(symbol string) int {
	for i, s := range marketwatchv1.TrackedSymbols {
		if s == symbol {
			return i
		}
	}
	return 0
}

type tickMsg time.Time

const tickInterval = 500 * time.Millisecond

func tickCmd() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m marketWatchModel) Init() tea.Cmd {
	return tickCmd()
}

func (m marketWatchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
		return m, nil
	case tickMsg:
		return m, tickCmd()
	}
	return m, nil
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#F5F5F5")).
			Background(lipgloss.Color("#5B4FE0")).
			Padding(0, 2)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8888A0")).
			Italic(true)

	symbolStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7CE38B")).
			Width(5)

	priceStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F5F5F5")).
			Width(14).
			Align(lipgloss.Right)

	volStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F0B429")).
			Width(10).
			Align(lipgloss.Right)

	sectionHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#5B4FE0")).
				MarginTop(1)

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B6B80")).
			MarginTop(1)

	waitingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F0B429")).
			MarginTop(1)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#3A3A55")).
			Padding(1, 2)
)

func (m marketWatchModel) View() string {
	snap, ok := m.store.Latest()

	var b strings.Builder
	b.WriteString(titleStyle.Render("MARKETWATCH — live volatility & correlation"))
	b.WriteString("\n")

	if !ok {
		b.WriteString(subtitleStyle.Render("watching real pool prices across SOL/JUP/JTO/BONK/WIF/RAY -- no trading, read-only"))
		b.WriteString("\n")
		b.WriteString(waitingStyle.Render("waiting for the bot's first snapshot... (upload + handshake can take a minute)"))
		b.WriteString("\n")
		b.WriteString(footerStyle.Render("press q to quit"))
		return b.String()
	}

	b.WriteString(subtitleStyle.Render(fmt.Sprintf("slot %d -- no trading, read-only", snap.Slot)))
	b.WriteString("\n\n")

	var rows strings.Builder
	for i, sym := range marketwatchv1.TrackedSymbols {
		price := snap.Prices[i]
		vol := snap.Volatilities[i]
		hist := m.store.PriceHistory(i)
		spark := sparkline(hist, 24)
		trend := trendColor(hist)
		priceStr := "  n/a"
		if price > 0 {
			priceStr = formatPrice(price)
		}
		volStr := "  n/a"
		if price > 0 {
			volStr = fmt.Sprintf("%.2f%%", vol*100)
		}
		rows.WriteString(symbolStyle.Render(sym))
		rows.WriteString(priceStyle.Render(priceStr))
		rows.WriteString("  ")
		rows.WriteString(volStyle.Render(volStr))
		rows.WriteString("  ")
		rows.WriteString(lipgloss.NewStyle().Foreground(trend).Render(spark))
		rows.WriteString("\n")
	}
	b.WriteString(panelStyle.Render(rows.String()))

	b.WriteString(sectionHeaderStyle.Render("live data throughput"))
	b.WriteString("\n")
	b.WriteString(panelStyle.Render(renderThroughputPanel(
		m.store.ThroughputHistory(), snap.UpdatesPerSec, snap.TotalUpdates,
		m.chartCols(), throughputChartRowsDefault,
	)))

	b.WriteString(sectionHeaderStyle.Render("correlation matrix"))
	b.WriteString("\n")
	b.WriteString(panelStyle.Render(renderCorrelationMatrix(snap)))

	// Real bug this fixes (live-reported, 2026-09-25): the TA panel
	// below used to render at a fixed height regardless of the actual
	// terminal size, so on any terminal shorter than everything above
	// plus that fixed height, the top of the screen (the title bar)
	// scrolled off before the user ever saw it. Measure what's actually
	// been rendered so far for real, instead of hand-counting lines
	// (which would silently drift out of sync the next time this layout
	// changes), and size the TA panel to whatever's left.
	taHeader := "SOL/USDC — technical analysis"
	footerLine := "press q to quit"
	topLines := strings.Count(b.String(), "\n") + 1
	budget := m.height - topLines - 2 // -2 reserves taHeader's own line + footerLine's own line
	priceRows, rsiRows := taChartHeights(budget)

	solIdx := symbolIndex("SOL")
	b.WriteString(sectionHeaderStyle.Render(taHeader))
	b.WriteString("\n")
	b.WriteString(panelStyle.Render(renderTAPanel("SOL/USDC", m.store.PriceHistory(solIdx), m.chartCols(), priceRows, rsiRows)))

	b.WriteString(footerStyle.Render(footerLine))
	return b.String()
}

// formatPrice picks a decimal precision that keeps small (e.g. BONK at
// ~$0.00002) and larger (e.g. SOL/JUP/JTO/WIF/RAY, roughly $0.1-$200)
// quotes both readable, instead of one fixed precision that's wrong for
// most of the tracked symbols -- BONK in particular needs real precision
// well past 5 decimals or it would just render as "$0.00000".
func formatPrice(p float64) string {
	switch {
	case p >= 1000:
		return fmt.Sprintf("$%.2f", p)
	case p >= 1:
		return fmt.Sprintf("$%.3f", p)
	case p >= 0.01:
		return fmt.Sprintf("$%.5f", p)
	default:
		return fmt.Sprintf("$%.8f", p)
	}
}

var sparkChars = []rune("▁▂▃▄▅▆▇█")

// sparkline renders values (oldest first) as a block-character sparkline
// exactly width runes wide, right-aligned (padded with spaces on the
// left for a short history) -- the same min-max-normalize-to-8-levels
// technique behind every terminal sparkline tool.
func sparkline(values []float64, width int) string {
	if len(values) == 0 {
		return strings.Repeat(" ", width)
	}
	v := values
	if len(v) > width {
		v = v[len(v)-width:]
	}
	minV, maxV := v[0], v[0]
	for _, x := range v {
		if x < minV {
			minV = x
		}
		if x > maxV {
			maxV = x
		}
	}
	span := maxV - minV

	var b strings.Builder
	for i := 0; i < width-len(v); i++ {
		b.WriteRune(' ')
	}
	for _, x := range v {
		idx := len(sparkChars) / 2
		if span > 0 {
			idx = int((x - minV) / span * float64(len(sparkChars)-1))
			if idx < 0 {
				idx = 0
			}
			if idx >= len(sparkChars) {
				idx = len(sparkChars) - 1
			}
		}
		b.WriteRune(sparkChars[idx])
	}
	return b.String()
}

// trendColor is green if the history ends higher than it started, red if
// lower, neutral gray if flat or there's not enough history to tell yet.
func trendColor(hist []float64) lipgloss.Color {
	if len(hist) < 2 {
		return lipgloss.Color("#8888A0")
	}
	first, last := hist[0], hist[len(hist)-1]
	switch {
	case last > first:
		return lipgloss.Color("#7CE38B")
	case last < first:
		return lipgloss.Color("#E36B6B")
	default:
		return lipgloss.Color("#8888A0")
	}
}

// correlationColor interpolates red (-1) -> neutral gray (0) -> green
// (+1), the standard correlation-heatmap convention, clamped defensively
// even though MarketStatsSnapshot.Correlation already keeps values in
// [-1,1].
func correlationColor(c float64) lipgloss.Color {
	if c > 1 {
		c = 1
	}
	if c < -1 {
		c = -1
	}
	lerp := func(a, b int, t float64) int {
		return int(float64(a) + t*float64(b-a))
	}
	var r, g, bl int
	const (
		neutralR, neutralG, neutralB = 60, 60, 75
		posR, posG, posB             = 46, 160, 90
		negR, negG, negB             = 190, 60, 60
	)
	if c >= 0 {
		r, g, bl = lerp(neutralR, posR, c), lerp(neutralG, posG, c), lerp(neutralB, posB, c)
	} else {
		t := -c
		r, g, bl = lerp(neutralR, negR, t), lerp(neutralG, negG, t), lerp(neutralB, negB, t)
	}
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, bl))
}

// renderCorrelationMatrix draws the full NxN symbol correlation grid
// (self-correlation shown as a fixed "1.00", not computed data) with
// each off-diagonal cell's background colored by correlationColor.
func renderCorrelationMatrix(snap marketwatchv1.MarketStatsSnapshot) string {
	const cellWidth = 7
	headerCell := lipgloss.NewStyle().Width(cellWidth).Align(lipgloss.Center).Bold(true)
	labelCell := lipgloss.NewStyle().Width(5).Bold(true).Foreground(lipgloss.Color("#7CE38B"))
	blankLabel := lipgloss.NewStyle().Width(5)

	var b strings.Builder
	b.WriteString(blankLabel.Render(""))
	for _, sym := range marketwatchv1.TrackedSymbols {
		b.WriteString(headerCell.Render(sym))
	}
	b.WriteString("\n")

	for i, rowSym := range marketwatchv1.TrackedSymbols {
		b.WriteString(labelCell.Render(rowSym))
		for j := range marketwatchv1.TrackedSymbols {
			var val float64
			var cell string
			if i == j {
				val = 1.0
				cell = " 1.00 "
			} else {
				val = snap.Correlation(i, j)
				cell = fmt.Sprintf("%+.2f", val)
				for len(cell) < cellWidth {
					cell = " " + cell
				}
			}
			style := lipgloss.NewStyle().
				Width(cellWidth).
				Align(lipgloss.Center).
				Foreground(lipgloss.Color("#F5F5F5")).
				Background(correlationColor(val))
			b.WriteString(style.Render(cell))
		}
		b.WriteString("\n")
	}
	return b.String()
}
