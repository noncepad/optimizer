package main

import (
	"database/sql"
	"fmt"
	"strings"

	"git.noncepad.com/pkg/optimizer/store"
)

// HarnessCmd runs a fixed set of questions' SQL directly against
// prefetch.db and prints the real, known-correct answer for each --
// intended as a baseline any future answer-checking step (AI or
// otherwise) would be graded against, though nothing does that grading
// yet.
type HarnessCmd struct {
	DBPath string `option:"db" default:"/tmp/prefetch2.db" help:"path to prefetch.db to run harness questions against."`
}

// harnessQuestion pairs a plain-English question with the SQL that
// computes its real, correct answer.
type harnessQuestion struct {
	Question string
	Table    string
	SQL      string
}

var harnessQuestions = []harnessQuestion{
	{
		"How many pools does raydium_amm_pool have?",
		"raydium_amm_pool",
		`SELECT COUNT(*) FROM raydium_amm_pool`,
	},
	{
		"What are the top 10 raydium_amm_pool pools by liquidity?",
		"raydium_amm_pool",
		`SELECT hex(pubkey), coin_balance + pc_balance AS total FROM raydium_amm_pool
		 WHERE coin_balance IS NOT NULL AND pc_balance IS NOT NULL
		 ORDER BY total DESC LIMIT 10`,
	},
	{
		"What percent of raydium_cpmm_pool rows have their vault balance fetched?",
		"raydium_cpmm_pool",
		`SELECT printf('%.2f%%', 100.0 * SUM(token0_balance IS NOT NULL) / COUNT(*)) FROM raydium_cpmm_pool`,
	},
	{
		"What are the 3 most common decimal values in mint_info?",
		"mint_info",
		`SELECT decimals, COUNT(*) c FROM mint_info GROUP BY decimals ORDER BY c DESC LIMIT 3`,
	},
	{
		"How many orca_whirlpool_pool rows have zero on-chain liquidity?",
		"orca_whirlpool_pool",
		`SELECT COUNT(*) FROM orca_whirlpool_pool WHERE liquidity_hi = 0 AND liquidity_lo = 0`,
	},
	{
		"How many sanctum_lst entries have a nonzero sol_value?",
		"sanctum_lst",
		`SELECT COUNT(*) FROM sanctum_lst WHERE sol_value > 0`,
	},
	{
		"How many distinct lending markets does kamino_reserve span?",
		"kamino_reserve",
		`SELECT COUNT(DISTINCT lending_market) FROM kamino_reserve`,
	},
	{
		"Is jet_reserve empty?",
		"jet_reserve",
		`SELECT COUNT(*) FROM jet_reserve`,
	},
	{
		"How many phoenix_market rows are there, and what are their symbols?",
		"phoenix_market",
		`SELECT symbol FROM phoenix_market`,
	},
	{
		"Which raydium_clmm_pool has the highest liquidity_hi value?",
		"raydium_clmm_pool",
		`SELECT hex(pubkey), liquidity_hi FROM raydium_clmm_pool ORDER BY liquidity_hi DESC LIMIT 1`,
	},
}

func (r *HarnessCmd) Run(rc *RunConfig) error {
	_ = rc
	prefetchDB, err := store.Open(r.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open prefetch db: %s", err)
	}
	defer func() {
		_ = prefetchDB.Close()
	}()
	db := prefetchDB.DB()

	fmt.Printf("## Harness question set -- known-correct answers (%s)\n\n", r.DBPath)
	for i, q := range harnessQuestions {
		answer, err := runHarnessQuery(db, q.SQL)
		fmt.Printf("%d. %s\n", i+1, q.Question)
		if err != nil {
			fmt.Printf("   ERROR: %s\n\n", err)
			continue
		}
		fmt.Printf("   Answer: %s\n\n", answer)
	}
	return nil
}

// runHarnessQuery runs query and renders every row/column into one
// semicolon-joined string -- deliberately generic (any harness question's
// SQL, whatever shape its result set is) rather than one formatter per
// question. Reuses formatCellValue (cmd/explore.go) for base58/hex
// decoding of any raw pubkey BLOB a future question's SQL might select
// directly, though every question above already converts pubkeys to hex
// text itself via SQLite's hex().
func runHarnessQuery(db *sql.DB, query string) (string, error) {
	rows, err := db.Query(query)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = rows.Close()
	}()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	var lines []string
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = formatCellValue(v)
		}
		lines = append(lines, strings.Join(parts, " | "))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "(no rows)", nil
	}
	return strings.Join(lines, "; "), nil
}
