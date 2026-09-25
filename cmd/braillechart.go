package main

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// braillechart.go -- a small multi-series line-chart renderer using the
// Unicode Braille block (U+2800..U+28FF), the standard technique real
// terminal plotting tools (drawille, plotext's braille mode, etc.) use
// for a much smoother line than one-character-per-sample block glyphs:
// each Braille glyph packs a 2(wide)x4(tall) dot grid into one terminal
// cell, so an NxM-cell chart has 2N x 4M effective plot resolution.

// brailleDotBit maps a dot's (row, col) position within one glyph's 2x4
// grid to that dot's bit in the Braille codepoint offset -- the fixed
// bit layout the Unicode Braille block itself defines.
var brailleDotBit = [4][2]int{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

// chartLayer is one colored line series' own dot grid -- kept separate
// per series (rather than one shared grid) so brailleChart.render can
// color each cell by whichever series actually drew into it.
type chartLayer struct {
	dots  []bool
	style lipgloss.Style
}

// brailleChart accumulates one or more layers on dot grids that all
// share the same pixel dimensions and renders them to a single string.
// Layers are drawn in the order they were created; when more than one
// layer has a dot in the same cell, the most-recently-created layer's
// style wins that cell's color -- draw background/reference layers
// (e.g. Bollinger Bands) first and the real price line last so the
// price line's color is never hidden behind a band it happens to touch.
type brailleChart struct {
	cols, rows int
	dotW, dotH int
	layers     []*chartLayer
}

func newBrailleChart(cols, rows int) *brailleChart {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return &brailleChart{cols: cols, rows: rows, dotW: cols * 2, dotH: rows * 4}
}

func (c *brailleChart) newLayer(style lipgloss.Style) *chartLayer {
	l := &chartLayer{dots: make([]bool, c.dotW*c.dotH), style: style}
	c.layers = append(c.layers, l)
	return l
}

func (c *brailleChart) set(l *chartLayer, x, y int) {
	if x < 0 || x >= c.dotW || y < 0 || y >= c.dotH {
		return
	}
	l.dots[y*c.dotW+x] = true
}

// line draws a connected segment between two dot-space points via
// Bresenham's algorithm -- turns a sparse series of samples into a
// continuous-looking line instead of disconnected dots, the same way
// every real line-chart renderer connects consecutive points.
func (c *brailleChart) line(l *chartLayer, x0, y0, x1, y1 int) {
	dx := iabs(x1 - x0)
	dy := -iabs(y1 - y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		c.set(l, x0, y0)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

// plotSeries maps series (one value per x index, NaN for "no data at
// this index") onto the layer's dot grid via toY, connecting consecutive
// non-NaN points and leaving a real gap (not an interpolated line)
// across any NaN run -- so an indicator's own "not enough data yet"
// warm-up period renders as genuinely absent, not a misleading flat
// line back to zero.
func (c *brailleChart) plotSeries(l *chartLayer, series []float64, toY func(v float64) int) {
	n := len(series)
	xAt := func(i int) int {
		if n <= 1 {
			return 0
		}
		return i * (c.dotW - 1) / (n - 1)
	}
	prevX, prevY, havePrev := 0, 0, false
	for i, v := range series {
		if math.IsNaN(v) {
			havePrev = false
			continue
		}
		x, y := xAt(i), toY(v)
		if havePrev {
			c.line(l, prevX, prevY, x, y)
		}
		prevX, prevY, havePrev = x, y, true
	}
}

// horizontalLine draws a full-width reference line (e.g. RSI's 30/70
// zone bounds) at dot-row y.
func (c *brailleChart) horizontalLine(l *chartLayer, y int) {
	for x := 0; x < c.dotW; x++ {
		c.set(l, x, y)
	}
}

func (c *brailleChart) render() string {
	var b strings.Builder
	for r := 0; r < c.rows; r++ {
		for col := 0; col < c.cols; col++ {
			var mask int
			var owner *chartLayer
			for _, l := range c.layers {
				cellHasDot := false
				for dy := 0; dy < 4; dy++ {
					for dx := 0; dx < 2; dx++ {
						x := col*2 + dx
						y := r*4 + dy
						if l.dots[y*c.dotW+x] {
							mask |= brailleDotBit[dy][dx]
							cellHasDot = true
						}
					}
				}
				if cellHasDot {
					owner = l
				}
			}
			if mask == 0 {
				b.WriteByte(' ')
				continue
			}
			ch := string(rune(0x2800 + mask))
			if owner != nil {
				b.WriteString(owner.style.Render(ch))
			} else {
				b.WriteString(ch)
			}
		}
		if r != c.rows-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func iabs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
