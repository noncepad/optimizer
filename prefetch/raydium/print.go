package raydium

import (
	"database/sql"
	"fmt"
	"io"
	"sort"

	"git.noncepad.com/pkg/optimizer/prefetch/raydium/amm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/clmm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/cpmm"
	sgo "github.com/gagliardetto/solana-go"
)

func (raydium *Raydium) String() string {
	out := "not yet implemented"
	return out
}

// PoolSummary is a unified, cross-program view of a single pool used for
// ranking. Liquidity is the sum of both vault balances in raw token units
// (no decimals or price normalization applied), so it is only a rough proxy
// for comparing pools that trade different token pairs.
type PoolSummary struct {
	Pubkey    sgo.PublicKey
	Program   string
	Liquidity uint64
}

// Stats is a count-based summary of the pools currently stored in a database.
type Stats struct {
	AmmCount  int
	CpmmCount int
	ClmmCount int
	// Top10 holds up to 10 pools with the highest Liquidity across all
	// three programs, sorted descending.
	Top10 []PoolSummary
}

// addTop inserts entry into list, keeps it sorted descending by Liquidity,
// and truncates it to n entries.
func addTop(list []PoolSummary, entry PoolSummary, n int) []PoolSummary {
	list = append(list, entry)
	sort.Slice(list, func(i, j int) bool { return list[i].Liquidity > list[j].Liquidity })
	if len(list) > n {
		list = list[:n]
	}
	return list
}

// Summarize reads every downloaded pool from db and returns pool counts per
// program plus the top 10 pools by liquidity across all three programs.
func Summarize(db *sql.DB) (*Stats, error) {
	s := &Stats{Top10: make([]PoolSummary, 0, 10)}

	err := amm.ReadPools(db, func(p *amm.PoolRow) bool {
		s.AmmCount++
		s.Top10 = addTop(s.Top10, PoolSummary{
			Pubkey:    p.Pubkey,
			Program:   "amm",
			Liquidity: p.CoinBalance + p.PcBalance,
		}, 10)
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("raydium summarize amm: %w", err)
	}

	err = cpmm.ReadPools(db, func(p *cpmm.PoolRow) bool {
		s.CpmmCount++
		s.Top10 = addTop(s.Top10, PoolSummary{
			Pubkey:    p.Pubkey,
			Program:   "cpmm",
			Liquidity: p.Token0Balance + p.Token1Balance,
		}, 10)
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("raydium summarize cpmm: %w", err)
	}

	err = clmm.ReadPools(db, func(p *clmm.PoolRow) bool {
		s.ClmmCount++
		s.Top10 = addTop(s.Top10, PoolSummary{
			Pubkey:    p.Pubkey,
			Program:   "clmm",
			Liquidity: p.Token0Balance + p.Token1Balance,
		}, 10)
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("raydium summarize clmm: %w", err)
	}

	return s, nil
}

// PrintSummary writes a human-readable summary of the pools stored in db to
// w, including the top 10 pools by liquidity.
func PrintSummary(db *sql.DB, w io.Writer) error {
	s, err := Summarize(db)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Raydium pool summary\n")
	fmt.Fprintf(w, "  amm  pools: %d\n", s.AmmCount)
	fmt.Fprintf(w, "  cpmm pools: %d\n", s.CpmmCount)
	fmt.Fprintf(w, "  clmm pools: %d\n", s.ClmmCount)
	fmt.Fprintf(w, "  total:      %d\n", s.AmmCount+s.CpmmCount+s.ClmmCount)
	fmt.Fprintf(w, "\nTop %d pools by liquidity (sum of both vault balances, raw token units):\n", len(s.Top10))
	for i, p := range s.Top10 {
		fmt.Fprintf(w, "  %2d. [%-4s] %s  liquidity=%d\n", i+1, p.Program, p.Pubkey, p.Liquidity)
	}
	return nil
}
