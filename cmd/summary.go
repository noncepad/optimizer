package main

import (
	"database/sql"
	"fmt"
	"os"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/chainstate"
)

// SummaryCmd prints a human-readable overview of what download-arb has
// (and hasn't) populated into prefetch.db -- row counts, liquidity/balance
// coverage for the swap DEXes, and reference-data stats.
type SummaryCmd struct{}

func (r *SummaryCmd) Run(rc *RunConfig) error {
	dbPath := getDBFilePath()
	info, err := os.Stat(dbPath)
	if err != nil {
		return fmt.Errorf("failed to stat %s: %s", dbPath, err)
	}
	chainState, err := chainstate.Create(rc.Ctx, dbPath, state.Client{})
	if err != nil {
		return fmt.Errorf("failed to open prefetch db: %s", err)
	}
	defer func() {
		_ = chainState.Close()
	}()
	db := chainState.Database().DB()

	fmt.Printf(
		"## `%s` summary (last written %s, %d MB)\n\n",
		dbPath, info.ModTime().Format("2006-01-02 15:04"), info.Size()/(1024*1024),
	)

	if err = printSwapDexes(db); err != nil {
		return err
	}
	if err = printLendingProtocols(db); err != nil {
		return err
	}
	if err = printReferenceData(db); err != nil {
		return err
	}
	return nil
}

func printSwapDexes(db *sql.DB) error {
	fmt.Println("**Swap DEXes:**")
	fmt.Println("| Table | Rows | Notes |")
	fmt.Println("|---|---|---|")

	clmmTotal, clmmNonzero, err := countWithCondition(db, "raydium_clmm_pool", "liquidity_hi != 0 OR liquidity_lo != 0")
	if err != nil {
		return err
	}
	fmt.Printf("| `raydium_clmm_pool` | %d | %s have nonzero on-chain liquidity |\n",
		clmmTotal, pctOf(clmmNonzero, clmmTotal))

	orcaTotal, orcaNonzero, err := countWithCondition(db, "orca_whirlpool_pool", "liquidity_hi != 0 OR liquidity_lo != 0")
	if err != nil {
		return err
	}
	fmt.Printf("| `orca_whirlpool_pool` | %d | %s have nonzero liquidity |\n",
		orcaTotal, pctOf(orcaNonzero, orcaTotal))

	cpmmTotal, cpmmBal, err := countWithCondition(db, "raydium_cpmm_pool", "token0_balance IS NOT NULL")
	if err != nil {
		return err
	}
	fmt.Printf("| `raydium_cpmm_pool` | %d | only %d have vault balances fetched (rest still `NULL`) |\n",
		cpmmTotal, cpmmBal)

	ammTotal, ammBal, err := countWithCondition(db, "raydium_amm_pool", "coin_balance IS NOT NULL")
	if err != nil {
		return err
	}
	fmt.Printf("| `raydium_amm_pool` | %d | %d have balances fetched |\n", ammTotal, ammBal)

	clmmConfig, err := count(db, "raydium_clmm_config")
	if err != nil {
		return err
	}
	cpmmConfig, err := count(db, "raydium_cpmm_config")
	if err != nil {
		return err
	}
	fmt.Printf("| `raydium_clmm_config` / `raydium_cpmm_config` | %d / %d | fee-tier configs referenced by the pool tables |\n",
		clmmConfig, cpmmConfig)

	sanctumTotal, sanctumPriced, err := countWithCondition(db, "sanctum_lst", "sol_value > 0")
	if err != nil {
		return err
	}
	fmt.Printf("| `sanctum_lst` | %d | %d with a nonzero `sol_value` |\n", sanctumTotal, sanctumPriced)

	fmt.Println()
	return nil
}

func printLendingProtocols(db *sql.DB) error {
	fmt.Println("**Lending protocols:**")
	fmt.Println("| Table | Rows | Notes |")
	fmt.Println("|---|---|---|")

	kaminoTotal, err := count(db, "kamino_reserve")
	if err != nil {
		return err
	}
	kaminoMarkets, err := countDistinct(db, "kamino_reserve", "lending_market")
	if err != nil {
		return err
	}
	fmt.Printf("| `kamino_reserve` | %d | spans %d distinct lending markets |\n", kaminoTotal, kaminoMarkets)

	driftTotal, err := count(db, "drift_spot_market")
	if err != nil {
		return err
	}
	fmt.Printf("| `drift_spot_market` | %d | |\n", driftTotal)

	for _, t := range []string{"marginfi_bank", "solend_reserve", "jet_reserve"} {
		n, err := count(db, t)
		if err != nil {
			return err
		}
		note := "empty"
		if n > 0 {
			note = ""
		}
		fmt.Printf("| `%s` | %s | %s |\n", t, emptyBold(n), note)
	}

	fmt.Println()
	return nil
}

func printReferenceData(db *sql.DB) error {
	fmt.Println("**Reference data:**")
	fmt.Println("| Table | Rows |")
	fmt.Println("|---|---|")

	mintTotal, err := count(db, "mint_info")
	if err != nil {
		return err
	}

	rows, err := db.Query(`SELECT decimals, COUNT(*) c FROM mint_info GROUP BY decimals ORDER BY c DESC LIMIT 3`)
	if err != nil {
		return fmt.Errorf("query mint_info decimals: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var parts []string
	for rows.Next() {
		var decimals, c int
		if err = rows.Scan(&decimals, &c); err != nil {
			return fmt.Errorf("scan mint_info decimals: %w", err)
		}
		parts = append(parts, fmt.Sprintf("%d has %dk", decimals, c/1000))
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("iterate mint_info decimals: %w", err)
	}

	fmt.Printf("| `mint_info` | %d (decimals cache; most common: %s) |\n", mintTotal, joinComma(parts))
	fmt.Println()
	return nil
}

func count(db *sql.DB, table string) (int, error) {
	var n int
	if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

func countWithCondition(db *sql.DB, table, condition string) (total int, matching int, err error) {
	if total, err = count(db, table); err != nil {
		return 0, 0, err
	}
	if err = db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", table, condition)).Scan(&matching); err != nil {
		return 0, 0, fmt.Errorf("count %s where %s: %w", table, condition, err)
	}
	return total, matching, nil
}

func countDistinct(db *sql.DB, table, column string) (int, error) {
	var n int
	if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(DISTINCT %s) FROM %s", column, table)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count distinct %s.%s: %w", table, column, err)
	}
	return n, nil
}

func pctOf(part, total int) string {
	if total == 0 {
		return "0 (0%)"
	}
	return fmt.Sprintf("%d (%d%%)", part, part*100/total)
}

func emptyBold(n int) string {
	if n == 0 {
		return "**0**"
	}
	return fmt.Sprintf("%d", n)
}

func joinComma(parts []string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += ", "
		}
		s += p
	}
	return s
}
