// Package ledger wraps a wiki client for doing accounting.
package ledger

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	pbw "git.noncepad.com/pkg/optimizer/gen/wiki/v1"
	"git.noncepad.com/pkg/optimizer/wiki"
	dslist "git.noncepad.com/pkg/solpipe-util/ds/list"
)

type LedgerClient struct {
	c *wiki.Client
}

func FromClient(c *wiki.Client) *LedgerClient {
	x := new(LedgerClient)
	x.c = c
	return x
}

type Commit struct {
	txIndex   int
	t         time.Time
	baseTopic string
	parent    [2]string
	tx        *wiki.Transaction
	listTx    *dslist.Generic[*Transaction]
}

func (c *LedgerClient) Transaction(ctx context.Context, bookWeb string, bookTopic string) (*Commit, error) {
	commit := new(Commit)
	var err error
	commit.t = time.Now()
	commit.parent = [2]string{bookWeb, bookTopic}
	commit.listTx = dslist.CreateGeneric[*Transaction]()
	commit.tx, err = c.c.Transaction(ctx, commit.parent[0])
	if err != nil {
		return nil, err
	}
	year, month, day := commit.t.Date()
	commit.baseTopic = fmt.Sprintf("%s_%d%d%d", bookTopic, year, month, day)
	return commit, nil
}

type Transaction struct {
	c      *Commit
	mSplit map[string]float64
	// map ledger account -> commodity type -> amount
	mInventory map[string]map[string]float64
}

func (commit *Commit) Transaction() *Transaction {
	tn := new(Transaction)
	tn.c = commit
	tn.mSplit = make(map[string]float64)
	tn.mInventory = make(map[string]map[string]float64)
	commit.listTx.Append(tn)
	return tn
}

func (tn *Transaction) Ledger(topic string, amount float64) error {
	_, present := tn.mSplit[topic]
	if present {
		return fmt.Errorf("duplicate split on topic")
	}
	tn.mSplit[topic] = amount
	return nil
}

func (tn *Transaction) Inventory(accountTopic string, commodityTopic string, amount float64) error {
	mCommodity, present := tn.mInventory[accountTopic]
	if !present {
		mCommodity = make(map[string]float64)
		// return fmt.Errorf("duplicate split on topic")
		tn.mInventory[accountTopic] = mCommodity
	}
	mCommodity[commodityTopic] = amount
	return nil
}

func (commit *Commit) topic() string {
	commit.txIndex++
	return fmt.Sprintf("%s_%d", commit.baseTopic, commit.txIndex)
}

// The macro names/attributes wiki/plugin/ledger's parseHookLedger
// actually recognizes server-side (see that package's own doc comment
// and wiki/plugin/ledger/save.go/ledger.go) - a transaction topic's body
// needs exactly one %GNUCASH_TX%, plus one %GNUCASH_SPLIT% per account
// touched, each optionally followed immediately by at most one
// %GNUCASH_COMMODITY% annotating it (see body's own doc comment on that
// last constraint).
const (
	macroTx        = "GNUCASH_TX"
	macroSplit     = "GNUCASH_SPLIT"
	macroCommodity = "GNUCASH_COMMODITY"
)

// formatAmount renders amount to 2 decimal places, matching the server's
// own display convention (wiki/plugin/ledger's own formatAmount, over a
// big.Rat rather than a float64 - the amounts here are only ever a
// float64 to begin with, a known precision limitation of this type
// rather than something worth working around in this formatting step
// alone).
func formatAmount(amount float64) string {
	return strconv.FormatFloat(amount, 'f', 2, 64)
}

// body renders tn as %GNUCASH_TX%/%GNUCASH_SPLIT%/%GNUCASH_COMMODITY%
// macro text for date, one line each, in a deterministic (sorted by
// account name) order - map iteration order is random in Go, and a
// transaction's saved body shouldn't reorder itself from one Finish()
// call to the next.
//
// wiki/plugin/ledger's %GNUCASH_COMMODITY% must directly follow exactly
// one %GNUCASH_SPLIT% (see save.go's groupSplitEntries) - a second
// %GNUCASH_COMMODITY% right after the first, with no new %GNUCASH_SPLIT%
// between them, is rejected server-side as "already set" rather than
// attached to anything. Since Inventory can record more than one
// commodity per account (tn.mInventory's value is itself a map), body
// returns a clear client-side error for that case instead of emitting a
// macro sequence the server would only reject with a more confusing
// message; likewise for an Inventory() entry recorded for an account
// with no matching Ledger() split to attach it to.
func (tn *Transaction) body(date time.Time) (string, error) {
	for account := range tn.mInventory {
		if _, ok := tn.mSplit[account]; !ok {
			return "", fmt.Errorf("account %q has an Inventory() entry but no Ledger() split - "+
				"%%%s%% must directly follow a %%%s%% for the same account", account, macroCommodity, macroSplit)
		}
	}

	accounts := make([]string, 0, len(tn.mSplit))
	for account := range tn.mSplit {
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)

	var b strings.Builder
	fmt.Fprintf(&b, "%%%s{Date=%q}%%\n", macroTx, date.Format("2006-01-02"))
	for _, account := range accounts {
		fmt.Fprintf(&b, "%%%s{Account=%q Amount=%q}%%\n", macroSplit, account, formatAmount(tn.mSplit[account]))

		switch mCommodity := tn.mInventory[account]; len(mCommodity) {
		case 0:
		case 1:
			for commodityType, amount := range mCommodity {
				fmt.Fprintf(&b, "%%%s{Type=%q Amount=%q}%%\n", macroCommodity, commodityType, formatAmount(amount))
			}
		default:
			return "", fmt.Errorf("account %q has %d commodity entries, but only one %%%s%% per %%%s%% is supported",
				account, len(mCommodity), macroCommodity, macroSplit)
		}
	}
	return b.String(), nil
}

func (commit *Commit) Finish() (*pbw.PutTopicsResponse, error) {
	for tx := range commit.listTx.Drain {
		body, err := tx.body(commit.t)
		if err != nil {
			return nil, err
		}
		topic := commit.topic()
		if err := commit.tx.Topic(true, false, commit.parent[1], topic, body, "", nil, nil); err != nil {
			return nil, fmt.Errorf("failed to insert topic: %s", err)
		}
	}
	result, err := commit.tx.Commit()
	commit.tx = nil
	if err != nil {
		return nil, fmt.Errorf("failed to commit: %s", err)
	}
	return result, nil
}
