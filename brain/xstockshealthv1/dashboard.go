package xstockshealthv1

// dashboard.go serves the current health board on a local HTTP page --
// "run it locally for now" instead of deciding between extending
// catscope-htmx or a real standalone deployment (see HACKATHON_PLAN.md
// §5/§10 item 4, edge-generator repo). Polling-refreshed plain HTML, no
// JS/SSE -- the simplest thing that shows the board is genuinely live.

import (
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// boardStore holds the most recently received health board. Safe for
// concurrent access: loopInstance's stdout reader is the only writer, the
// HTTP handler (one goroutine per request) is the only reader.
type boardStore struct {
	mu      sync.RWMutex
	total   uint64
	entries []HealthEntry
	// updated is false until the bot's first real HealthBoard message
	// arrives -- distinguishes "genuinely zero obligations tracked" from
	// "hasn't sent anything yet" for the page.
	updated bool
}

func (b *boardStore) set(total uint64, entries []HealthEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total = total
	b.entries = entries
	b.updated = true
}

func (b *boardStore) get() (total uint64, entries []HealthEntry, updated bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.total, b.entries, b.updated
}

// maxLiquidationEvents caps how many past liquidation attempts the page
// keeps -- a real, ongoing bot could otherwise grow this list forever;
// the newest ones are what matter for watching it live.
const maxLiquidationEvents = 50

// liquidationStore holds every real liquidation attempt this bot has
// made, newest first. Same concurrency contract as boardStore.
type liquidationStore struct {
	mu     sync.RWMutex
	events []LiquidationEvent
}

func (l *liquidationStore) add(ev LiquidationEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append([]LiquidationEvent{ev}, l.events...)
	if len(l.events) > maxLiquidationEvents {
		l.events = l.events[:maxLiquidationEvents]
	}
}

func (l *liquidationStore) get() []LiquidationEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]LiquidationEvent, len(l.events))
	copy(out, l.events)
	return out
}

// maxDexTicks caps how many recent DEX ticks the page keeps -- the Rust
// side already caps what it sends per message (RECENT_CAP, 20), this is
// just the same "don't grow forever" convention across pushes.
const maxDexTicks = 40

// dexStore holds the most recent real DEX price ticks (see
// xstock_dex_watcher.rs's own module doc comment), newest first. Same
// concurrency contract as boardStore/liquidationStore.
type dexStore struct {
	mu    sync.RWMutex
	ticks []DexTick
}

// add prepends ticks (oldest-first, as the Rust side sends them) so the
// stored slice stays newest-first overall.
func (d *dexStore) add(ticks []DexTick) {
	if len(ticks) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	reversed := make([]DexTick, len(ticks))
	for i, t := range ticks {
		reversed[len(ticks)-1-i] = t
	}
	d.ticks = append(reversed, d.ticks...)
	if len(d.ticks) > maxDexTicks {
		d.ticks = d.ticks[:maxDexTicks]
	}
}

func (d *dexStore) get() []DexTick {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]DexTick, len(d.ticks))
	copy(out, d.ticks)
	return out
}

// movingMarkets tallies tick counts per ticker across whatever's
// currently in d.ticks -- "which markets are moving" derived directly
// from data already being collected for the activity feed, not a
// separate wire message. Sorted descending by count, ties broken by
// ticker name for a stable render order.
func (d *dexStore) movingMarkets() []tickerActivity {
	d.mu.RLock()
	defer d.mu.RUnlock()
	counts := make(map[string]int, len(d.ticks))
	for _, t := range d.ticks {
		counts[t.Ticker]++
	}
	out := make([]tickerActivity, 0, len(counts))
	for ticker, n := range counts {
		out = append(out, tickerActivity{Ticker: ticker, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Ticker < out[j].Ticker
	})
	return out
}

type tickerActivity struct {
	Ticker string
	Count  int
}

// displayTickerActivity is tickerActivity pre-formatted for the
// template -- WidthPct is relative to the busiest ticker in the set (the
// busiest always renders a full-width bar), not an absolute scale.
type displayTickerActivity struct {
	Ticker   string
	Count    int
	WidthPct string
}

func toDisplayMovingMarkets(activity []tickerActivity) []displayTickerActivity {
	out := make([]displayTickerActivity, len(activity))
	max := 0
	for _, a := range activity {
		if a.Count > max {
			max = a.Count
		}
	}
	for i, a := range activity {
		widthPct := 0.0
		if max > 0 {
			widthPct = float64(a.Count) / float64(max) * 100
		}
		out[i] = displayTickerActivity{Ticker: a.Ticker, Count: a.Count, WidthPct: fmt.Sprintf("%.0f", widthPct)}
	}
	return out
}

// overviewStore holds the most recently received MarketOverview. Same
// concurrency contract as boardStore.
type overviewStore struct {
	mu      sync.RWMutex
	m       MarketOverview
	updated bool
}

func (o *overviewStore) set(m MarketOverview) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m = m
	o.updated = true
}

func (o *overviewStore) get() (MarketOverview, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.m, o.updated
}

// displayEntry is HealthEntry pre-formatted for the template -- keeps
// `math.IsInf`/float formatting out of the template itself.
type displayEntry struct {
	AccountID uint64
	// Collateral/Debt are prefixed with the dominant reserve's ticker
	// (e.g. "TSLAx  $500.00") -- see HealthEntry's own doc comment for
	// why that's "dominant by USD," not necessarily the obligation's only
	// position on that side.
	Collateral   string
	Debt         string
	HealthFactor string
	// Risky is a placeholder threshold (< 1.5), not the protocol's real
	// liquidation_threshold_pct-derived cutoff (that's already folded
	// into CollateralUSD on the Rust side -- see
	// KaminoXstocksWatcher::health, which weights collateral by each
	// reserve's real threshold before this ratio is computed at all).
	// This is purely which color the row gets on the page.
	Risky bool
}

func toDisplay(entries []HealthEntry) []displayEntry {
	out := make([]displayEntry, len(entries))
	for i, e := range entries {
		hfStr := fmt.Sprintf("%.2f", e.HealthFactor)
		if math.IsInf(e.HealthFactor, 1) {
			hfStr = "∞" // no debt -- maximally safe, not a missing value
		}
		collateral := fmt.Sprintf("$%.2f", e.CollateralUSD)
		if e.CollateralUSD > 0 {
			collateral = fmt.Sprintf("%s  $%.2f", e.CollateralTicker, e.CollateralUSD)
		}
		debt := fmt.Sprintf("$%.2f", e.DebtUSD)
		if e.DebtUSD > 0 {
			debt = fmt.Sprintf("%s  $%.2f", e.DebtTicker, e.DebtUSD)
		}
		out[i] = displayEntry{
			AccountID:    e.AccountID,
			Collateral:   collateral,
			Debt:         debt,
			HealthFactor: hfStr,
			Risky:        e.HealthFactor < 1.5,
		}
	}
	return out
}

// displayLiquidation is LiquidationEvent pre-formatted for the template.
type displayLiquidation struct {
	Obligation     uint64
	RepayTicker    string
	WithdrawTicker string
	RepayUSD       string
	EstimatedBonus string
	HealthBefore   string
	Slot           uint64
	ExplorerURL    string
	SigShort       string
	SentOK         bool
}

func toDisplayLiquidations(events []LiquidationEvent) []displayLiquidation {
	out := make([]displayLiquidation, len(events))
	for i, ev := range events {
		hfStr := fmt.Sprintf("%.2f", ev.HealthFactorBefore)
		if math.IsInf(ev.HealthFactorBefore, 1) {
			hfStr = "∞"
		}
		sig := ev.Signature.String()
		short := sig
		if len(short) > 12 {
			short = short[:6] + "…" + short[len(short)-6:]
		}
		out[i] = displayLiquidation{
			Obligation:     ev.Obligation,
			RepayTicker:    ev.RepayTicker,
			WithdrawTicker: ev.WithdrawTicker,
			RepayUSD:       fmt.Sprintf("$%.2f", ev.RepayUSD),
			EstimatedBonus: fmt.Sprintf("~$%.2f", ev.EstimatedBonusUSD),
			HealthBefore:   hfStr,
			Slot:           ev.Slot,
			ExplorerURL:    "https://explorer.solana.com/tx/" + sig,
			SigShort:       short,
			SentOK:         ev.SentOK,
		}
	}
	return out
}

// displayDexTick is DexTick pre-formatted for the template.
type displayDexTick struct {
	Ticker   string
	PriceUSD string
	Up       bool
	Slot     uint64
}

func toDisplayDexTicks(ticks []DexTick) []displayDexTick {
	out := make([]displayDexTick, len(ticks))
	for i, t := range ticks {
		out[i] = displayDexTick{
			Ticker:   t.Ticker,
			PriceUSD: fmt.Sprintf("$%.2f", t.PriceUSD),
			Up:       t.Up,
			Slot:     t.Slot,
		}
	}
	return out
}

// riskSlice is one wedge of the risk-tier pie -- pre-formatted, same
// "keep formatting out of the template" convention every other display
// type here follows.
type riskSlice struct {
	Label string
	Count uint64
	Pct   string // e.g. "12.4", pre-formatted for direct display
	Color string
}

// pageData is everything boardPageTemplate needs. WorstHealthFactor is
// the current single lowest tracked health factor, formatted -- shown
// even (especially) when Liquidations is empty, so "nothing eligible
// right now" reads as a real, live fact instead of an unexplained blank
// section.
// pageSize is how many rows the Health board / Liquidation attempts
// tables show per page -- the rest is reachable by paging, not sent
// again or dropped.
const pageSize = 20

// pageWindow is one table's pagination state, pre-computed so the
// template only ever does `{{if .PageWindow.HasPrev}}` style checks, no
// arithmetic. URLs are absolute (`/?boardPage=N&liqPage=M`) so the *other*
// table's current page survives navigating this one, and so
// `<meta http-equiv="refresh">` (which reloads the exact current URL)
// keeps you on the same page across auto-refreshes.
type pageWindow struct {
	Page       int
	TotalPages int
	TotalRows  int
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
}

// paginateSlice returns page (1-indexed, clamped into range) of items,
// pageSize at a time, plus the pageWindow describing it. otherPageParam/
// otherPage build the *other* table's query param into this table's
// prev/next links, so paging one never resets the other.
func paginateSlice[T any](items []T, page int, thisParam, otherParam string, otherPage int) ([]T, pageWindow) {
	total := len(items)
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	url := func(p int) string {
		return fmt.Sprintf("/?%s=%d&%s=%d", thisParam, p, otherParam, otherPage)
	}
	return items[start:end], pageWindow{
		Page:       page,
		TotalPages: totalPages,
		TotalRows:  total,
		HasPrev:    page > 1,
		HasNext:    page < totalPages,
		PrevURL:    url(page - 1),
		NextURL:    url(page + 1),
	}
}

type pageData struct {
	Updated           bool
	TotalTracked      uint64
	Entries           []displayEntry
	BoardWindow       pageWindow
	WorstHealthFactor string
	AtRiskCount       int
	Liquidations      []displayLiquidation
	LiqWindow         pageWindow
	DexTicks          []displayDexTick
	MovingMarkets     []displayTickerActivity
	OverviewUpdated   bool
	ReservesMonitored uint16
	PoolsMonitored    uint16
	ArbDetectedUSD    string
	ArbDetectionCount uint64
	ArbByTicker       []ArbTickerAmount
	LiquidationPnLUSD string
	RiskSlices        []riskSlice
	RiskPieCSS        template.CSS
}

func buildPageData(total uint64, entries []HealthEntry, updated bool, liquidations []LiquidationEvent, dexTicks []DexTick, moving []tickerActivity, overview MarketOverview, overviewUpdated bool, boardPage, liqPage int) pageData {
	disp := toDisplay(entries)
	worst := "n/a"
	atRisk := 0
	if len(entries) > 0 {
		hf := entries[0].HealthFactor // health_board() is sorted ascending
		if math.IsInf(hf, 1) {
			worst = "∞"
		} else {
			worst = fmt.Sprintf("%.2f", hf)
		}
	}
	for _, e := range entries {
		if e.HealthFactor < 1.5 {
			atRisk++
		}
	}
	riskSlices, riskCSS := buildRiskPie(overview.RiskTierCounts)
	displayLiqs := toDisplayLiquidations(liquidations)
	boardPageEntries, boardWindow := paginateSlice(disp, boardPage, "boardPage", "liqPage", liqPage)
	liqPageEntries, liqWindow := paginateSlice(displayLiqs, liqPage, "liqPage", "boardPage", boardPage)
	arbByTicker := make([]ArbTickerAmount, len(overview.ArbByTicker))
	copy(arbByTicker, overview.ArbByTicker)
	sort.Slice(arbByTicker, func(i, j int) bool { return arbByTicker[i].USD > arbByTicker[j].USD })
	return pageData{
		Updated:           updated,
		TotalTracked:      total,
		Entries:           boardPageEntries,
		BoardWindow:       boardWindow,
		WorstHealthFactor: worst,
		AtRiskCount:       atRisk,
		Liquidations:      liqPageEntries,
		LiqWindow:         liqWindow,
		DexTicks:          toDisplayDexTicks(dexTicks),
		MovingMarkets:     toDisplayMovingMarkets(moving),
		OverviewUpdated:   overviewUpdated,
		ReservesMonitored: overview.ReservesMonitored,
		PoolsMonitored:    overview.PoolsMonitored,
		ArbDetectedUSD:    fmt.Sprintf("$%.2f", overview.ArbDetectedUSD),
		ArbDetectionCount: overview.ArbDetectionCount,
		ArbByTicker:       arbByTicker,
		LiquidationPnLUSD: fmt.Sprintf("$%.2f", overview.LiquidationPnLUSD),
		RiskSlices:        riskSlices,
		RiskPieCSS:        riskCSS,
	}
}

// riskTierLabels/riskTierColors index-align with MarketOverview.
// RiskTierCounts -- see KaminoXstocksWatcher::risk_tier_counts's own doc
// comment on the Rust side for the exact health-factor boundaries.
var riskTierLabels = [4]string{"Eligible now (≤1.0)", "At risk (1.0-1.5)", "Watch (1.5-3.0)", "Safe (≥3.0)"}
var riskTierColors = [4]string{"#f85149", "#f0883e", "#d29922", "#3fb950"}

// buildRiskPie turns raw tier counts into display slices plus a ready
// conic-gradient CSS value for the donut -- template.CSS, not a plain
// string, since html/template's CSS sanitizer for style-attribute
// contexts doesn't recognize conic-gradient() and would otherwise strip
// it; safe here because every byte comes from Go-computed numbers, never
// external input.
func buildRiskPie(counts [4]uint64) ([]riskSlice, template.CSS) {
	var total uint64
	for _, c := range counts {
		total += c
	}
	slices := make([]riskSlice, 4)
	if total == 0 {
		for i := range slices {
			slices[i] = riskSlice{Label: riskTierLabels[i], Count: 0, Pct: "0.0", Color: riskTierColors[i]}
		}
		return slices, template.CSS("conic-gradient(#30363d 0% 100%)")
	}
	segments := make([]string, 0, 4)
	cumulative := 0.0
	for i, c := range counts {
		pct := float64(c) / float64(total) * 100
		slices[i] = riskSlice{Label: riskTierLabels[i], Count: c, Pct: fmt.Sprintf("%.1f", pct), Color: riskTierColors[i]}
		next := cumulative + pct
		if c > 0 {
			segments = append(segments, fmt.Sprintf("%s %.2f%% %.2f%%", riskTierColors[i], cumulative, next))
		}
		cumulative = next
	}
	return slices, template.CSS(fmt.Sprintf("conic-gradient(%s)", strings.Join(segments, ", ")))
}

var boardPageTemplate = template.Must(template.New("board").Parse(`<!DOCTYPE html>
<html><head><title>xStocks Liquidation Engine</title>
<meta http-equiv="refresh" content="5">
<meta charset="utf-8">
<style>
:root{color-scheme:dark}
*{box-sizing:border-box}
body{font-family:-apple-system,"Segoe UI",Roboto,sans-serif;background:radial-gradient(circle at top left,#111927,#0d1117 45%);color:#e6edf3;margin:0;padding:2.5rem;line-height:1.5}
.page{max-width:1080px;margin:0 auto}
header.hero{margin-bottom:2.25rem;padding-bottom:1.5rem;border-bottom:1px solid #21262d}
h1{font-weight:700;font-size:1.7rem;margin:0 0 .35rem;background:linear-gradient(90deg,#58a6ff,#a371f7);-webkit-background-clip:text;background-clip:text;color:transparent;display:inline-block}
.subtitle{color:#8b949e;font-size:.92rem;margin:0;max-width:60ch}
.stats{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:1rem;margin-bottom:2rem}
.stat{background:#161b22;border:1px solid #30363d;border-radius:12px;padding:1.1rem 1.3rem;box-shadow:0 1px 0 rgba(255,255,255,.02) inset,0 6px 16px -8px rgba(0,0,0,.5);transition:border-color .15s,transform .15s}
.stat:hover{border-color:#3d4552;transform:translateY(-1px)}
.stat .label{color:#8b949e;font-size:.72rem;text-transform:uppercase;letter-spacing:.05em;margin-bottom:.4rem}
.stat .value{font-size:1.7rem;font-weight:700;font-variant-numeric:tabular-nums}
.stat .value.warn{color:#f0883e}
section{margin-bottom:2.75rem;background:#0f151d;border:1px solid #1c2229;border-radius:14px;padding:1.5rem 1.75rem}
h2{font-weight:700;font-size:1.1rem;margin:0 0 .5rem;display:flex;align-items:center;gap:.5rem;padding-bottom:.6rem;border-bottom:1px solid #1c2229}
.section-note{color:#8b949e;font-size:.85rem;margin:.9rem 0 1rem}
table{border-collapse:collapse;width:100%;font-size:.9rem}
th,td{padding:.6rem .9rem;text-align:right;border-bottom:1px solid #1c2229}
th:first-child,td:first-child{text-align:left}
th{color:#8b949e;font-weight:600;text-transform:uppercase;font-size:.68rem;letter-spacing:.04em}
tbody tr:hover td{background:#131a22}
tr.safe td{color:#e6edf3}
tr.risky td{color:#f0883e}
tr.risky td:last-child{color:#f85149;font-weight:600}
tr.failed td{color:#6e7681;text-decoration:line-through}
.pill{display:inline-block;padding:.15rem .55rem;border-radius:999px;font-size:.72rem;font-weight:600}
.pill.ok{background:#238636;color:#fff}
.pill.no{background:#30363d;color:#8b949e}
a{color:#58a6ff;text-decoration:none}
a:hover{text-decoration:underline}
.empty{color:#8b949e;font-size:.9rem;padding:1rem 0}
.overview{display:grid;grid-template-columns:1.1fr 1fr;gap:1.5rem}
@media (max-width:800px){.overview{grid-template-columns:1fr}}
.pnl-cards{display:flex;flex-direction:column;gap:.75rem;margin-bottom:1rem}
.pnl-card{background:#161b22;border:1px solid #30363d;border-radius:10px;padding:1rem 1.25rem}
.pnl-card .label{color:#8b949e;font-size:.72rem;text-transform:uppercase;letter-spacing:.04em;margin-bottom:.35rem}
.pnl-card .value{font-size:1.4rem;font-weight:600;font-variant-numeric:tabular-nums}
.pnl-card.detected .value{color:#d29922}
.pnl-card.realized .value{color:#3fb950}
.pnl-card .note{color:#8b949e;font-size:.78rem;margin-top:.3rem}
.bars{display:flex;flex-direction:column;gap:.5rem}
.bar-row{display:grid;grid-template-columns:70px 1fr 2.5rem;align-items:center;gap:.6rem;font-size:.85rem}
.bar-track{background:#21262d;border-radius:4px;height:.6rem;overflow:hidden}
.bar-fill{background:#58a6ff;height:100%;border-radius:4px}
.pie-wrap{display:flex;align-items:center;gap:1.5rem;flex-wrap:wrap}
.pie{width:150px;height:150px;border-radius:50%;flex-shrink:0}
.pie-legend{display:flex;flex-direction:column;gap:.4rem;font-size:.85rem}
.pie-legend .dot{display:inline-block;width:.7rem;height:.7rem;border-radius:50%;margin-right:.5rem}
.arb-breakdown{display:flex;flex-wrap:wrap;gap:.5rem;margin-top:.6rem}
.arb-chip{background:#0d1117;border:1px solid #30363d;border-radius:6px;padding:.25rem .6rem;font-size:.78rem;font-variant-numeric:tabular-nums}
.arb-chip b{color:#d29922}
.pager{display:flex;align-items:center;gap:.9rem;margin-top:.9rem;font-size:.85rem;color:#8b949e}
.pager a{color:#58a6ff;padding:.3rem .7rem;border:1px solid #30363d;border-radius:6px}
.pager a:hover{text-decoration:none;border-color:#58a6ff}
.pager .disabled{padding:.3rem .7rem;border:1px solid #21262d;border-radius:6px;color:#484f58}
</style></head>
<body>
<div class="page">
<header class="hero">
<h1>xStocks Liquidation Engine</h1>
<p class="subtitle">Watches every real obligation in Kamino's xStocks lending market and fires a real (capped, small) liquidation the moment one is eligible. Refreshes every 5s.</p>
</header>

{{if .Updated}}
<div class="stats">
  <div class="stat"><div class="label">Obligations tracked</div><div class="value">{{.TotalTracked}}</div></div>
  <div class="stat"><div class="label">Worst health factor</div><div class="value{{if .AtRiskCount}} warn{{end}}">{{.WorstHealthFactor}}</div></div>
  <div class="stat"><div class="label">Liquidations attempted</div><div class="value">{{.LiqWindow.TotalRows}}</div></div>
</div>
{{end}}

{{if .OverviewUpdated}}
<section>
<h2>📊 Bot PnL &amp; market overview</h2>
<div class="overview">
<div>
<div class="pnl-cards">
  <div class="pnl-card realized"><div class="label">Realized PnL (from liquidations)</div><div class="value">{{.LiquidationPnLUSD}}</div><div class="note">Only grows from an actual sent, landed liquidation transaction.</div></div>
  <div class="pnl-card detected">
    <div class="label">Detected arb opportunity (Kamino vs. Orca)</div>
    <div class="value">{{.ArbDetectedUSD}}</div>
    <div class="note">Added up every time the bot notices Kamino's oracle price and Orca's real pool price for the same xStock disagree by more than 0.05% -- each occurrence is valued as if a fixed $100 trade captured that gap. Nothing is bought or sold and no fees/slippage are modeled, so treat this as "how much opportunity existed," not money made. So far: <strong>{{.ArbDetectionCount}}</strong> gap{{if ne .ArbDetectionCount 1}}s{{end}} detected.</div>
    {{if .ArbByTicker}}
    <div class="arb-breakdown">
    {{range .ArbByTicker}}
    <span class="arb-chip">{{.Ticker}}: <b>${{printf "%.2f" .USD}}</b></span>
    {{end}}
    </div>
    {{end}}
  </div>
</div>
<div class="stats" style="margin-bottom:0">
  <div class="stat"><div class="label">Reserves monitored</div><div class="value">{{.ReservesMonitored}}</div></div>
  <div class="stat"><div class="label">DEX pools monitored</div><div class="value">{{.PoolsMonitored}}</div></div>
</div>
</div>
<div>
<p class="section-note" style="margin-top:0">🔥 Which markets are moving (real DEX ticks in the recent activity window)</p>
{{if .MovingMarkets}}
<div class="bars">
{{range .MovingMarkets}}
<div class="bar-row">
<div>{{.Ticker}}</div>
<div class="bar-track"><div class="bar-fill" style="width:{{.WidthPct}}%"></div></div>
<div>{{.Count}}</div>
</div>
{{end}}
</div>
{{else}}
<p class="empty">Waiting for the first real DEX price tick…</p>
{{end}}
</div>
</div>

<p class="section-note">🥧 Risk distribution across every tracked obligation with a computed health factor</p>
<div class="pie-wrap">
<div class="pie" style="background:{{.RiskPieCSS}}"></div>
<div class="pie-legend">
{{range .RiskSlices}}
<div><span class="dot" style="background:{{.Color}}"></span>{{.Label}}: {{.Count}} ({{.Pct}}%)</div>
{{end}}
</div>
</div>
</section>
{{end}}

{{if .Updated}}
<section>
<h2>🩺 Health board</h2>
<p class="section-note">Health factor = collateral value ÷ debt, with collateral already weighted by its real liquidation threshold. Below <strong>1.00</strong> means eligible for liquidation right now. Worst-to-best, page {{.BoardWindow.Page}} of {{.BoardWindow.TotalPages}} ({{.BoardWindow.TotalRows}} tracked with a computed health factor, of {{.TotalTracked}} total obligations).</p>
<table>
<tr><th>Obligation</th><th>Collateral</th><th>Debt</th><th>Health Factor</th></tr>
{{range .Entries}}
<tr class="{{if .Risky}}risky{{else}}safe{{end}}">
<td>{{.AccountID}}</td>
<td>{{.Collateral}}</td>
<td>{{.Debt}}</td>
<td>{{.HealthFactor}}</td>
</tr>
{{end}}
</table>
<div class="pager">
{{if .BoardWindow.HasPrev}}<a href="{{.BoardWindow.PrevURL}}">← Newer (worse)</a>{{else}}<span class="disabled">← Newer (worse)</span>{{end}}
<span>Page {{.BoardWindow.Page}} / {{.BoardWindow.TotalPages}}</span>
{{if .BoardWindow.HasNext}}<a href="{{.BoardWindow.NextURL}}">Older (better) →</a>{{else}}<span class="disabled">Older (better) →</span>{{end}}
</div>
</section>
{{else}}
<p class="empty">Waiting for the bot's first health board push…</p>
{{end}}

<section>
<h2>⚡ Liquidation attempts</h2>
{{if .Liquidations}}
<p class="section-note">"Est. bonus" is a rough placeholder, not read from the reserve's real bonus config -- treat it as an estimate, not a confirmed profit. Newest first, page {{.LiqWindow.Page}} of {{.LiqWindow.TotalPages}} ({{.LiqWindow.TotalRows}} total).</p>
<table>
<tr><th>Slot</th><th>Obligation</th><th>Repaid</th><th>Amount</th><th>Seized</th><th>Est. bonus</th><th>Health before</th><th>Result</th></tr>
{{range .Liquidations}}
<tr class="{{if .SentOK}}safe{{else}}failed{{end}}">
<td>{{.Slot}}</td>
<td>{{.Obligation}}</td>
<td>{{.RepayTicker}}</td>
<td>{{.RepayUSD}}</td>
<td>{{.WithdrawTicker}}</td>
<td>{{.EstimatedBonus}}</td>
<td>{{.HealthBefore}}</td>
<td>{{if .SentOK}}<span class="pill ok">sent</span> <a href="{{.ExplorerURL}}" target="_blank">{{.SigShort}}</a>{{else}}<span class="pill no">failed</span>{{end}}</td>
</tr>
{{end}}
</table>
<div class="pager">
{{if .LiqWindow.HasPrev}}<a href="{{.LiqWindow.PrevURL}}">← Newer</a>{{else}}<span class="disabled">← Newer</span>{{end}}
<span>Page {{.LiqWindow.Page}} / {{.LiqWindow.TotalPages}}</span>
{{if .LiqWindow.HasNext}}<a href="{{.LiqWindow.NextURL}}">Older →</a>{{else}}<span class="disabled">Older →</span>{{end}}
</div>
{{else}}
<p class="empty">No liquidations attempted yet -- nothing tracked is currently eligible. Worst health factor right now: <strong>{{.WorstHealthFactor}}</strong> (needs to reach 1.00 or below).</p>
{{end}}
</section>

<section>
<h2>🔀 Live DEX activity</h2>
<p class="section-note">Real swaps against the most liquid Orca pool per xStock, newest first -- not eligibility data, just proof the market (and this bot's view of it) is actually moving.</p>
{{if .DexTicks}}
<table>
<tr><th>Ticker</th><th>Price</th><th></th><th>Slot</th></tr>
{{range .DexTicks}}
<tr class="{{if .Up}}safe{{else}}risky{{end}}">
<td>{{.Ticker}}</td>
<td>{{.PriceUSD}}</td>
<td>{{if .Up}}▲{{else}}▼{{end}}</td>
<td>{{.Slot}}</td>
</tr>
{{end}}
</table>
{{else}}
<p class="empty">Waiting for the first real DEX price tick…</p>
{{end}}
</section>
</div>
</body></html>`))

// startDashboard serves the board on addr (e.g. "127.0.0.1:8090") until
// ctx is cancelled. Errors starting the listener are logged, not fatal --
// this is a convenience view, not load-bearing for the bot itself.
// queryInt reads an integer query param, falling back to def for a
// missing or malformed value rather than erroring the whole page over a
// bad page number.
func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func startDashboard(addr string, store *boardStore, liquidations *liquidationStore, dex *dexStore, overview *overviewStore, entry *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		total, entries, updated := store.get()
		m, overviewUpdated := overview.get()
		boardPage := queryInt(r, "boardPage", 1)
		liqPage := queryInt(r, "liqPage", 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = boardPageTemplate.Execute(w, buildPageData(total, entries, updated, liquidations.get(), dex.get(), dex.movingMarkets(), m, overviewUpdated, boardPage, liqPage))
	})
	server := &http.Server{Addr: addr, Handler: mux}
	entry.Info(fmt.Sprintf("xstockshealthv1: serving local health board at http://%s/", addr))
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			entry.Error(fmt.Sprintf("xstockshealthv1: dashboard server stopped: %s", err))
		}
	}()
}
