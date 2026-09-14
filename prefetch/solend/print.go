package solend

import (
	"fmt"
	"strings"
)

func (s *Solend) String() string {
	ss := make([]string, len(s.Reserves)+1)
	ss[0] = fmt.Sprintf("Solend %d reserves", len(s.Reserves))
	for i, x := range s.Reserves {
		ss[i+1] = x.String()
	}
	return strings.Join(ss, "\n")
}

func (r *Reserve) String() string {
	return fmt.Sprintf("...reserve %s; mint %s; market %s", r.Pubkey, r.Mint, r.LendingMarket)
}
