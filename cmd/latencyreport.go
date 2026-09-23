package main

import (
	"bufio"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"time"
)

// LatencyReportCmd turns a native-transfer latency run's log output
// into a real HTML report and (best-effort) opens it in the default
// browser. Works against either testlatencylite's own log lines or the
// fuller internal testperplatencyv1lite format it was modeled on (see
// the regexes below) -- whichever fields a given line actually has.
//
// Deliberately reads the log file rather than any structured wire
// message -- testlatencylite has no outbound custom-message protocol at
// all (see catscope-rust-bot's testlatencylite::message module doc
// comment); every result is a plain log line. Recomputes n/p50/p99
// itself from the parsed per-transfer samples rather than trusting the
// bot's own separately-logged summary lines, so it stays correct even if
// a run gets killed before that summary ever prints.
type LatencyReportCmd struct {
	LogFile string `arg:"log-file" help:"path to a native-transfer latency run's log output (stdout+stderr, redirected to a file when the run was started)"`
	Out     string `option:"out" help:"output HTML path (default: <log-file>.html)"`
	NoOpen  bool   `option:"no-open" help:"don't try to open the report in a browser"`
}

// Each per-transfer line is parsed field-by-field with its own small,
// independent regex rather than one giant one -- the richer internal
// format logs several extra fields (send_slot=/inclusion_slot=/slots=/
// write=/read=) that testlatencylite's own leaner lines don't have, and
// some of those fields can themselves read "unknown" or
// "unknown (<=Nµs)" instead of a number (a same-slot sample with no
// resolvable split -- see the real module's own doc comment on
// `write_delay_upper_bound`). Matching each field separately means a
// missing or unresolved one just leaves that column blank instead of
// failing the whole line.
var (
	reHeader   = regexp.MustCompile(`native transfer (\d+)/(\d+) confirmed via (\w+) -- sig=(\S+)`)
	reSendSlot = regexp.MustCompile(`send_slot=(\d+)`)
	reInclSlot = regexp.MustCompile(`inclusion_slot=(\d+)`)
	reSlots    = regexp.MustCompile(`\bslots=(\d+)`)
	reWriteUs  = regexp.MustCompile(`\bwrite=(\d+)µs`)
	reReadUs   = regexp.MustCompile(`\bread=(\d+)µs`)
	reTotalUs  = regexp.MustCompile(`\btotal=(\d+)`)

	// reTxEstHeader matches the separate, end-of-run "tx.index estimate"
	// lines the fuller internal module logs -- a refinement of the plain
	// write/read split above, using the real Agave `tx.index` (this
	// transfer's own ordinal position within its landing block) rather
	// than just the slot boundary, so it can resolve a same-slot sample
	// the slot-level split can't. Logged on its own line per transfer,
	// separate from (and after) the "confirmed via" line, so it's parsed
	// as a second pass keyed by transfer index and merged into the same
	// row rather than expected on the same line.
	reTxEstHeader     = regexp.MustCompile(`native transfer (\d+)/(\d+) tx\.index estimate --`)
	reTxIndex         = regexp.MustCompile(`\btx_index=(\d+)`)
	reSlotMaxTxIndex  = regexp.MustCompile(`slot_max_tx_index=(\d+)`)
	reWriteEstimateUs = regexp.MustCompile(`write_estimate=(\d+)µs`)
	reReadEstimateUs  = regexp.MustCompile(`read_estimate=(\d+)µs`)

	// reAstralaneSent matches the source's "sent transaction {sig} via
	// [real ]Astralane bundle" line -- logged on its own, separate line
	// right before the matching "confirmed via" line, carrying the exact
	// same signature. Matched by signature rather than transfer index
	// (this line has no index of its own) and merged into the row that
	// later carries the same `sig=`.
	reAstralaneSent = regexp.MustCompile(`sent transaction (\S+) via (?:real )?[Aa]stralane bundle`)
)

type laneSample struct {
	Index   int
	Sig     string
	TotalUs uint64
}

// Latency is a duration measured in whole microseconds -- every value
// this report parses is already an exact integer microsecond count (the
// source module never logs fractional µs), so this stays an integer type
// rather than a float. Its only job is display: render itself in
// whichever of milliseconds or microseconds keeps a human-readable
// number, instead of forcing every value onto one unit (the report used
// to show raw µs everywhere, which turns anything past a few hundred
// milliseconds into an unreadable 7-8 digit number).
type Latency uint64

// millisecondThresholdUs is the switchover point: below it, microseconds
// are the more legible unit (sub-millisecond values would round away
// almost all their precision as "0.XXms"); at or above it, milliseconds
// are. 1,000µs = 1ms exactly, so this is also just "one millisecond".
const millisecondThresholdUs = 1000

// String renders the microsecond-suffixed form below the threshold, the
// millisecond-suffixed form (two decimal places, i.e. rounded to the
// nearest µs) at or above it. Never seconds -- these transfers land
// within single-digit seconds at the very most, so a third unit would
// add a conversion step for the reader without buying back any real
// legibility.
func (l Latency) String() string {
	if l < millisecondThresholdUs {
		return fmt.Sprintf("%dµs", uint64(l))
	}
	return fmt.Sprintf("%.2fms", float64(l)/1000.0)
}

// latencyFuncs is the html/template FuncMap wiring Latency's formatting
// into the report template. Two entries because the template has two
// shapes of input: reportRow/reportData's plain numeric fields (already
// uint64) and the fuller internal format's optional fields, which arrive
// from the log as strings (empty when the source module itself logged
// "unknown" for a same-slot sample it couldn't resolve -- see
// optionalUintField's doc comment).
var latencyFuncs = template.FuncMap{
	"fmtUs": func(us uint64) string { return Latency(us).String() },
	"fmtUsStr": func(s string) string {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return s
		}
		return Latency(v).String()
	},
}

func (r *LatencyReportCmd) Run(rc *RunConfig) error {
	f, err := os.Open(r.LogFile)
	if err != nil {
		return fmt.Errorf("failed to open log file: %s", err)
	}
	defer f.Close()

	byLane := map[string][]laneSample{}
	rowsByIndex := map[int]*reportRow{}
	astralaneSigs := map[string]bool{}
	scanner := bufio.NewScanner(f)
	// Real log lines from a long run can be long (account dumps etc.) --
	// grow the scanner's buffer well past bufio's 64KiB default rather
	// than silently dropping any line that happens to exceed it.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if m := reHeader.FindStringSubmatch(line); m != nil {
			idx, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			mTotal := reTotalUs.FindStringSubmatch(line)
			if mTotal == nil {
				// Every real "confirmed via" line always carries a total --
				// one that doesn't is a line this regex mismatched
				// somehow, not a genuinely unresolved sample.
				continue
			}
			totalUs, err := strconv.ParseUint(mTotal[1], 10, 64)
			if err != nil {
				continue
			}
			lane := m[3]
			row := &reportRow{
				Index:     idx,
				Lane:      lane,
				Sig:       m[4],
				TotalUs:   totalUs,
				SendSlot:  optionalUintField(reSendSlot, line),
				InclSlot:  optionalUintField(reInclSlot, line),
				Slots:     optionalUintField(reSlots, line),
				WriteUs:   optionalUintField(reWriteUs, line),
				ReadUs:    optionalUintField(reReadUs, line),
				HasDetail: reSendSlot.MatchString(line),
			}
			byLane[lane] = append(byLane[lane], laneSample{Index: idx, Sig: row.Sig, TotalUs: totalUs})
			rowsByIndex[idx] = row
			continue
		}
		// Separate, end-of-run tx.index estimate lines -- logged after
		// every "confirmed via" line, on their own, so merge into
		// whichever row already exists for that transfer index rather
		// than expecting them on the same line. A row that doesn't exist
		// yet (log truncated mid-run before the confirming line) is
		// skipped rather than fabricated.
		if m := reTxEstHeader.FindStringSubmatch(line); m != nil {
			idx, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			row, ok := rowsByIndex[idx]
			if !ok {
				continue
			}
			row.TxIndex = optionalUintField(reTxIndex, line)
			row.SlotMaxTxIndex = optionalUintField(reSlotMaxTxIndex, line)
			row.WriteEstimateUs = optionalUintField(reWriteEstimateUs, line)
			row.ReadEstimateUs = optionalUintField(reReadEstimateUs, line)
			row.HasEstimate = row.TxIndex != ""
			continue
		}
		if m := reAstralaneSent.FindStringSubmatch(line); m != nil {
			astralaneSigs[m[1]] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed reading log file: %s", err)
	}
	if len(rowsByIndex) == 0 {
		return fmt.Errorf("no native-transfer confirmation lines found in %s -- wrong log file, or the run never confirmed a transfer", r.LogFile)
	}
	allRows := make([]reportRow, 0, len(rowsByIndex))
	for _, row := range rowsByIndex {
		row.Astralane = astralaneSigs[row.Sig]
		allRows = append(allRows, *row)
	}
	sort.Slice(allRows, func(i, j int) bool { return allRows[i].Index < allRows[j].Index })
	anyDetail, anyEstimate, anyAstralane := false, false, false
	astralaneN := 0
	for _, row := range allRows {
		if row.HasDetail {
			anyDetail = true
		}
		if row.HasEstimate {
			anyEstimate = true
		}
		if row.Astralane {
			anyAstralane = true
			astralaneN++
		}
	}

	lanes := []string{"LowLatency", "Commit", "Transaction"}
	var stats []laneStat
	for _, lane := range lanes {
		n, p50, p99 := percentilesUs(byLane[lane])
		stats = append(stats, laneStat{Lane: lane, N: n, P50Us: p50, P99Us: p99})
	}
	writeUs, readUs := extractResolved(allRows)
	wn, wp50, wp99 := percentilesRaw(writeUs)
	rn, rp50, rp99 := percentilesRaw(readUs)
	writeEstUs, readEstUs := extractEstimates(allRows)
	wen, wep50, wep99 := percentilesRaw(writeEstUs)
	ren, rep50, rep99 := percentilesRaw(readEstUs)

	out := r.Out
	if len(out) == 0 {
		out = r.LogFile + ".html"
	}
	data := reportData{
		Title:         fmt.Sprintf("Native transfer latency run — %d/%d", len(allRows), len(allRows)),
		Generated:     time.Now().Format(time.RFC1123),
		LogFile:       r.LogFile,
		Stats:         stats,
		Rows:          allRows,
		AnyDetail:     anyDetail,
		WriteN:        wn,
		WriteP50Us:    wp50,
		WriteP99Us:    wp99,
		ReadN:         rn,
		ReadP50Us:     rp50,
		ReadP99Us:     rp99,
		AnyEstimate:   anyEstimate,
		WriteEstN:     wen,
		WriteEstP50Us: wep50,
		WriteEstP99Us: wep99,
		ReadEstN:      ren,
		ReadEstP50Us:  rep50,
		ReadEstP99Us:  rep99,
		AnyAstralane:  anyAstralane,
		AstralaneN:    astralaneN,
		AstralaneOf:   len(allRows),
	}
	outFile, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("failed to create output file: %s", err)
	}
	defer outFile.Close()
	if err := reportTemplate.Execute(outFile, data); err != nil {
		return fmt.Errorf("failed to render report: %s", err)
	}
	fmt.Printf("wrote report: %s\n", out)
	if !r.NoOpen {
		if err := openInBrowser(out); err != nil {
			fmt.Printf("could not open browser automatically (%s) -- open %s manually\n", err, out)
		}
	}
	return nil
}

// optionalUintField returns the field's value as a display string, or ""
// if the line doesn't carry that field at all (either genuinely absent --
// testlatencylite's own leaner lines -- or logged as "unknown"/
// "unknown (<=Nµs)" for a same-slot sample the source module itself
// couldn't resolve).
func optionalUintField(re *regexp.Regexp, line string) string {
	m := re.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return m[1]
}

func extractResolved(rows []reportRow) (write, read []uint64) {
	for _, row := range rows {
		if row.WriteUs != "" {
			if v, err := strconv.ParseUint(row.WriteUs, 10, 64); err == nil {
				write = append(write, v)
			}
		}
		if row.ReadUs != "" {
			if v, err := strconv.ParseUint(row.ReadUs, 10, 64); err == nil {
				read = append(read, v)
			}
		}
	}
	return
}

func extractEstimates(rows []reportRow) (write, read []uint64) {
	for _, row := range rows {
		if row.WriteEstimateUs != "" {
			if v, err := strconv.ParseUint(row.WriteEstimateUs, 10, 64); err == nil {
				write = append(write, v)
			}
		}
		if row.ReadEstimateUs != "" {
			if v, err := strconv.ParseUint(row.ReadEstimateUs, 10, 64); err == nil {
				read = append(read, v)
			}
		}
	}
	return
}

func percentilesUs(samples []laneSample) (n uint64, p50 uint64, p99 uint64) {
	vals := make([]uint64, len(samples))
	for i, s := range samples {
		vals[i] = s.TotalUs
	}
	return percentilesRaw(vals)
}

func percentilesRaw(vals []uint64) (n uint64, p50 uint64, p99 uint64) {
	if len(vals) == 0 {
		return 0, 0, 0
	}
	sorted := make([]uint64, len(vals))
	copy(sorted, vals)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n = uint64(len(sorted))
	p50 = sorted[(len(sorted)-1)*50/100]
	p99 = sorted[(len(sorted)-1)*99/100]
	return
}

// openInBrowser best-effort opens `path` with the OS's default handler.
func openInBrowser(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

type reportRow struct {
	Index    int
	Lane     string
	Sig      string
	TotalUs  uint64
	SendSlot string
	InclSlot string
	Slots    string
	WriteUs  string
	ReadUs   string
	// HasDetail is true when this line carries the fuller field set
	// (send_slot=/inclusion_slot=/etc) -- gates whether the report shows
	// those extra columns at all, since a pure testlatencylite run never
	// has them.
	HasDetail bool
	// The tx.index-based estimate (see reTxEstHeader's own doc comment) --
	// from a separate log line, merged in by transfer index. Empty/false
	// when a run never logs this refinement at all (or, for a fair-coin
	// unresolved sample, this specific transfer never got the timestamps
	// needed to compute it).
	TxIndex         string
	SlotMaxTxIndex  string
	WriteEstimateUs string
	ReadEstimateUs  string
	HasEstimate     bool
	// Astralane is true when this transfer's own signature also appears
	// in a "sent transaction ... via Astralane bundle" line -- i.e. it
	// was actually routed through Astralane's bundler, not a plain send.
	// False (not "unknown") when a run never logs any Astralane activity
	// at all -- see AnyAstralane's own doc comment for how that's told
	// apart from "attempted and rejected."
	Astralane bool
}

type laneStat struct {
	Lane  string
	N     uint64
	P50Us uint64
	P99Us uint64
}

type reportData struct {
	Title      string
	Generated  string
	LogFile    string
	Stats      []laneStat
	Rows       []reportRow
	AnyDetail  bool
	WriteN     uint64
	WriteP50Us uint64
	WriteP99Us uint64
	ReadN      uint64
	ReadP50Us  uint64
	ReadP99Us  uint64
	// tx.index-based estimates -- see reTxEstHeader's own doc comment for
	// what makes these different from the plain Write/Read bounds above.
	AnyEstimate   bool
	WriteEstN     uint64
	WriteEstP50Us uint64
	WriteEstP99Us uint64
	ReadEstN      uint64
	ReadEstP50Us  uint64
	ReadEstP99Us  uint64
	// AnyAstralane is true when at least one row's signature was found in
	// a "sent transaction ... via Astralane bundle" line. AstralaneN/Of
	// count how many of the run's confirmed transfers actually went
	// through Astralane vs. a plain send -- distinct from a row simply
	// never resolving an estimate; a row can be a plain send and still
	// confirm normally.
	AnyAstralane bool
	AstralaneN   int
	AstralaneOf  int
}

// reportTemplate carries over the established visual pattern from the
// hand-authored reports in catscope-rust-bot/latency-reports/ (same
// CSS custom-property light/dark theme, IBM Plex fonts, per-lane color
// coding, sortable table) -- genericized to take parsed run data instead
// of being hand-written per run.
var reportTemplate = template.Must(template.New("report").Funcs(latencyFuncs).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{.Title}}</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600&family=IBM+Plex+Sans:wght@400;500;600&display=swap" rel="stylesheet">
<style>
  :root {
    --bg: #f6f7f9; --surface: #ffffff; --surface-2: #eef1f4; --border: #dde2e7;
    --text: #161b22; --text-dim: #5c6773;
    --account: #0e7c86; --account-bg: #e3f3f2;
    --tx: #a66a12; --tx-bg: #f7ecd9;
    --ok: #1a7f4e; --ok-bg: #e2f5ea;
    --unknown: #6b7280; --unknown-bg: #eceef1;
    --focus: #2f6fed;
  }
  @media (prefers-color-scheme: dark) {
    :root:not([data-theme="light"]) {
      --bg: #0e1316; --surface: #151b1f; --surface-2: #1b2227; --border: #28323a;
      --text: #e7ecef; --text-dim: #8a97a3;
      --account: #4ad9c9; --account-bg: #133633;
      --tx: #f2b544; --tx-bg: #3a2e10;
      --ok: #5fd399; --ok-bg: #123023;
      --unknown: #9aa4ae; --unknown-bg: #232a30;
      --focus: #6c9bff;
    }
  }
  :root[data-theme="dark"] {
    --bg: #0e1316; --surface: #151b1f; --surface-2: #1b2227; --border: #28323a;
    --text: #e7ecef; --text-dim: #8a97a3;
    --account: #4ad9c9; --account-bg: #133633;
    --tx: #f2b544; --tx-bg: #3a2e10;
    --ok: #5fd399; --ok-bg: #123023;
    --unknown: #9aa4ae; --unknown-bg: #232a30;
    --focus: #6c9bff;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; background: var(--bg); color: var(--text);
    font-family: "IBM Plex Sans", -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  }
  .page { max-width: 1300px; margin: 0 auto; padding: 2.5rem 1.5rem 4rem; display: flex; flex-direction: column; gap: 1.75rem; }
  header h1 { font-size: 1.5rem; font-weight: 600; margin: 0 0 0.35rem; text-wrap: balance; }
  header .meta { color: var(--text-dim); font-size: 0.85rem; font-family: "IBM Plex Mono", ui-monospace, monospace; word-break: break-all; }
  .prose { background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 1.25rem 1.5rem; display: flex; flex-direction: column; gap: 0.75rem; }
  .prose h2 { font-size: 1.05rem; font-weight: 600; margin: 0; }
  .prose p, .prose li { margin: 0; line-height: 1.6; font-size: 0.88rem; color: var(--text); }
  .prose ul { margin: 0; padding-left: 1.25rem; display: flex; flex-direction: column; gap: 0.35rem; }
  .prose code { font-family: "IBM Plex Mono", ui-monospace, monospace; background: var(--surface-2); border-radius: 4px; padding: 0.05rem 0.3rem; font-size: 0.85em; }
  .prose dl { margin: 0; display: flex; flex-direction: column; gap: 0.7rem; }
  .prose dt { font-family: "IBM Plex Mono", ui-monospace, monospace; font-weight: 600; font-size: 0.85rem; color: var(--text); }
  .prose dt .kind { font-family: "IBM Plex Sans", sans-serif; font-weight: 400; font-style: italic; color: var(--text-dim); margin-left: 0.5rem; }
  .prose dd { margin: 0.2rem 0 0; }
  .caveat { background: var(--unknown-bg); color: var(--text-dim); border: 1px solid var(--border); border-radius: 10px; padding: 0.9rem 1.1rem; font-size: 0.82rem; line-height: 1.6; }
  .caveat strong { color: var(--text); }
  .stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 0.75rem; }
  .stat { background: var(--surface); border: 1px solid var(--border); border-radius: 10px; padding: 0.85rem 1rem; display: flex; flex-direction: column; gap: 0.3rem; }
  .stat .label { font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--text-dim); }
  .stat .value { font-family: "IBM Plex Mono", ui-monospace, monospace; font-variant-numeric: tabular-nums; font-size: 1.05rem; }
  .stat.LowLatency .value { color: var(--account); }
  .stat.Transaction .value { color: var(--tx); }
  .stat.Commit .value { color: var(--ok); }
  .hint { color: var(--text-dim); font-size: 0.8rem; }
  .table-wrap { background: var(--surface); border: 1px solid var(--border); border-radius: 12px; overflow: auto; max-height: 70vh; }
  table { border-collapse: collapse; width: 100%; font-size: 0.85rem; }
  thead th { position: sticky; top: 0; background: var(--surface-2); text-align: left; padding: 0.6rem 0.8rem; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--text-dim); border-bottom: 1px solid var(--border); cursor: pointer; white-space: nowrap; user-select: none; }
  thead th:hover { color: var(--text); }
  thead th.sorted::after { content: " " attr(data-dir); font-size: 0.7em; }
  tbody td { padding: 0.5rem 0.8rem; border-bottom: 1px solid var(--border); font-family: "IBM Plex Mono", ui-monospace, monospace; font-variant-numeric: tabular-nums; white-space: nowrap; }
  tbody tr:last-child td { border-bottom: none; }
  tbody tr:hover td { background: var(--surface-2); }
  th.highlight, td.highlight { background: var(--account-bg); }
  tbody tr:hover td.highlight { background: var(--account-bg); filter: brightness(0.95); }
  th.highlight { color: var(--account); }
  .unresolved { color: var(--unknown); font-style: italic; }
  .lane-badge { display: inline-flex; align-items: center; padding: 0.15rem 0.55rem; border-radius: 999px; font-size: 0.74rem; font-family: "IBM Plex Sans", sans-serif; font-weight: 600; }
  .lane-badge.LowLatency { background: var(--account-bg); color: var(--account); }
  .lane-badge.Transaction { background: var(--tx-bg); color: var(--tx); }
  .lane-badge.Commit { background: var(--ok-bg); color: var(--ok); }
  .route-badge { display: inline-flex; align-items: center; gap: 0.3rem; padding: 0.15rem 0.55rem; border-radius: 999px; font-size: 0.74rem; font-family: "IBM Plex Sans", sans-serif; font-weight: 600; }
  .route-badge.astralane { background: var(--ok-bg); color: var(--ok); }
  .route-badge.direct { background: var(--surface-2); color: var(--text-dim); }
  footer { color: var(--text-dim); font-size: 0.78rem; text-align: center; }
</style>
</head>
<body>
<div class="page">
  <header>
    <h1>{{.Title}}</h1>
    <div class="meta">generated {{.Generated}} · source: {{.LogFile}}</div>
  </header>

  <div class="prose">
    <h2>What this test does</h2>
    <p>This is a native-transfer latency test: it sends real SOL transfers, one at a time, and times how long each takes to be observed as confirmed. Each transfer races three independent, real update channels to see which one reports it first:</p>
    <ul>
      <li><strong>LowLatency</strong> -- a fast, speculative processed-tier account-balance update, the earliest signal Solana exposes for a change to an account.</li>
      <li><strong>Commit</strong> -- a rooted/finalized-tier account-balance update, structurally further behind live (finality takes real time to accumulate).</li>
      <li><strong>Transaction</strong> -- a direct signature match against the transaction list itself, independent of either account-balance stream.</li>
    </ul>
    <p>Whichever lane observes the expected result first wins and gets timed for that transfer -- the whole point of the comparison is seeing which lane actually wins in practice, and by how much.</p>
    {{if .AnyAstralane}}
    <p>{{.AstralaneN}} of {{.AstralaneOf}} transfers were sent as a real, tipped <strong>Astralane</strong> bundle rather than a plain send -- see the Route column below for which.</p>
    {{end}}
  </div>

  {{if .AnyDetail}}
  <div class="prose">
    <h2>How the numbers below are calculated</h2>
    <p>Every timestamp here comes from this guest's own clock, measured against two real events: the moment a transfer is sent, and the moment one of the three lanes above reports it. Nothing in the underlying data streams carries a timestamp for the transfer's actual on-chain execution instant -- only its landing slot number -- so several of these columns are <strong>bounds</strong>, not exact measurements. The table below is a literal glossary of every column:</p>
    <dl>
      <dt>Total =<span class="kind">exact</span></dt>
      <dd>Real wall-clock time from send to whichever lane confirmed first. The only column here that's a direct measurement, not a bound or an estimate.</dd>
      <dt>Write ≥<span class="kind">lower bound</span></dt>
      <dd>Time from send until this guest first observed the transfer's landing slot beginning. Since the transfer could have executed anywhere within that slot -- not necessarily at its very start -- the real write time can run up to roughly one Solana slot later than what's shown here.</dd>
      <dt>Read ≤<span class="kind">upper bound</span></dt>
      <dd>Derived as Total minus Write, not measured directly. Because Write is a lower bound, Read is correspondingly an upper bound by the same margin -- an understated Write overstates Read.</dd>
      {{if .AnyEstimate}}
      <dt>Write est. ≈<span class="kind">refined estimate</span></dt>
      <dd>The same Write quantity above, refined using the transfer's own real Agave <code>tx.index</code> -- its ordinal position among every transaction in that slot's block (<code>tx_index</code> out of <code>slot_max_tx_index</code>) -- to estimate roughly how far into the slot it actually executed, rather than anchoring to the slot's start. Tighter than the plain bound wherever it resolves, but still an estimate: block execution isn't perfectly evenly spaced across a slot's real wall-clock time.</dd>
      <dt>Read est. ≈<span class="kind">refined estimate</span></dt>
      <dd>Total minus Write est. -- the same refinement applied to Read.</dd>
      {{end}}
    </dl>
    <p>Treat single-sample Write/Read splits as directional, not exact -- the <strong>est.</strong> columns (highlighted below, alongside Total) are the more trustworthy figures wherever they resolve.</p>
  </div>
  {{end}}

  {{if .AnyEstimate}}
  <div class="stats">
    <div class="stat">
      <div class="label">Write estimate</div>
      <div class="value">n={{.WriteEstN}} · p50 {{fmtUs .WriteEstP50Us}} · p99 {{fmtUs .WriteEstP99Us}}</div>
    </div>
    <div class="stat">
      <div class="label">Read estimate</div>
      <div class="value">n={{.ReadEstN}} · p50 {{fmtUs .ReadEstP50Us}} · p99 {{fmtUs .ReadEstP99Us}}</div>
    </div>
  </div>
  {{end}}

  <div class="stats">
    {{if .AnyAstralane}}
    <div class="stat">
      <div class="label">Astralane bundled</div>
      <div class="value">{{.AstralaneN}} / {{.AstralaneOf}}</div>
    </div>
    {{end}}
    {{range .Stats}}
    <div class="stat {{.Lane}}">
      <div class="label">{{.Lane}}</div>
      <div class="value">n={{.N}} · p50 {{fmtUs .P50Us}} · p99 {{fmtUs .P99Us}}</div>
    </div>
    {{end}}
    {{if .AnyDetail}}
    <div class="stat">
      <div class="label">Write delay (lower bound)</div>
      <div class="value">n={{.WriteN}} · p50 {{fmtUs .WriteP50Us}} · p99 {{fmtUs .WriteP99Us}}</div>
    </div>
    <div class="stat">
      <div class="label">Read delay (upper bound)</div>
      <div class="value">n={{.ReadN}} · p50 {{fmtUs .ReadP50Us}} · p99 {{fmtUs .ReadP99Us}}</div>
    </div>
    {{end}}
  </div>

  <div class="hint">Click a column header to sort. Highlighted columns (est. and Total) are the ones worth trusting most.</div>

  <div class="table-wrap">
    <table id="tbl">
      <thead>
        <tr>
          <th data-type="num">#</th>
          <th data-type="str">Lane</th>
          {{if .AnyAstralane}}
          <th data-type="str">Route</th>
          {{end}}
          {{if .AnyDetail}}
          <th data-type="num">Send Slot</th>
          <th data-type="num">Inclusion Slot</th>
          <th data-type="num">Slots</th>
          <th data-type="num">Write ≥</th>
          <th data-type="num">Read ≤</th>
          {{end}}
          {{if .AnyEstimate}}
          <th data-type="num">tx.index</th>
          <th data-type="num" class="highlight">Write est. ≈</th>
          <th data-type="num" class="highlight">Read est. ≈</th>
          {{end}}
          <th data-type="num" class="highlight">Total =</th>
          <th data-type="str">Signature</th>
        </tr>
      </thead>
      <tbody>
        {{range .Rows}}
        <tr>
          <td>{{.Index}}</td>
          <td><span class="lane-badge {{.Lane}}">{{.Lane}}</span></td>
          {{if $.AnyAstralane}}
          <td>{{if .Astralane}}<span class="route-badge astralane">⚡ Astralane</span>{{else}}<span class="route-badge direct">direct</span>{{end}}</td>
          {{end}}
          {{if $.AnyDetail}}
          <td>{{if .SendSlot}}{{.SendSlot}}{{else}}<span class="unresolved">—</span>{{end}}</td>
          <td>{{if .InclSlot}}{{.InclSlot}}{{else}}<span class="unresolved">—</span>{{end}}</td>
          <td>{{if .Slots}}{{.Slots}}{{else}}<span class="unresolved">—</span>{{end}}</td>
          <td{{if .WriteUs}} data-sort="{{.WriteUs}}"{{else}} data-sort="-1"{{end}}>{{if .WriteUs}}{{fmtUsStr .WriteUs}}{{else}}<span class="unresolved">unknown</span>{{end}}</td>
          <td{{if .ReadUs}} data-sort="{{.ReadUs}}"{{else}} data-sort="-1"{{end}}>{{if .ReadUs}}{{fmtUsStr .ReadUs}}{{else}}<span class="unresolved">unknown</span>{{end}}</td>
          {{end}}
          {{if $.AnyEstimate}}
          <td>{{if .TxIndex}}{{.TxIndex}}/{{.SlotMaxTxIndex}}{{else}}<span class="unresolved">unresolved</span>{{end}}</td>
          <td class="highlight"{{if .WriteEstimateUs}} data-sort="{{.WriteEstimateUs}}"{{else}} data-sort="-1"{{end}}>{{if .WriteEstimateUs}}{{fmtUsStr .WriteEstimateUs}}{{else}}<span class="unresolved">—</span>{{end}}</td>
          <td class="highlight"{{if .ReadEstimateUs}} data-sort="{{.ReadEstimateUs}}"{{else}} data-sort="-1"{{end}}>{{if .ReadEstimateUs}}{{fmtUsStr .ReadEstimateUs}}{{else}}<span class="unresolved">—</span>{{end}}</td>
          {{end}}
          <td class="highlight" data-sort="{{.TotalUs}}">{{fmtUs .TotalUs}}</td>
          <td>{{.Sig}}</td>
        </tr>
        {{end}}
      </tbody>
    </table>
  </div>

  <footer>generated by <code>optimizer latencyreport</code></footer>
</div>
<script>
  (function () {
    const table = document.getElementById('tbl');
    const tbody = table.tBodies[0];
    const ths = Array.from(table.tHead.rows[0].cells);
    let sortCol = -1, sortAsc = true;
    ths.forEach((th, i) => {
      th.addEventListener('click', () => {
        const rows = Array.from(tbody.rows);
        const type = th.dataset.type;
        sortAsc = sortCol === i ? !sortAsc : true;
        sortCol = i;
        rows.sort((a, b) => {
          // Prefer the raw-microsecond data-sort attribute over the
          // rendered text when a cell has one -- since Latency.String()
          // renders each value in whichever of ms/µs is most readable,
          // two cells in the same column can carry different units
          // ("349µs" vs "16.82ms"), and comparing their displayed
          // numbers directly would sort by digit value, not real
          // magnitude.
          const aSort = a.cells[i].dataset.sort;
          const bSort = b.cells[i].dataset.sort;
          if (type === 'num' && aSort !== undefined && bSort !== undefined) {
            return sortAsc ? aSort - bSort : bSort - aSort;
          }
          let x = a.cells[i].innerText.trim();
          let y = b.cells[i].innerText.trim();
          if (type === 'num') {
            x = parseFloat(x.replace(/[^0-9.-]/g, '')) || 0;
            y = parseFloat(y.replace(/[^0-9.-]/g, '')) || 0;
            return sortAsc ? x - y : y - x;
          }
          return sortAsc ? x.localeCompare(y) : y.localeCompare(x);
        });
        rows.forEach(r => tbody.appendChild(r));
        ths.forEach(h => { h.classList.remove('sorted'); h.removeAttribute('data-dir'); });
        th.classList.add('sorted');
        th.setAttribute('data-dir', sortAsc ? '▲' : '▼');
      });
    });
  })();
</script>
</body>
</html>
`))
