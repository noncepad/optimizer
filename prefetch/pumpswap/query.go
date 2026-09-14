package pumpswap

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dslist "git.noncepad.com/pkg/solpipe-util/ds/list"
	_ "modernc.org/sqlite" // Import the SQLite driver
)

// CreateQuery prepares the top-pools-by-liquidity statement against
// pumpswap_pool. Callers wrap the result as one dispatch case of a
// single, consolidated "get top pools" tool (see prefetch/prompttool.go)
// rather than exposing their own separate tool.
func CreateQuery(db *sql.DB) (*Query, error) {
	rqs := new(Query)
	rqs.db = db
	var err error
	// pumpswap_pool's primary key is `pool`, not `pubkey`. Ranked by
	// base_balance+quote_balance descending -- same proxy-for-liquidity
	// ordering used elsewhere in this codebase; unlike the raydium
	// tables these balance columns are NOT NULL (default 0), so no
	// separate "has this been fetched yet" filter is needed here.
	// hex(pool) so the caller gets a readable base58-decodable string
	// back, not a raw BLOB.
	rqs.topPool, err = db.Prepare(
		"SELECT hex(pool) FROM pumpswap_pool ORDER BY base_balance + quote_balance DESC LIMIT ?")
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %v", err)
	}
	return rqs, nil
}

type Query struct {
	db      *sql.DB
	topPool *sql.Stmt
}

// TopPools returns up to count pumpswap_pool pool accounts, ranked by
// base_balance+quote_balance descending, as a semicolon-joined
// "pool=..." list.
func (q *Query) TopPools(ctx context.Context, count int) (string, error) {
	rows, err := q.topPool.QueryContext(ctx, count)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = rows.Close()
	}()
	llStr := dslist.CreateGeneric[string]()
	var poolStr string
	for rows.Next() {
		err = rows.Scan(&poolStr)
		if err != nil {
			return "", fmt.Errorf("row scan failed: %s", err)
		}
		llStr.Append(fmt.Sprintf("pool=%s", poolStr))
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("row iteration failed: %s", err)
	}
	// Header states the ranking basis in the returned text itself, not
	// just in the tool's static description -- so it survives even if
	// the model paraphrases everything else when relaying the answer.
	header := "Pumpswap pools, ranked by combined base+quote balance (liquidity proxy) highest first:\n"
	if len(llStr.Array()) == 0 {
		return header + "(no pools found)", nil
	}
	return header + strings.Join(llStr.Array(), ";"), nil
}
