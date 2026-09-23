package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/chainstate"
	"git.noncepad.com/pkg/optimizer/portfolio"
)

// explorerTableURL links a dashboard row through to the prefetch.db table
// explorer, optionally pre-sorted so the most relevant rows (e.g. highest
// liquidity) surface first without the reader having to set that up by hand.
func explorerTableURL(table, sortCol string) string {
	if sortCol == "" {
		return prefetchExplorerPrefix + "/table/" + table
	}
	v := url.Values{}
	v.Set("sort", sortCol)
	v.Set("dir", "desc")
	return prefetchExplorerPrefix + "/table/" + table + "?" + v.Encode()
}

// explorerFilterURL links through to the prefetch.db table explorer
// pre-filtered to rows where col equals val.
func explorerFilterURL(table, col, val string) string {
	v := url.Values{}
	v.Set("col", col)
	v.Set("val", val)
	return prefetchExplorerPrefix + "/table/" + table + "?" + v.Encode()
}

// DashboardCmd serves a local, read-only HTML dashboard over whatever is in
// prefetch.db -- row counts, liquidity/balance coverage, and reference-data
// stats. It never touches the chain; it only re-runs the summary.go queries
// on every page load, so refreshing the page picks up a prefetch.db that a
// separate `download-arb` run updated underneath it.
type DashboardCmd struct {
	DBPath        string `option:"db" default:"/tmp/prefetch.db" help:"path to prefetch.db."`
	PortfolioPath string `option:"portfolio-db" help:"path to portfolio.db, as written by watch-balances (default ~/.optimizer/portfolio.db)."`
	Addr          string `option:"addr" default:":8090" help:"address to listen on."`
}

func (r *DashboardCmd) Run(rc *RunConfig) error {
	if _, err := os.Stat(r.DBPath); err != nil {
		return fmt.Errorf("failed to stat %s: %s", r.DBPath, err)
	}
	if len(r.PortfolioPath) == 0 {
		r.PortfolioPath = getPortfolioDBFilePath()
	}
	// portfolio.Open creates the file (and schema) on first use, so this
	// works even before watch-balances has ever run -- but it does need
	// the parent directory to exist.
	_ = os.MkdirAll(filepath.Dir(r.PortfolioPath), 0o750)

	chainState, err := chainstate.Create(rc.Ctx, r.DBPath, state.Client{})
	if err != nil {
		return fmt.Errorf("failed to open prefetch db: %s", err)
	}
	defer func() {
		_ = chainState.Close()
	}()
	db := chainState.Database().DB()

	// One long-lived connection to portfolio.db, shared by the PnL card and
	// the portfolio table explorer -- see the SQLITE_BUSY note on
	// DownloadArbCmd.Run for why this must not be opened twice.
	portfolioDB, err := portfolio.Open(r.PortfolioPath)
	if err != nil {
		return fmt.Errorf("failed to open portfolio db: %s", err)
	}
	defer func() {
		_ = portfolioDB.Close()
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		data, err := buildDashboardData(db, r.DBPath, portfolioDB)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := dashboardTemplate.Execute(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	// /data is what the page's own JS polls to update in place -- the
	// initial "/" render stays server-side (fast first paint, works with
	// JS off), everything after that is this JSON endpoint.
	mux.HandleFunc("/data", func(w http.ResponseWriter, req *http.Request) {
		data, err := buildDashboardData(db, r.DBPath, portfolioDB)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	registerExploreRoutes(mux, prefetchExplorerPrefix, db)
	registerExploreRoutes(mux, portfolioExplorerPrefix, portfolioDB.DB())
	fmt.Printf("dashboard listening on http://localhost%s (reading %s, portfolio %s)\n", r.Addr, r.DBPath, r.PortfolioPath)
	return http.ListenAndServe(r.Addr, mux)
}

// rowCountBar is one bar in the row-count chart. BarPct is log-scaled
// (log10(Count+1) relative to the largest table) since counts here span
// from single digits to 700k+ -- a linear scale would flatten every table
// but the biggest one or two to an invisible sliver.
type rowCountBar struct {
	Table  string
	URL    string
	Count  int
	BarPct int
}

// coverageBar is one bar in the liquidity/balance coverage chart -- what
// fraction of a table's rows have had their on-chain vault balance or
// liquidity actually fetched, vs. still sitting at NULL/0.
type coverageBar struct {
	Table    string
	URL      string
	Total    int
	Matching int
	Pct      int
	Note     string
}

type decimalBar struct {
	Decimals int
	URL      string
	Count    int
	BarPct   int
}

type lendingRow struct {
	Table string
	URL   string
	Count int
	Empty bool
	Note  string
}

type dashboardData struct {
	DBPath      string
	GeneratedAt string
	FileMB      int64
	FileMTime   string
	RowCounts   []rowCountBar
	Coverage    []coverageBar
	Decimals    []decimalBar
	Lending     []lendingRow
	PnL         *pnlData
}

func buildDashboardData(db *sql.DB, dbPath string, portfolioDB *portfolio.DB) (*dashboardData, error) {
	info, err := os.Stat(dbPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dbPath, err)
	}
	pnl, err := buildPnLData(portfolioDB)
	if err != nil {
		return nil, fmt.Errorf("build pnl: %w", err)
	}

	tables := []string{
		"raydium_amm_pool", "raydium_clmm_pool", "raydium_cpmm_pool", "orca_whirlpool_pool",
		"sanctum_lst", "mint_info", "kamino_reserve", "marginfi_bank", "solend_reserve",
		"drift_spot_market", "jet_reserve", "pumpfun_bonding_curve", "pumpswap_pool", "phoenix_market",
	}
	counts := make(map[string]int, len(tables))
	maxLog := 0.0
	for _, t := range tables {
		n, err := count(db, t)
		if err != nil {
			return nil, err
		}
		counts[t] = n
		if l := math.Log10(float64(n) + 1); l > maxLog {
			maxLog = l
		}
	}
	rowCounts := make([]rowCountBar, 0, len(tables))
	for _, t := range tables {
		n := counts[t]
		pct := 0
		if maxLog > 0 {
			pct = int(math.Round(100 * math.Log10(float64(n)+1) / maxLog))
		}
		rowCounts = append(rowCounts, rowCountBar{Table: t, URL: explorerTableURL(t, ""), Count: n, BarPct: pct})
	}

	coverageSpecs := []struct{ table, condition, note, sortCol string }{
		{"raydium_clmm_pool", "liquidity_hi != 0 OR liquidity_lo != 0", "nonzero on-chain liquidity", "liquidity_hi"},
		{"orca_whirlpool_pool", "liquidity_hi != 0 OR liquidity_lo != 0", "nonzero on-chain liquidity", "liquidity_hi"},
		{"raydium_cpmm_pool", "token0_balance IS NOT NULL", "vault balances fetched", "token0_balance"},
		{"raydium_amm_pool", "coin_balance IS NOT NULL", "vault balances fetched", "coin_balance"},
		{"sanctum_lst", "sol_value > 0", "nonzero sol_value", "sol_value"},
	}
	coverage := make([]coverageBar, 0, len(coverageSpecs))
	for _, c := range coverageSpecs {
		total, matching, err := countWithCondition(db, c.table, c.condition)
		if err != nil {
			return nil, err
		}
		pct := 0
		if total > 0 {
			pct = matching * 100 / total
		}
		coverage = append(coverage, coverageBar{
			Table: c.table, URL: explorerTableURL(c.table, c.sortCol), Total: total, Matching: matching, Pct: pct, Note: c.note,
		})
	}

	rows, err := db.Query(`SELECT decimals, COUNT(*) c FROM mint_info GROUP BY decimals ORDER BY c DESC LIMIT 6`)
	if err != nil {
		return nil, fmt.Errorf("query mint_info decimals: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var decimals []decimalBar
	maxDecCount := 0
	for rows.Next() {
		var d, c int
		if err := rows.Scan(&d, &c); err != nil {
			return nil, fmt.Errorf("scan mint_info decimals: %w", err)
		}
		decimals = append(decimals, decimalBar{Decimals: d, URL: explorerFilterURL("mint_info", "decimals", strconv.Itoa(d)), Count: c})
		if c > maxDecCount {
			maxDecCount = c
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mint_info decimals: %w", err)
	}
	for i := range decimals {
		if maxDecCount > 0 {
			decimals[i].BarPct = decimals[i].Count * 100 / maxDecCount
		}
	}

	lendingSpecs := []struct{ table, note string }{
		{"kamino_reserve", "spans multiple lending markets"},
		{"drift_spot_market", ""},
		{"marginfi_bank", ""},
		{"solend_reserve", ""},
		{"jet_reserve", ""},
	}
	lending := make([]lendingRow, 0, len(lendingSpecs))
	for _, l := range lendingSpecs {
		n, err := count(db, l.table)
		if err != nil {
			return nil, err
		}
		lending = append(lending, lendingRow{Table: l.table, URL: explorerTableURL(l.table, ""), Count: n, Empty: n == 0, Note: l.note})
	}

	return &dashboardData{
		DBPath:      dbPath,
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
		FileMB:      info.Size() / (1024 * 1024),
		FileMTime:   info.ModTime().Format("2006-01-02 15:04"),
		RowCounts:   rowCounts,
		Coverage:    coverage,
		Decimals:    decimals,
		Lending:     lending,
		PnL:         pnl,
	}, nil
}

var dashboardTemplate = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"pnlBody":  pnlBodyHTML,
	"heroBody": heroBodyHTML,
}).Parse(`<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>prefetch.db dashboard</title>
<style>
  :root {
    color-scheme: light;
    --surface-1:      #fcfcfb;
    --page:           #f9f9f7;
    --text-primary:   #0b0b0b;
    --text-secondary: #52514e;
    --text-muted:     #898781;
    --gridline:       #e1e0d9;
    --track:          #e1e0d9;
    --series-seq:      #2a78d6;
    --status-critical: #d03b3b;
    --status-good:     #0ca30c;
    --border:         rgba(11,11,11,0.10);
  }
  @media (prefers-color-scheme: dark) {
    :root {
      color-scheme: dark;
      --surface-1:      #1a1a19;
      --page:           #0d0d0d;
      --text-primary:   #ffffff;
      --text-secondary: #c3c2b7;
      --text-muted:     #898781;
      --gridline:       #2c2c2a;
      --track:          #383835;
      --series-seq:      #3987e5;
      --status-critical: #e66767;
      --status-good:     #0ca30c;
      --border:         rgba(255,255,255,0.10);
    }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 32px; background: var(--page); color: var(--text-primary);
    font-family: system-ui, -apple-system, "Segoe UI", sans-serif;
  }
  h1 { font-size: 20px; margin: 0 0 4px; }
  a { color: var(--series-seq); text-decoration: none; }
  a:hover { text-decoration: underline; }
  .meta { color: var(--text-secondary); font-size: 13px; margin-bottom: 28px; }
  .card {
    background: var(--surface-1); border: 1px solid var(--border); border-radius: 10px;
    padding: 20px 24px; margin-bottom: 20px;
  }
  .card h2 { font-size: 14px; margin: 0 0 16px; color: var(--text-secondary); font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; }
  .card-head { display: flex; justify-content: space-between; align-items: baseline; margin: 0 0 16px; }
  .card-head h2 { margin: 0; }
  .card-link { font-size: 11px; font-weight: 500; text-transform: none; letter-spacing: normal; }
  .bots-badge {
    display: inline-block; margin-left: 8px; padding: 2px 8px; border-radius: 999px;
    background: var(--track); color: var(--text-secondary); font-size: 11px; font-weight: 600;
    text-transform: none; letter-spacing: normal; vertical-align: middle;
  }
  .bar-row { display: grid; grid-template-columns: 190px 1fr 90px; align-items: center; gap: 12px; padding: 5px 0; }
  .bar-label { font-size: 13px; color: var(--text-secondary); font-variant-numeric: tabular-nums; }
  .bar-track { position: relative; height: 14px; background: var(--track); border-radius: 4px; overflow: hidden; }
  .bar-fill { position: absolute; left: 0; top: 0; bottom: 0; background: var(--series-seq); border-radius: 4px; transition: width .5s ease; }
  .bar-value { font-size: 13px; color: var(--text-primary); text-align: right; font-variant-numeric: tabular-nums; }
  .bar-note { font-size: 12px; color: var(--text-muted); }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th, td { text-align: left; padding: 6px 10px; border-bottom: 1px solid var(--gridline); }
  th { color: var(--text-muted); font-weight: 600; font-size: 11px; text-transform: uppercase; letter-spacing: 0.03em; }
  td.num { text-align: right; font-variant-numeric: tabular-nums; }
  .empty { color: var(--status-critical); font-weight: 600; }
  .hero { margin: 4px 0 28px; }
  .hero-label { display: block; font-size: 13px; color: var(--text-secondary); margin-bottom: 4px; }
  .hero .delta-pos, .hero .delta-neg { font-size: 40px; font-weight: 700; }
  .hero-sub { font-size: 14px; color: var(--text-muted); margin-left: 12px; }
  .pnl-bot { margin-bottom: 24px; padding-bottom: 20px; border-bottom: 1px solid var(--gridline); }
  .pnl-bot:last-child { margin-bottom: 0; padding-bottom: 0; border-bottom: none; }
  .pnl-bot-head { display: flex; justify-content: space-between; align-items: baseline; font-size: 14px; margin-bottom: 12px; }
  .pnl-bot-label { font-weight: 600; }
  .pnl-account { margin: 12px 0 20px 16px; }
  .pnl-account:last-child { margin-bottom: 0; }
  .pnl-account-head { display: flex; justify-content: space-between; align-items: baseline; font-size: 13px; margin-bottom: 8px; }
  .pnl-account-id { font-family: ui-monospace, monospace; }
  .delta-pos { color: var(--status-good); font-weight: 600; }
  .delta-neg { color: var(--status-critical); font-weight: 600; }
  .muted { color: var(--text-muted); }
</style>
</head>
<body>
  <h1>prefetch.db dashboard</h1>
  <div class="meta">{{.DBPath}} &middot; <span id="meta-mb">{{.FileMB}}</span> MB &middot; last written <span id="meta-mtime">{{.FileMTime}}</span> &middot; generated <span id="meta-generated">{{.GeneratedAt}}</span> &middot; <span id="meta-live">live</span> &middot; <a href="/explore">browse prefetch.db &rarr;</a> &middot; <a href="/portfolio-explore">browse portfolio.db &rarr;</a></div>

  <div class="hero" id="hero-pnl">{{heroBody .PnL}}</div>

  <div class="card">
    <div class="card-head">
      <h2>PnL by bot (portfolio.db) <span class="bots-badge" id="bots-badge">{{len .PnL.Bots}} bot(s)</span></h2>
      <a class="card-link" href="/portfolio-explore/table/balance_snapshot">browse raw balance_snapshot &rarr;</a>
    </div>
    <div id="pnl-body">{{pnlBody .PnL}}</div>
  </div>

  <div class="card">
    <h2>Row counts (log scale)</h2>
    {{range .RowCounts}}
    <div class="bar-row">
      <a class="bar-label" href="{{.URL}}">{{.Table}}</a>
      <div class="bar-track"><div class="bar-fill" id="rc-fill-{{.Table}}" style="width:{{.BarPct}}%"></div></div>
      <div class="bar-value" id="rc-val-{{.Table}}">{{.Count}}</div>
    </div>
    {{end}}
  </div>

  <div class="card">
    <h2>Liquidity / balance coverage</h2>
    {{range .Coverage}}
    <div class="bar-row">
      <a class="bar-label" href="{{.URL}}">{{.Table}}</a>
      <div class="bar-track"><div class="bar-fill" id="cov-fill-{{.Table}}" style="width:{{.Pct}}%"></div></div>
      <div class="bar-value" id="cov-val-{{.Table}}">{{.Pct}}%</div>
    </div>
    <div class="bar-row"><div></div><div class="bar-note" id="cov-note-{{.Table}}">{{.Matching}} / {{.Total}} rows have {{.Note}}</div><div></div></div>
    {{end}}
  </div>

  <div class="card">
    <h2>Lending protocols</h2>
    <table>
      <tr><th>Table</th><th>Rows</th><th>Note</th></tr>
      <tbody id="lending-body">
      {{range .Lending}}
      <tr>
        <td><a href="{{.URL}}">{{.Table}}</a></td>
        <td class="num {{if .Empty}}empty{{end}}">{{.Count}}</td>
        <td>{{.Note}}</td>
      </tr>
      {{end}}
      </tbody>
    </table>
  </div>

  <div class="card">
    <h2>mint_info decimals (top values)</h2>
    <div id="decimals-body">
    {{range .Decimals}}
    <div class="bar-row">
      <a class="bar-label" href="{{.URL}}">{{.Decimals}} decimals</a>
      <div class="bar-track"><div class="bar-fill" style="width:{{.BarPct}}%"></div></div>
      <div class="bar-value">{{.Count}}</div>
    </div>
    {{end}}
    </div>
  </div>

<script>
// Polls /data and patches values/bar widths in place -- no page reload,
// no flicker. Row-count and coverage bars have stable per-table element
// IDs so their width transitions smoothly; lending/decimals (whose row
// sets can change) are just rebuilt wholesale each tick.
function esc(s) {
  return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}
function totalHtml(hasTotal, startUSD, latestUSD, deltaUSD, deltaPositive) {
  if (!hasTotal) return '<span class="muted">total pending price data</span>';
  const cls = deltaPositive ? 'delta-pos' : 'delta-neg';
  return esc(startUSD) + ' &rarr; ' + esc(latestUSD) + ' <span class="' + cls + '">(' + esc(deltaUSD) + ')</span>';
}
function renderHero(pnl) {
  const el = document.getElementById('hero-pnl');
  if (!el) return;
  if (!pnl || !pnl.HeroHasTotal) {
    el.innerHTML = '<span class="hero-label">Total PnL</span><span class="muted" style="font-size:16px;">not enough price data yet</span>';
    return;
  }
  const cls = pnl.HeroDeltaPositive ? 'delta-pos' : 'delta-neg';
  el.innerHTML = '<span class="hero-label">Total PnL across ' + ((pnl.Bots || []).length) + ' bot(s)</span>' +
    '<span class="' + cls + '">' + esc(pnl.HeroDeltaUSD) + '</span>' +
    '<span class="hero-sub">' + esc(pnl.HeroStartUSD) + ' &rarr; ' + esc(pnl.HeroLatestUSD) + '</span>';
}
function renderPnL(pnl) {
  const body = document.getElementById('pnl-body');
  if (!body) return;
  if (!pnl || !pnl.Available) {
    body.innerHTML = '<div class="bar-note">' + esc(pnl ? pnl.Message : 'no PnL data yet.') + '</div>';
    return;
  }
  body.innerHTML = (pnl.Bots || []).map(bot => {
    const accountsHtml = (bot.Accounts || []).map(acc => {
      const rows = (acc.Mints || []).map(m => {
        let deltaCell = '<td class="num muted">unknown</td>';
        if (m.HasDelta) {
          const cls = m.DeltaPositive ? 'delta-pos' : 'delta-neg';
          deltaCell = '<td class="num ' + cls + '">' + esc(m.DeltaUSD) + '</td>';
        }
        return '<tr><td><a href="' + m.MintURL + '">' + esc(m.Mint) + '</a></td><td class="num">' +
               esc(m.StartAmount) + ' (' + esc(m.StartUSD) + ')</td>' +
               '<td class="num">' + esc(m.LatestAmount) + ' (' + esc(m.LatestUSD) + ')</td>' + deltaCell + '</tr>';
      }).join('');
      const note = acc.UnpricedCount > 0
        ? '<div class="bar-note">' + acc.UnpricedCount + ' mint(s) still pending a price.</div>' : '';
      return '<div class="pnl-account"><div class="pnl-account-head"><a class="pnl-account-id" href="' + acc.AccountURL + '">' +
             esc(acc.Account) + '</a><span>' + totalHtml(acc.HasTotal, acc.StartTotalUSD, acc.LatestTotalUSD, acc.DeltaTotalUSD, acc.DeltaPositive) +
             '</span></div><table><tr><th>Mint</th><th>Start</th><th>Current</th><th>&Delta;</th></tr>' + rows + '</table>' + note + '</div>';
    }).join('');
    return '<div class="pnl-bot"><div class="pnl-bot-head"><span class="pnl-bot-label">' + esc(bot.Label) +
           '</span><span>' + totalHtml(bot.HasTotal, bot.StartTotalUSD, bot.LatestTotalUSD, bot.DeltaTotalUSD, bot.DeltaPositive) +
           '</span></div>' + accountsHtml + '</div>';
  }).join('') || '<div class="bar-note">no bots yet.</div>';
}
async function refreshDashboard() {
  let d;
  try {
    const res = await fetch('/data', {cache: 'no-store'});
    if (!res.ok) return;
    d = await res.json();
  } catch (e) {
    return; // transient fetch error -- try again next tick
  }
  document.getElementById('meta-mb').textContent = d.FileMB;
  document.getElementById('meta-mtime').textContent = d.FileMTime;
  document.getElementById('meta-generated').textContent = d.GeneratedAt;
  const botsBadge = document.getElementById('bots-badge');
  if (botsBadge) botsBadge.textContent = ((d.PnL && d.PnL.Bots) || []).length + ' bot(s)';
  renderHero(d.PnL);
  renderPnL(d.PnL);

  for (const row of d.RowCounts) {
    const fill = document.getElementById('rc-fill-' + row.Table);
    const val = document.getElementById('rc-val-' + row.Table);
    if (fill) fill.style.width = row.BarPct + '%';
    if (val) val.textContent = row.Count;
  }
  for (const c of d.Coverage) {
    const fill = document.getElementById('cov-fill-' + c.Table);
    const val = document.getElementById('cov-val-' + c.Table);
    const note = document.getElementById('cov-note-' + c.Table);
    if (fill) fill.style.width = c.Pct + '%';
    if (val) val.textContent = c.Pct + '%';
    if (note) note.textContent = c.Matching + ' / ' + c.Total + ' rows have ' + c.Note;
  }
  const lendingBody = document.getElementById('lending-body');
  if (lendingBody) {
    lendingBody.innerHTML = d.Lending.map(l =>
      '<tr><td><a href="' + l.URL + '">' + esc(l.Table) + '</a></td><td class="num ' + (l.Empty ? 'empty' : '') + '">' +
      l.Count + '</td><td>' + esc(l.Note) + '</td></tr>'
    ).join('');
  }
  const decimalsBody = document.getElementById('decimals-body');
  if (decimalsBody) {
    decimalsBody.innerHTML = (d.Decimals || []).map(x =>
      '<div class="bar-row"><a class="bar-label" href="' + x.URL + '">' + x.Decimals + ' decimals</a>' +
      '<div class="bar-track"><div class="bar-fill" style="width:' + x.BarPct + '%"></div></div>' +
      '<div class="bar-value">' + x.Count + '</div></div>'
    ).join('');
  }
}
refreshDashboard();
setInterval(refreshDashboard, 5000);
</script>
</body>
</html>
`))
