package phoenix

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dslist "git.noncepad.com/pkg/solpipe-util/ds/list"
	_ "modernc.org/sqlite" // Import the SQLite driver
)

// CreateQuery prepares the top-markets statement against phoenix_market.
// Callers wrap the result as one dispatch case of a single, consolidated
// "get top pools" tool (see prefetch/prompttool.go) rather than exposing
// their own separate tool.
func CreateQuery(db *sql.DB) (*Query, error) {
	rqs := new(Query)
	rqs.db = db
	var err error
	// phoenix_market carries no balance/liquidity column at all (just
	// market_account, symbol, asset_id -- see store's schema), unlike
	// every other DEX's pool table, so there's genuinely nothing to rank
	// "top" by here. Ordered alphabetically by symbol instead of
	// fabricating a liquidity ordering; count still caps how many come
	// back, same TopPools(ctx, count) shape as every other DEX for
	// dispatch consistency. hex(market_account) so the caller gets a
	// readable base58-decodable string back, not a raw BLOB.
	rqs.topPool, err = db.Prepare(
		"SELECT hex(market_account), symbol FROM phoenix_market ORDER BY symbol ASC LIMIT ?")
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %v", err)
	}
	return rqs, nil
}

type Query struct {
	db      *sql.DB
	topPool *sql.Stmt
}

// TopPools returns up to count phoenix_market rows, alphabetically by
// symbol, as a semicolon-joined "market=... symbol=..." list. Unlike the
// other DEXes' TopPools, this scans two columns internally since
// phoenix_market has no liquidity/balance data to rank by -- the external
// signature stays the same so the caller can dispatch to it identically.
func (q *Query) TopPools(ctx context.Context, count int) (string, error) {
	rows, err := q.topPool.QueryContext(ctx, count)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = rows.Close()
	}()
	llStr := dslist.CreateGeneric[string]()
	var marketStr, symbol string
	for rows.Next() {
		err = rows.Scan(&marketStr, &symbol)
		if err != nil {
			return "", fmt.Errorf("row scan failed: %s", err)
		}
		llStr.Append(fmt.Sprintf("market=%s symbol=%s", marketStr, symbol))
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("row iteration failed: %s", err)
	}
	// Header states the ranking basis in the returned text itself, not
	// just in the tool's static description -- so it survives even if
	// the model paraphrases everything else when relaying the answer.
	// Phoenix genuinely has no liquidity/balance data to rank by, unlike
	// every other DEX here, so this explicitly says so instead of
	// letting "top" imply a size ordering that doesn't exist.
	header := "Phoenix markets are NOT ranked by liquidity (no liquidity data exists for this DEX) -- listed alphabetically by symbol instead:\n"
	if len(llStr.Array()) == 0 {
		return header + "(no markets found)", nil
	}
	return header + strings.Join(llStr.Array(), ";"), nil
}
