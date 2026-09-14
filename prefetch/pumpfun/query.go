package pumpfun

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dslist "git.noncepad.com/pkg/solpipe-util/ds/list"
	_ "modernc.org/sqlite" // Import the SQLite driver
)

// CreateQuery prepares the top-bonding-curves-by-reserves statement
// against pumpfun_bonding_curve. Callers wrap the result as one dispatch
// case of a single, consolidated "get top pools" tool (see
// prefetch/prompttool.go) rather than exposing their own separate tool.
func CreateQuery(db *sql.DB) (*Query, error) {
	rqs := new(Query)
	rqs.db = db
	var err error
	// pumpfun_bonding_curve's primary key is `mint`, not `pubkey` -- each
	// row is one bonding curve, not a swap pool. Ranked by
	// real_sol_reserves (the curve's actual on-chain SOL reserves, not
	// its virtual/pricing-curve reserves) descending. hex(mint) so the
	// caller gets a readable base58-decodable string back, not a raw
	// BLOB.
	rqs.topPool, err = db.Prepare(
		"SELECT hex(mint) FROM pumpfun_bonding_curve ORDER BY real_sol_reserves DESC LIMIT ?")
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %v", err)
	}
	return rqs, nil
}

type Query struct {
	db      *sql.DB
	topPool *sql.Stmt
}

// TopPools returns up to count pumpfun_bonding_curve mints, ranked by
// real_sol_reserves descending, as a semicolon-joined "mint=..." list.
func (q *Query) TopPools(ctx context.Context, count int) (string, error) {
	rows, err := q.topPool.QueryContext(ctx, count)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = rows.Close()
	}()
	llStr := dslist.CreateGeneric[string]()
	var mintStr string
	for rows.Next() {
		err = rows.Scan(&mintStr)
		if err != nil {
			return "", fmt.Errorf("row scan failed: %s", err)
		}
		llStr.Append(fmt.Sprintf("mint=%s", mintStr))
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("row iteration failed: %s", err)
	}
	// Header states the ranking basis in the returned text itself, not
	// just in the tool's static description -- so it survives even if
	// the model paraphrases everything else when relaying the answer.
	header := "Pump.fun bonding curves, ranked by real SOL reserves highest first:\n"
	if len(llStr.Array()) == 0 {
		return header + "(no bonding curves found)", nil
	}
	return header + strings.Join(llStr.Array(), ";"), nil
}
