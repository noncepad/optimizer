package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.noncepad.com/pkg/optimizer/store"
)

// newTestStore opens a real, local, temp-file SQLite database with the
// full prefetch.db schema applied (store.Open runs every migration) --
// same helper shape as go-wiki/client/hedgefund's own sql_workflow_test.go
// uses for exactly the same reason: pnl.PositionsBetween is real query
// logic worth exercising against a real database, not a mock.
func newTestStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func uint8Ptr(v uint8) *uint8 { return &v }

func TestFormatRawAmount(t *testing.T) {
	cases := []struct {
		raw      uint64
		decimals *uint8
		want     string
	}{
		{1_000_000, uint8Ptr(6), "1"},
		{1_500_000, uint8Ptr(6), "1.5"},
		{0, uint8Ptr(6), "0"},
		{123, nil, "123 (raw)"},
	}
	for _, c := range cases {
		got := formatRawAmount(c.raw, c.decimals)
		if got != c.want {
			t.Errorf("formatRawAmount(%d, %v) = %q, want %q", c.raw, c.decimals, got, c.want)
		}
	}
}

// TestGetPnlOverDaysImpl seeds two real pnl_position_snapshot rows for
// the same wallet+mint -- one comfortably before the 7-day window
// (picked as the "start" snapshot) and one comfortably inside it but
// before "now" (picked as "end") -- and confirms the formatted output
// reports the right before/after USD values and delta. Timestamps are
// computed relative to time.Now() at test run time with a wide enough
// margin (10 days / 1 minute) that ordinary test execution latency can't
// push a row across a window boundary.
func TestGetPnlOverDaysImpl(t *testing.T) {
	ctx := context.Background()
	db := newTestStore(t)

	mint := make([]byte, 32)
	for i := range mint {
		mint[i] = byte(i + 1)
	}
	now := time.Now()
	startTime := now.AddDate(0, 0, -10).Unix()
	endTime := now.Add(-1 * time.Minute).Unix()

	if _, err := db.Raw().Exec(
		`INSERT INTO pnl_position_snapshot (time, wallet, mint, balance, decimals, usd_price) VALUES (?, ?, ?, ?, ?, ?)`,
		startTime, testOwner.Bytes(), mint, 1_000_000, 6, 1.00,
	); err != nil {
		t.Fatalf("seed start snapshot: %v", err)
	}
	if _, err := db.Raw().Exec(
		`INSERT INTO pnl_position_snapshot (time, wallet, mint, balance, decimals, usd_price) VALUES (?, ?, ?, ?, ?, ?)`,
		endTime, testOwner.Bytes(), mint, 2_000_000, 6, 1.00,
	); err != nil {
		t.Fatalf("seed end snapshot: %v", err)
	}

	out, err := getPnlOverDaysImpl(ctx, db, testOwner, 7)
	if err != nil {
		t.Fatalf("getPnlOverDaysImpl: %v", err)
	}
	if !strings.Contains(out, "$1.00") {
		t.Errorf("expected the start USD value $1.00 in output, got %q", out)
	}
	if !strings.Contains(out, "$2.00") {
		t.Errorf("expected the end USD value $2.00 in output, got %q", out)
	}
	if !strings.Contains(out, "delta +1.00") {
		t.Errorf("expected a per-mint delta of +1.00 in output, got %q", out)
	}
	if !strings.Contains(out, "TOTAL: $1.00 -> $2.00  delta +1.00") {
		t.Errorf("expected a matching TOTAL line, got %q", out)
	}
}

// TestGetPnlOverDaysImplNoPositions confirms an empty database produces
// the explicit "no positions recorded" message rather than an error or
// an empty string that could be mistaken for "zero change".
func TestGetPnlOverDaysImplNoPositions(t *testing.T) {
	ctx := context.Background()
	db := newTestStore(t)

	out, err := getPnlOverDaysImpl(ctx, db, testOwner, 7)
	if err != nil {
		t.Fatalf("getPnlOverDaysImpl: %v", err)
	}
	if !strings.Contains(out, "no positions recorded") {
		t.Errorf("expected a 'no positions recorded' message, got %q", out)
	}
}

// TestGetPnlOverDaysImplInvalidDaysAgo confirms zero/negative windows
// are rejected rather than silently computing a backwards or empty
// window.
func TestGetPnlOverDaysImplInvalidDaysAgo(t *testing.T) {
	ctx := context.Background()
	db := newTestStore(t)

	if _, err := getPnlOverDaysImpl(ctx, db, testOwner, 0); err == nil {
		t.Error("expected an error for days_ago=0")
	}
	if _, err := getPnlOverDaysImpl(ctx, db, testOwner, -1); err == nil {
		t.Error("expected an error for negative days_ago")
	}
}

// TestPnlToolSet confirms pnlTools produces exactly the one tool
// pnlPersona's own text describes.
func TestPnlToolSet(t *testing.T) {
	ctx := context.Background()
	db := newTestStore(t)

	tools, err := pnlTools(db, testOwner)
	if err != nil {
		t.Fatalf("pnlTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	info, err := tools[0].Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "get_pnl_over_days" {
		t.Errorf("expected tool name get_pnl_over_days, got %q", info.Name)
	}
}

func TestNewPnlAgentBuilds(t *testing.T) {
	ctx := context.Background()
	db := newTestStore(t)

	agent, err := NewPnlAgent(ctx, &fakeToolCallingModel{response: "ok"}, db, testOwner)
	if err != nil {
		t.Fatalf("NewPnlAgent: %v", err)
	}
	if agent == nil {
		t.Fatal("expected a non-nil agent")
	}
}

func TestRunPnlReportRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestStore(t)
	cm := &fakeToolCallingModel{response: "value grew 12% this week"}

	agent, err := NewPnlAgent(ctx, cm, db, testOwner)
	if err != nil {
		t.Fatalf("NewPnlAgent: %v", err)
	}
	report, err := RunPnlReport(ctx, agent, "how did we do this week?")
	if err != nil {
		t.Fatalf("RunPnlReport: %v", err)
	}
	if report.Summary != "value grew 12% this week" {
		t.Errorf("unexpected summary: %q", report.Summary)
	}
}

func TestRunPnlReportError(t *testing.T) {
	ctx := context.Background()
	db := newTestStore(t)
	cm := &fakeToolCallingModel{err: errors.New("simulated model failure")}

	agent, err := NewPnlAgent(ctx, cm, db, testOwner)
	if err != nil {
		t.Fatalf("NewPnlAgent: %v", err)
	}
	if _, err := RunPnlReport(ctx, agent, "anything"); err == nil {
		t.Fatal("expected an error from RunPnlReport")
	}
}
