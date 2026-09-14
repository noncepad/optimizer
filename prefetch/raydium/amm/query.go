package amm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dslist "git.noncepad.com/pkg/solpipe-util/ds/list"
	_ "modernc.org/sqlite" // Import the SQLite driver
)

// CreateQuery prepares the top-pools-by-liquidity statement against
// raydium_amm_pool. Callers wrap the result as one dispatch case of a
// single, consolidated "get top pools" tool (see prefetch/prompttool.go)
// rather than exposing their own separate tool.
func CreateQuery(db *sql.DB) (*Query, error) {
	rqs := new(Query)
	rqs.db = db
	var err error
	// Ranked by coin_balance+pc_balance, restricted to rows with both
	// vault balances actually fetched (NULL means "not fetched yet", not
	// zero -- see store's schema comments). hex(pubkey) so the caller
	// gets a readable base58-decodable string back, not a raw BLOB.
	rqs.topPool, err = db.Prepare(
		"SELECT hex(pubkey) FROM raydium_amm_pool " +
			"WHERE coin_balance IS NOT NULL AND pc_balance IS NOT NULL " +
			"ORDER BY coin_balance + pc_balance DESC LIMIT ?")
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %v", err)
	}
	return rqs, nil
}

type Query struct {
	db      *sql.DB
	topPool *sql.Stmt
}

// TopPools returns up to count raydium_amm_pool pubkeys, ranked by
// coin_balance+pc_balance descending, as a semicolon-joined "pubkey=..."
// list.
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
	header := "Raydium AMM pools, ranked by combined coin+pc vault balance (liquidity proxy) highest first:\n"
	if len(llStr.Array()) == 0 {
		return header + "(no pools found)", nil
	}
	return header + strings.Join(llStr.Array(), ";"), nil
}
