package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestBrailleChartEmptyRendersBlank(t *testing.T) {
	c := newBrailleChart(4, 2)
	c.newLayer(lipgloss.NewStyle())
	got := c.render()
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 rows, got %d: %q", len(lines), got)
	}
	for i, line := range lines {
		if line != "    " {
			t.Errorf("row %d = %q, want 4 blank spaces (no dots set)", i, line)
		}
	}
}

func TestBrailleChartDimensions(t *testing.T) {
	c := newBrailleChart(10, 3)
	if c.dotW != 20 || c.dotH != 12 {
		t.Fatalf("dotW/dotH = %d/%d, want 20/12 (2x cols, 4x rows)", c.dotW, c.dotH)
	}
}

func TestBrailleChartSinglePixelSetsExpectedGlyph(t *testing.T) {
	c := newBrailleChart(1, 1)
	l := c.newLayer(lipgloss.NewStyle())
	// top-left dot only -> braille codepoint 0x2800 + 0x01
	c.set(l, 0, 0)
	got := c.render()
	want := string(rune(0x2801))
	if !strings.Contains(got, want) {
		t.Errorf("render() = %q, want it to contain %q", got, want)
	}
}

func TestBrailleChartLineConnectsEndpoints(t *testing.T) {
	c := newBrailleChart(1, 1)
	l := c.newLayer(lipgloss.NewStyle())
	c.line(l, 0, 0, 1, 3)
	if !l.dots[0*c.dotW+0] {
		t.Error("expected the start point (0,0) to be set")
	}
	if !l.dots[3*c.dotW+1] {
		t.Error("expected the end point (1,3) to be set")
	}
}

func TestBrailleChartOutOfBoundsSetIsIgnored(t *testing.T) {
	c := newBrailleChart(2, 2)
	l := c.newLayer(lipgloss.NewStyle())
	// Should not panic on any of these.
	c.set(l, -1, 0)
	c.set(l, 0, -1)
	c.set(l, c.dotW, 0)
	c.set(l, 0, c.dotH)
	for i, d := range l.dots {
		if d {
			t.Errorf("dot %d unexpectedly set from an out-of-bounds call", i)
		}
	}
}

func TestBrailleChartPlotSeriesSkipsNaNGaps(t *testing.T) {
	c := newBrailleChart(4, 1)
	l := c.newLayer(lipgloss.NewStyle())
	series := []float64{0, nan(), 1}
	c.plotSeries(l, series, func(v float64) int {
		if v == 0 {
			return 0
		}
		return c.dotH - 1
	})
	// Just confirm it doesn't panic and produces some non-empty render --
	// the real point of this test is that a NaN in the middle of a
	// series doesn't crash the line-drawing loop.
	if c.render() == "" {
		t.Error("expected a non-empty render")
	}
}

func nan() float64 {
	var f float64
	return f / f // NaN without importing math just for this one test helper
}
