package main

import (
	"strings"
	"testing"
)

func TestFormatCountCommaGroupsLargeNumbers(t *testing.T) {
	cases := map[uint64]string{
		0:          "0",
		7:          "7",
		999:        "999",
		1000:       "1,000",
		12345:      "12,345",
		1048502:    "1,048,502",
		987654321:  "987,654,321",
		1000000000: "1,000,000,000",
	}
	for n, want := range cases {
		if got := formatCount(n); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestRenderThroughputPanelShowsRateAndTotal(t *testing.T) {
	hist := []float64{100, 200, 300, 250, 400}
	out := renderThroughputPanel(hist, 12345, 987654321, 60, throughputChartRowsDefault)
	if !strings.Contains(out, "12,345 updates/sec") {
		t.Errorf("expected rate in output, got: %s", out)
	}
	if !strings.Contains(out, "987,654,321") {
		t.Errorf("expected total updates in output, got: %s", out)
	}
}

func TestRenderThroughputPanelWithSparseHistoryShowsWaitingMessage(t *testing.T) {
	out := renderThroughputPanel([]float64{42}, 0, 0, 60, throughputChartRowsDefault)
	if !strings.Contains(out, "collecting throughput samples") {
		t.Errorf("expected waiting message for <2 history points, got: %s", out)
	}
}

func TestRenderThroughputPanelClampsBelowMinimums(t *testing.T) {
	hist := []float64{100, 200, 300}
	// Must not panic on a too-small cols/rows -- clamped internally.
	_ = renderThroughputPanel(hist, 50, 100, 0, 0)
}
