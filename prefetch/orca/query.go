package orca

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dslist "git.noncepad.com/pkg/solpipe-util/ds/list"
	_ "modernc.org/sqlite" // Import the SQLite driver
)

// CreateQuery prepares the top-pools-by-liquidity statement against
// orca_whirlpool_pool. Callers wrap the result as one dispatch case of a
// single, consolidated "get top pools" tool (see prefetch/prompttool.go)
// rather than exposing their own separate tool.
func CreateQuery(db *sql.DB) (*Query, error) {
	rqs := new(Query)
	rqs.db = db
	var err error
	// Ranked by liquidity_hi -- the high 64 bits of the on-chain u128
	// active-liquidity value, same proxy-for-liquidity ordering already
	// used in the dashboard's coverage-card sort links and
	// cmd/harness.go's example question set (not a full USD valuation).
	// hex(pubkey) so the caller gets a readable base58-decodable string
	// back, not a raw BLOB.
	rqs.topPool, err = db.Prepare(
		"SELECT hex(pubkey) FROM orca_whirlpool_pool ORDER BY liquidity_hi DESC LIMIT ?")
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %v", err)
	}
	return rqs, nil
}

type Query struct {
	db      *sql.DB
	topPool *sql.Stmt
}

// TopPools returns up to count orca_whirlpool_pool pubkeys, ranked by
// liquidity_hi descending, as a semicolon-joined "pubkey=..." list.
func (q *Query) TopPools(ctx context.Context, count int) (string, error) {
	rows, err := q.topPool.QueryContext(ctx, count)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = rows.Close()
	}()
	llStr := dslist.CreateGeneric[string]()
	var pubkeyStr string
	for rows.Next() {
		err = rows.Scan(&pubkeyStr)
		if err != nil {
			return "", fmt.Errorf("row scan failed: %s", err)
		}
		llStr.Append(fmt.Sprintf("pubkey=%s", pubkeyStr))
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("row iteration failed: %s", err)
	}
	// Header states the ranking basis in the returned text itself, not
	// just in the tool's static description -- so it survives even if
	// the model paraphrases everything else when relaying the answer.
	header := "Orca Whirlpool pools, ranked by liquidity_hi (on-chain liquidity proxy) highest first:\n"
	if len(llStr.Array()) == 0 {
		return header + "(no pools found)", nil
	}
	return header + strings.Join(llStr.Array(), ";"), nil
}
