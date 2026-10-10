package ledger

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"git.noncepad.com/pkg/optimizer/api"

	wikiv1 "git.noncepad.com/pkg/optimizer/gen/wiki/v1"
	sgo "github.com/solana-foundation/solana-go/v2"
)

// bookKeeper implements api.BookKeeping over a book that already exists
// (an existing %GNUCASH_BOOK% topic - see this package's own doc comment:
// client/ledger never creates books/accounts itself, only transactions).
//
// wiki/plugin/ledger has no primitive to set an account's balance
// outright - every %GNUCASH_SPLIT% is a delta applied on top of an
// account's running balance (see wiki/plugin/ledger's own package doc
// comment) - and no RPC exposed to this client reads one back either.
// Snapshot works around both gaps at once: instead of posting into a
// long-lived running account, it creates one brand-new leaf
// %GNUCASH_ACCOUNT% topic per (wallet, commodity) on every call (see
// accountTopic) and posts exactly one split against it. A topic that
// never existed before this call has no prior balance to reconcile
// against, so that single split's Amount simply *is* the account's whole
// balance - "setting" it with no read-back required. This makes a
// Snapshot call a point-in-time record (like a dated ledger entry), not
// a running total.
//
// Every wallet's leaf accounts live under a per-wallet container account
// (parentContainerTopic for the treasury's parent, childContainerTopic
// for each child - see those), itself a child of assetsParent. Splitting
// containers out this way is what lets ProfitChild/ProfitParent find a
// wallet's whole snapshot history later (%GNUCASH_SPLIT_SEARCH% rolls up
// through an account's descendants) using only the identifier
// api.BookKeeping's fixed method signatures actually hand them
// (childID, with no api.Treasury to resolve it through) - see
// splitHistory and profitFor.
type bookKeeper struct {
	lc *LedgerClient
	// web/book identify the existing %GNUCASH_BOOK% topic Snapshot posts
	// into, same as LedgerClient.Transaction's own arguments.
	web, book string
	// assetsParent must already exist as a %GNUCASH_BOOK%/%GNUCASH_ACCOUNT%
	// topic in web - Snapshot creates/reuses the parent/child wallet
	// container accounts as its children.
	assetsParent string
	// equityTopic must already exist as an account topic in web - every
	// Snapshot call's asset splits are offset by one split against this
	// account (GnuCash's own convention for an "Opening Balances"-style
	// equity account), so the transaction still balances to zero even
	// though it's a revaluation, not a two-sided trade.
	equityTopic string
}

// BookKeeper returns an api.BookKeeping that records Snapshot calls into
// web/book. assetsParent and equityTopic must both already name existing
// account topics in web (see bookKeeper's own doc comment for what each
// is used for) - BookKeeper itself never creates them.
func (c *LedgerClient) BookKeeper(web, book, assetsParent, equityTopic string) api.BookKeeping {
	bk := new(bookKeeper)
	bk.lc = c
	bk.web = web
	bk.book = book
	bk.assetsParent = assetsParent
	bk.equityTopic = equityTopic
	return bk
}

// parentContainerTopic names the fixed account under bk.assetsParent that
// holds the treasury's parent wallet's own snapshot leaf accounts. Fixed
// (not keyed by the parent's own id/pubkey) because a Wallet's ID() is a
// meaningless sentinel for a treasury's parent (see api.Treasury's own
// doc comment - a real, distinguishing wallet identity only exists for
// children) and because ProfitParent has no api.Treasury to read a real
// identity from anyway - a bookKeeper is already scoped to one treasury's
// book via its own web/book/assetsParent config, so there's only ever
// one parent to mean.
const parentContainerTopic = "SnapshotParent"

// childContainerPrefix names the account family under bk.assetsParent
// holding each child wallet's own snapshot leaf accounts - see
// childContainerTopic. ProfitParent discovers every child that's ever
// been snapshotted by listing bk.assetsParent's children and matching
// this prefix (see ProfitParent), since - like ProfitChild - it's never
// handed an api.Treasury to enumerate children through directly.
const childContainerPrefix = "SnapshotChild_"

func childContainerTopic(childID sgo.PublicKey) string {
	return childContainerPrefix + childID.String()
}

// accountTopic names the fresh leaf account Snapshot creates to record
// one wallet's balance of one commodity as of snapshotID - stamped with
// the wallet's own id (Treasury.Child's own identifier, and the same
// value ProfitChild's childID names a wallet by - see
// childContainerTopic) and the commodity's mint address, so two wallets
// or two commodities in the same snapshot never collide, and snapshotID
// (a nanosecond timestamp) so repeated Snapshot calls for the same
// wallet/commodity don't either. Topic names are unique per web
// regardless of parent nesting (see db/schema.sql's topics table), so
// this still needs walletID even though the leaf itself is created under
// a wallet-specific container - two different wallets snapshotted in the
// same call share one snapshotID.
func accountTopic(walletID, commodity sgo.PublicKey, snapshotID string) string {
	return fmt.Sprintf("Snapshot_%s_%s_%s", walletID, commodity, snapshotID)
}

// toAmount converts a raw base-unit balance (lamports, or a token's
// smallest denomination) to the float64 client/ledger's Transaction.
// Ledger/Inventory expect. Lossless for any realistic wallet balance -
// only base-unit counts past 2^53 lose precision as a float64, a
// known limitation this package already accepts elsewhere (see
// Transaction.body's own doc comment on formatAmount).
func toAmount(raw uint64) float64 {
	return float64(raw)
}

// sortedPublicKeys returns m's keys sorted by their base58 string form,
// so Snapshot builds its transaction in a deterministic order instead of
// Go's randomized map iteration - map iteration order is random in Go,
// and identical treasury state shouldn't produce a differently-ordered
// transaction body from one Snapshot call to the next (same reasoning as
// Transaction.body's own account sort).
func sortedPublicKeys[V any](m map[sgo.PublicKey]V) []sgo.PublicKey {
	keys := make([]sgo.PublicKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

func sortedChildIDs(ids []sgo.PublicKey) []sgo.PublicKey {
	out := append([]sgo.PublicKey(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func sortedStrings(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// wikiClient returns a raw WikiServiceClient over the same connection
// LedgerClient's *client.Client already dialed - ProfitChild/
// ProfitParent/splitHistory need plain PutTopic/GetTopic/ListTopics
// calls that client.Transaction's streaming batch API has no use for,
// and client.Client exposes no wrapper for any of the three, so this
// goes straight to the connection instead (client.Client.Conn's own doc
// comment: built for exactly this, sharing one connection across
// multiple service clients).
func (bk *bookKeeper) wikiClient() wikiv1.WikiServiceClient {
	return wikiv1.NewWikiServiceClient(bk.lc.c.Conn())
}

// ctx reuses the context originally passed to client.Connect, since
// none of api.BookKeeping's methods take one of their own (it's a fixed
// interface this package doesn't own). That context must already carry
// the grpcauth "USER" identity every write RPC requires (see
// client.Client.Ctx's own doc comment) - context.Background would carry
// no identity and get every write in this file rejected server-side.
func (bk *bookKeeper) ctx() context.Context {
	return bk.lc.c.Ctx()
}

// Snapshot records treasury's current parent + child wallet balances
// (native SOL, keyed under sgo.WrappedSol - the conventional stand-in
// mint address for native SOL, since Wallet reports it separately from
// TokenBalance's SPL entries - plus every nonzero SPL token balance)
// into one new transaction in bk.book, via one fresh leaf account per
// (wallet, commodity) under that wallet's container account - see
// bookKeeper's own doc comment for why. A wallet/commodity with a zero
// balance is skipped outright rather than recorded as an empty account.
// If every wallet's balance is zero, Snapshot does nothing (no empty
// transaction is posted, and no container is created/touched).
//
// Since api.Wallet exposes no price/valuation, a split's book-currency
// Amount is set equal to its commodity's raw quantity (both must only
// share the same sign - see wiki/plugin/ledger's own doc comment - not
// an equal magnitude) rather than a real fiat value; this is a nominal,
// not a valued, snapshot. ProfitChild/ProfitParent are what turn this
// nominal history into a valued figure, via Jupiter (see jupiter.go).
func (bk *bookKeeper) Snapshot(treasury api.Treasury) error {
	ctx := bk.ctx()

	type walletEntry struct {
		wallet    api.Wallet
		container string
	}
	wallets := []walletEntry{{wallet: treasury.Parent(), container: parentContainerTopic}}
	for _, childID := range sortedChildIDs(treasury.Children()) {
		wallets = append(wallets, walletEntry{wallet: treasury.Child(childID), container: childContainerTopic(childID)})
	}

	type entry struct {
		container string
		account   string
		commodity sgo.PublicKey
		amount    float64
	}
	snapshotID := time.Now().UTC().Format("20060102T150405.000000000")
	var entries []entry
	containers := map[string]bool{}
	var total float64
	for _, we := range wallets {
		walletID := we.wallet.ID()

		if sol := we.wallet.SOLBalance(); sol > 0 {
			amount := toAmount(sol)
			entries = append(entries, entry{
				container: we.container,
				account:   accountTopic(walletID, sgo.WrappedSol, snapshotID),
				commodity: sgo.WrappedSol,
				amount:    amount,
			})
			containers[we.container] = true
			total += amount
		}
		for _, mint := range sortedPublicKeys(we.wallet.TokenBalance()) {
			raw := we.wallet.TokenBalance()[mint]
			if raw == 0 {
				continue
			}
			amount := toAmount(raw)
			entries = append(entries, entry{
				container: we.container,
				account:   accountTopic(walletID, mint, snapshotID),
				commodity: mint,
				amount:    amount,
			})
			containers[we.container] = true
			total += amount
		}
	}
	if len(entries) == 0 {
		return nil
	}

	commit, err := bk.lc.Transaction(ctx, bk.web, bk.book)
	if err != nil {
		return fmt.Errorf("bookkeeping snapshot: open transaction: %w", err)
	}

	// Ensure every touched wallet's container account exists before any
	// leaf account tries to parent under it - upserted (mustBeNew=false)
	// since a wallet snapshotted before already has one.
	for _, container := range sortedStrings(containers) {
		if err := commit.tx.Topic(false, false, bk.assetsParent, container,
			`%GNUCASH_ACCOUNT{Type="ASSET"}%`, "bookkeeping snapshot container", nil, nil); err != nil {
			return fmt.Errorf("bookkeeping snapshot: ensure container %q: %w", container, err)
		}
	}
	for _, e := range entries {
		if err := commit.tx.Topic(true, false, e.container, e.account,
			`%GNUCASH_ACCOUNT{Type="ASSET"}%`, "bookkeeping snapshot", nil, nil); err != nil {
			return fmt.Errorf("bookkeeping snapshot: create account %q: %w", e.account, err)
		}
	}

	txn := commit.Transaction()
	for _, e := range entries {
		if err := txn.Ledger(e.account, e.amount); err != nil {
			return fmt.Errorf("bookkeeping snapshot: ledger split on %q: %w", e.account, err)
		}
		if err := txn.Inventory(e.account, e.commodity.String(), e.amount); err != nil {
			return fmt.Errorf("bookkeeping snapshot: inventory split on %q: %w", e.account, err)
		}
	}
	if err := txn.Ledger(bk.equityTopic, -total); err != nil {
		return fmt.Errorf("bookkeeping snapshot: equity split on %q: %w", bk.equityTopic, err)
	}

	if _, err := commit.Finish(); err != nil {
		return fmt.Errorf("bookkeeping snapshot: finish: %w", err)
	}
	return nil
}

// ProfitChild reports childID's change in valuationMint terms between
// start and finish, read back from childID's own snapshot history (see
// childContainerTopic) via profitFor.
func (bk *bookKeeper) ProfitChild(start, finish time.Time, valuationMint sgo.PublicKey, childID sgo.PublicKey) (float64, error) {
	ctx := bk.ctx()
	wc := bk.wikiClient()
	return bk.profitFor(ctx, wc, childContainerTopic(childID), start, finish, valuationMint)
}

// ProfitParent reports the treasury parent's own change in valuationMint
// terms (parentOnly), plus the parent's change combined with every child
// ever snapshotted into this book (overall) - the children are
// discovered by listing bk.assetsParent's own children and matching
// childContainerPrefix, since, like ProfitChild, this has no
// api.Treasury to enumerate them through.
func (bk *bookKeeper) ProfitParent(start, finish time.Time, valuationMint sgo.PublicKey) (parentOnly float64, overall float64, err error) {
	ctx := bk.ctx()
	wc := bk.wikiClient()

	parentOnly, err = bk.profitFor(ctx, wc, parentContainerTopic, start, finish, valuationMint)
	if err != nil {
		return 0, 0, fmt.Errorf("profit for parent: %w", err)
	}
	overall = parentOnly

	children, err := wc.ListTopics(ctx, &wikiv1.ListTopicsRequest{
		Web:    bk.web,
		Filter: &wikiv1.ListTopicsRequest_Parent{Parent: bk.assetsParent},
	})
	if err != nil {
		return 0, 0, fmt.Errorf("list snapshot containers under %q: %w", bk.assetsParent, err)
	}
	for _, topic := range children.GetTopics() {
		name := topic.GetName()
		if !strings.HasPrefix(name, childContainerPrefix) {
			continue
		}
		childProfit, err := bk.profitFor(ctx, wc, name, start, finish, valuationMint)
		if err != nil {
			return 0, 0, fmt.Errorf("profit for %q: %w", name, err)
		}
		overall += childProfit
	}
	return parentOnly, overall, nil
}

// profitFor computes containerTopic's change in valuationMint terms
// between start and finish: for every commodity ever recorded under
// containerTopic, it picks the latest snapshot reading at or before
// start (the baseline - 0 if the wallet held none of that commodity yet
// at start) and the latest at or before finish (the ending balance),
// then converts each commodity's raw-unit delta into valuationMint terms
// via Jupiter's current USD price for both - see fetchJupiterPrices's
// own doc comment for what that approximates and what it silently
// excludes (a commodity Jupiter has no price for contributes nothing to
// the total, rather than being treated as zero-valued).
//
// Returns (0, nil) - not an error - if containerTopic has no recorded
// history at all (see splitHistory).
func (bk *bookKeeper) profitFor(ctx context.Context, wc wikiv1.WikiServiceClient, containerTopic string, start, finish time.Time, valuationMint sgo.PublicKey) (float64, error) {
	history, err := bk.splitHistory(ctx, wc, containerTopic, finish)
	if err != nil {
		return 0, err
	}
	if len(history) == 0 {
		return 0, nil
	}

	baseline := map[sgo.PublicKey]float64{}
	ending := map[sgo.PublicKey]float64{}
	for _, row := range history {
		if !row.date.After(start) {
			baseline[row.mint] = row.amount
		}
		// history is oldest-first and every row's date <= finish already
		// (splitHistory's own Start/End=finish query bound), so the last
		// write per mint here is the latest reading at or before finish.
		ending[row.mint] = row.amount
	}

	mints := make(map[sgo.PublicKey]bool, len(ending)+1)
	for mint := range ending {
		mints[mint] = true
	}
	for mint := range baseline {
		mints[mint] = true
	}
	mints[valuationMint] = true
	mintList := make([]sgo.PublicKey, 0, len(mints))
	for mint := range mints {
		mintList = append(mintList, mint)
	}

	prices, err := fetchJupiterPrices(ctx, mintList)
	if err != nil {
		return 0, fmt.Errorf("fetch jupiter prices: %w", err)
	}
	valuationPrice, ok := prices[valuationMint]
	if !ok {
		return 0, fmt.Errorf("jupiter has no current price for valuation mint %s", valuationMint)
	}

	var total float64
	for mint := range mints {
		if mint.Equals(valuationMint) {
			continue // added below directly, with no USD round-trip needed
		}
		price, ok := prices[mint]
		if !ok {
			continue // unpriced - excluded from the total, not counted as zero (see fetchJupiterPrices)
		}
		deltaRaw := ending[mint] - baseline[mint]
		if deltaRaw == 0 {
			continue
		}
		deltaWhole := deltaRaw / math.Pow(10, float64(price.Decimals))
		usdValue := deltaWhole * price.USDPrice
		total += usdValue / valuationPrice.USDPrice
	}
	total += (ending[valuationMint] - baseline[valuationMint]) / math.Pow(10, float64(valuationPrice.Decimals))

	return total, nil
}
