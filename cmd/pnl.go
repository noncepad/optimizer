package main

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"

	"git.noncepad.com/pkg/optimizer/portfolio"
	sgo "github.com/gagliardetto/solana-go"
)

// pnlMintRow is one mint's starting-vs-current mark-to-market row within
// an account, formatted for display. USD values are nil when a price was
// never available for that end of the timeline. MintURL points at the
// explorer's raw balance_snapshot rows for this mint, for click-through.
type pnlMintRow struct {
	Mint          string
	MintURL       string
	StartTime     string
	StartAmount   string
	StartUSD      string
	LatestTime    string
	LatestAmount  string
	LatestUSD     string
	DeltaUSD      string
	HasDelta      bool
	DeltaPositive bool
}

type pnlAccountRow struct {
	Account        string
	AccountURL     string
	Mints          []pnlMintRow
	StartTotalUSD  string
	LatestTotalUSD string
	DeltaTotalUSD  string
	HasTotal       bool
	DeltaPositive  bool
	UnpricedCount  int
}

// pnlBotRow is one bot -- a bot is exactly one market+pipeline pair -- with
// every account it has reported balances under.
type pnlBotRow struct {
	Label          string
	Market         string
	Pipeline       string
	Accounts       []pnlAccountRow
	StartTotalUSD  string
	LatestTotalUSD string
	DeltaTotalUSD  string
	HasTotal       bool
	DeltaPositive  bool
}

type pnlData struct {
	Available bool
	Message   string
	Path      string
	Bots      []pnlBotRow

	// Hero: total across every bot, for the headline number at the top of
	// the dashboard.
	HeroHasTotal      bool
	HeroStartUSD      string
	HeroLatestUSD     string
	HeroDeltaUSD      string
	HeroDeltaPositive bool
}

// buildPnLData reads portfolio.db (as written by `optimizer watch-balances`,
// possibly by several instances -- one per bot -- sharing the same file)
// and computes starting-vs-current mark-to-market value, grouped by bot
// (market+pipeline) and then by account. It degrades gracefully -- no
// snapshots yet is not an error, it's just "no data yet", since
// watch-balances is a separate, optional process. db is the dashboard's
// single long-lived connection to portfolio.db, shared with the portfolio
// table explorer -- this must never open its own separate *sql.DB against
// the same file (see the SQLITE_BUSY note on DownloadArbCmd.Run).
func buildPnLData(db *portfolio.DB) (*pnlData, error) {
	path := db.FilePath()
	bots, err := db.Bots()
	if err != nil {
		return nil, err
	}
	if len(bots) == 0 {
		return &pnlData{
			Available: false,
			Path:      path,
			Message:   "0 bots reporting yet. Each bot (one market+pipeline pair) appears here automatically the moment it reports its first balance -- no setup needed on this end.",
		}, nil
	}

	out := &pnlData{Available: true, Path: path}
	var heroStart, heroLatest float64
	heroHaveStart, heroHaveLatest := false, false

	for i, bot := range bots {
		accounts, err := db.AccountsForBot(bot)
		if err != nil {
			return nil, err
		}
		botRow := pnlBotRow{
			Label:    fmt.Sprintf("Bot %d — market %s", i+1, shortPubkey(bot.Market)),
			Market:   bot.Market.String(),
			Pipeline: bot.Pipeline.String(),
		}
		var botStart, botLatest float64
		botHaveStart, botHaveLatest := false, false

		for _, account := range accounts {
			positions, err := db.Positions(account)
			if err != nil {
				return nil, err
			}
			accRow := pnlAccountRow{Account: account.String(), AccountURL: snapshotExplorerURL("account", account.String())}
			var accStart, accLatest float64
			accHaveStart, accHaveLatest := false, false

			for _, p := range positions {
				// Positions() spans every mint an account has ever reported,
				// which could in principle span more than one bot if an
				// account were ever reused -- keep only this bot's rows.
				if !p.Market.Equals(bot.Market) || !p.Pipeline.Equals(bot.Pipeline) {
					continue
				}
				mr := pnlMintRow{
					Mint:         p.Mint.String(),
					MintURL:      snapshotExplorerURL("mint", p.Mint.String()),
					StartTime:    p.StartTime.Format("2006-01-02 15:04"),
					StartAmount:  formatAmount(p.StartBalance, p.StartDecimals),
					StartUSD:     formatUSD(p.StartUSDValue),
					LatestTime:   p.LatestTime.Format("2006-01-02 15:04"),
					LatestAmount: formatAmount(p.LatestBalance, p.LatestDecimals),
					LatestUSD:    formatUSD(p.LatestUSDValue),
				}
				if p.DeltaUSDValue != nil {
					mr.HasDelta = true
					mr.DeltaPositive = *p.DeltaUSDValue >= 0
					mr.DeltaUSD = formatUSDSigned(*p.DeltaUSDValue)
				} else {
					accRow.UnpricedCount++
				}
				if p.StartUSDValue != nil {
					accStart += *p.StartUSDValue
					accHaveStart = true
				}
				if p.LatestUSDValue != nil {
					accLatest += *p.LatestUSDValue
					accHaveLatest = true
				}
				accRow.Mints = append(accRow.Mints, mr)
			}
			if len(accRow.Mints) == 0 {
				continue
			}
			if accHaveStart && accHaveLatest {
				accRow.HasTotal = true
				accRow.StartTotalUSD = formatUSD(&accStart)
				accRow.LatestTotalUSD = formatUSD(&accLatest)
				delta := accLatest - accStart
				accRow.DeltaPositive = delta >= 0
				accRow.DeltaTotalUSD = formatUSDSigned(delta)
				botStart += accStart
				botLatest += accLatest
				botHaveStart, botHaveLatest = true, true
			}
			botRow.Accounts = append(botRow.Accounts, accRow)
		}
		if botHaveStart && botHaveLatest {
			botRow.HasTotal = true
			botRow.StartTotalUSD = formatUSD(&botStart)
			botRow.LatestTotalUSD = formatUSD(&botLatest)
			delta := botLatest - botStart
			botRow.DeltaPositive = delta >= 0
			botRow.DeltaTotalUSD = formatUSDSigned(delta)
			heroStart += botStart
			heroLatest += botLatest
			heroHaveStart, heroHaveLatest = true, true
		}
		out.Bots = append(out.Bots, botRow)
	}

	if heroHaveStart && heroHaveLatest {
		out.HeroHasTotal = true
		out.HeroStartUSD = formatUSD(&heroStart)
		out.HeroLatestUSD = formatUSD(&heroLatest)
		delta := heroLatest - heroStart
		out.HeroDeltaPositive = delta >= 0
		out.HeroDeltaUSD = formatUSDSigned(delta)
	}
	return out, nil
}

// snapshotExplorerURL links a PnL row through to the generic table
// explorer's raw balance_snapshot rows for one account or mint, newest
// first -- this is the "click around and see the data behind this number"
// path, reusing /explore rather than building a second detail view.
func snapshotExplorerURL(col, val string) string {
	v := url.Values{}
	v.Set("col", col)
	v.Set("val", val)
	v.Set("sort", "time")
	v.Set("dir", "desc")
	return portfolioExplorerPrefix + "/table/balance_snapshot?" + v.Encode()
}

// shortPubkey renders a base58 pubkey as "ABCD…WXYZ" for compact labels.
func shortPubkey(k sgo.PublicKey) string {
	s := k.String()
	if len(s) <= 10 {
		return s
	}
	return s[:4] + "…" + s[len(s)-4:]
}

func formatAmount(raw uint64, decimals *uint8) string {
	if decimals == nil {
		return fmt.Sprintf("%d (raw)", raw)
	}
	whole := float64(raw) / pow10f(*decimals)
	return fmt.Sprintf("%.6g", whole)
}

func formatUSD(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("$%.2f", *v)
}

func formatUSDSigned(v float64) string {
	sign := "+"
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s$%.2f", sign, v)
}

func pow10f(n uint8) float64 {
	v := 1.0
	for range n {
		v *= 10
	}
	return v
}

// pnlBodyHTML renders the PnL card's inner HTML server-side for the initial
// page load. The page's own JS (renderPnL in dashboard.go's template)
// mirrors this exact markup so later /data polls can patch it in place
// without a full reload. Mint/account text is base58 (a restricted
// alphanumeric alphabet with no HTML metacharacters) and every number is
// formatted by us, so nothing here needs escaping except the free-text
// status Message; URLs are built with net/url so they're already
// query-escaped.
func pnlBodyHTML(p *pnlData) template.HTML {
	if p == nil || !p.Available {
		msg := "no PnL data yet."
		if p != nil {
			msg = p.Message
		}
		return template.HTML(`<div class="bar-note">` + template.HTMLEscapeString(msg) + `</div>`)
	}
	if len(p.Bots) == 0 {
		return template.HTML(`<div class="bar-note">no bots yet.</div>`)
	}
	var b strings.Builder
	for _, bot := range p.Bots {
		b.WriteString(`<div class="pnl-bot"><div class="pnl-bot-head"><span class="pnl-bot-label">`)
		b.WriteString(bot.Label)
		b.WriteString(`</span><span>`)
		writeTotal(&b, bot.HasTotal, bot.StartTotalUSD, bot.LatestTotalUSD, bot.DeltaTotalUSD, bot.DeltaPositive)
		b.WriteString(`</span></div>`)
		for _, acc := range bot.Accounts {
			b.WriteString(`<div class="pnl-account"><div class="pnl-account-head"><a class="pnl-account-id" href="`)
			b.WriteString(acc.AccountURL)
			b.WriteString(`">`)
			b.WriteString(acc.Account)
			b.WriteString(`</a><span>`)
			writeTotal(&b, acc.HasTotal, acc.StartTotalUSD, acc.LatestTotalUSD, acc.DeltaTotalUSD, acc.DeltaPositive)
			b.WriteString(`</span></div><table><tr><th>Mint</th><th>Start</th><th>Current</th><th>&Delta;</th></tr>`)
			for _, m := range acc.Mints {
				deltaCell := `<td class="num muted">unknown</td>`
				if m.HasDelta {
					cls := "delta-neg"
					if m.DeltaPositive {
						cls = "delta-pos"
					}
					deltaCell = fmt.Sprintf(`<td class="num %s">%s</td>`, cls, m.DeltaUSD)
				}
				fmt.Fprintf(&b, `<tr><td><a href="%s">%s</a></td><td class="num">%s (%s)</td><td class="num">%s (%s)</td>%s</tr>`,
					m.MintURL, m.Mint, m.StartAmount, m.StartUSD, m.LatestAmount, m.LatestUSD, deltaCell)
			}
			b.WriteString(`</table>`)
			if acc.UnpricedCount > 0 {
				fmt.Fprintf(&b, `<div class="bar-note">%d mint(s) still pending a price.</div>`, acc.UnpricedCount)
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</div>`)
	}
	return template.HTML(b.String())
}

// heroBodyHTML renders the headline "Total PnL across N bot(s)" figure at
// the top of the dashboard -- see marks-and-anatomy's hero-figure guidance:
// one big number, the delta color-coded, with the start/current pair as
// smaller supporting text.
func heroBodyHTML(p *pnlData) template.HTML {
	if p == nil || !p.HeroHasTotal {
		return template.HTML(`<span class="hero-label">Total PnL</span><span class="muted" style="font-size:16px;">not enough price data yet</span>`)
	}
	cls := "delta-neg"
	if p.HeroDeltaPositive {
		cls = "delta-pos"
	}
	return template.HTML(fmt.Sprintf(
		`<span class="hero-label">Total PnL across %d bot(s)</span><span class="%s">%s</span><span class="hero-sub">%s &rarr; %s</span>`,
		len(p.Bots), cls, p.HeroDeltaUSD, p.HeroStartUSD, p.HeroLatestUSD,
	))
}

func writeTotal(b *strings.Builder, hasTotal bool, startUSD, latestUSD, deltaUSD string, deltaPositive bool) {
	if !hasTotal {
		b.WriteString(`<span class="muted">total pending price data</span>`)
		return
	}
	cls := "delta-neg"
	if deltaPositive {
		cls = "delta-pos"
	}
	fmt.Fprintf(b, `%s &rarr; %s <span class="%s">(%s)</span>`, startUSD, latestUSD, cls, deltaUSD)
}
