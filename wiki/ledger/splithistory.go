package ledger

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	sgo "github.com/solana-foundation/solana-go/v2"
	wikiv1 "git.noncepad.com/pkg/optimizer/gen/wiki/v1"
)

// splitSearchGenesis stands in for "the beginning of this book's
// history" - %GNUCASH_SPLIT_SEARCH% requires both a Start and an End (see
// wiki/plugin/ledger/view.go's renderSplitSearch), so splitHistory always
// queries from here through the caller's finish, rather than an
// open-ended "everything up to finish."
const splitSearchGenesis = "2000-01-01"

// splitSearchHeader is the exact Markdown table header row
// wiki/plugin/ledger's renderSplitSearch renders (render.RenderMarkdown's
// table format: "| " + cell + " |" per column, see render/markdown.go's
// renderTable) - splitHistory looks for this literal line to find where
// the table starts, rather than a real Markdown parser.
const splitSearchHeader = "| Date | Transaction | Account | Debit | Credit | Commodity |"

type splitRow struct {
	date   time.Time
	mint   sgo.PublicKey
	amount float64 // signed: Debit positive, Credit negative
}

// splitHistory returns every split recorded under containerTopic (and its
// descendant leaf accounts - see accountTopic/childContainerTopic) from
// splitSearchGenesis through finish, oldest first, by saving a
// %GNUCASH_SPLIT_SEARCH% query into bk.pnlQueryTopic and parsing the
// rendered Markdown table it comes back as - see bookKeeper's own doc
// comment for why this scrapes rendered output instead of calling a
// direct query RPC (none exists).
//
// Returns (nil, nil), not an error, if containerTopic doesn't exist yet
// (no Snapshot call has ever recorded this wallet - renderSplitSearch
// renders that as an inline "[...: account topic ... not found]" error
// string in place of a table, which this simply doesn't recognize as one)
// or if it exists but has no splits ("No splits found." - see
// renderSplitSearch) - both read as "no history to compute a PnL from,"
// not a hard failure.
func (bk *bookKeeper) splitHistory(ctx context.Context, wc wikiv1.WikiServiceClient, containerTopic string, finish time.Time) ([]splitRow, error) {
	body := fmt.Sprintf(`%%GNUCASH_SPLIT_SEARCH{Account=%q Start=%q End=%q}%%`,
		containerTopic, splitSearchGenesis, finish.Format("2006-01-02"))
	if _, err := wc.PutTopic(ctx, &wikiv1.PutTopicRequest{
		Web: bk.web, Topic: bk.pnlQueryTopic(), Body: body, Comment: "bookkeeping profit query",
	}); err != nil {
		return nil, fmt.Errorf("save PnL query topic %q: %w", bk.pnlQueryTopic(), err)
	}
	rev, err := wc.GetTopic(ctx, &wikiv1.GetTopicRequest{Web: bk.web, Topic: bk.pnlQueryTopic()})
	if err != nil {
		return nil, fmt.Errorf("render PnL query topic %q: %w", bk.pnlQueryTopic(), err)
	}
	return parseSplitSearchTable(rev.GetBody())
}

// pnlQueryTopic names the scratch topic splitHistory's
// %GNUCASH_SPLIT_SEARCH% query is rendered through, upserted (its body
// overwritten) on every call rather than left behind as clutter - its
// topic history has no meaning of its own.
func (bk *bookKeeper) pnlQueryTopic() string {
	return bk.book + "_SnapshotQuery"
}

func parseSplitSearchTable(body string) ([]splitRow, error) {
	lines := strings.Split(body, "\n")
	var rows []splitRow
	inTable := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !inTable {
			if line == splitSearchHeader {
				inTable = true
			}
			continue
		}
		if strings.HasPrefix(line, "| ---") {
			continue // the GFM header/body separator row
		}
		if !strings.HasPrefix(line, "|") {
			break // table ended
		}
		cells := splitTableRow(line)
		if len(cells) != 6 {
			continue
		}
		row, ok, err := parseSplitSearchRow(cells)
		if err != nil {
			return nil, err
		}
		if ok {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// parseSplitSearchRow turns one table row's cells (Date, Transaction,
// Account, Debit, Credit, Commodity) into a splitRow. ok is false for a
// row with no Commodity annotation (not one of Snapshot's own splits -
// every split Snapshot posts always has one, see bookkeeping.go) or
// whose Commodity type isn't a valid mint address, so a hand-authored
// ledger transaction sharing this container by mistake is silently
// skipped rather than corrupting a PnL total.
func parseSplitSearchRow(cells []string) (row splitRow, ok bool, err error) {
	date, err := time.Parse("2006-01-02", cells[0])
	if err != nil {
		return splitRow{}, false, fmt.Errorf("parse split date %q: %w", cells[0], err)
	}

	var amount float64
	switch {
	case cells[3] != "":
		amount, err = strconv.ParseFloat(cells[3], 64)
	case cells[4] != "":
		var credit float64
		credit, err = strconv.ParseFloat(cells[4], 64)
		amount = -credit
	default:
		return splitRow{}, false, nil
	}
	if err != nil {
		return splitRow{}, false, fmt.Errorf("parse split amount %q/%q: %w", cells[3], cells[4], err)
	}

	commodityParts := strings.SplitN(cells[5], " ", 2)
	if len(commodityParts) != 2 {
		return splitRow{}, false, nil
	}
	mint, err := sgo.PublicKeyFromBase58(commodityParts[1])
	if err != nil {
		return splitRow{}, false, nil
	}

	return splitRow{date: date, mint: mint, amount: amount}, true, nil
}

// splitTableRow splits one rendered "| a | b | c |" line into its
// trimmed cell values ["a", "b", "c"], undoing render/markdown.go's
// renderTable formatting (leading/trailing "|", " | " between cells).
func splitTableRow(line string) []string {
	parts := strings.Split(line, "|")
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	if len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}
