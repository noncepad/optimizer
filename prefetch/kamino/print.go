package kamino

import (
	"fmt"
	"strings"
)

func (k *Kamino) String() string {
	s := make([]string, len(k.Reserves)+1)
	s[0] = fmt.Sprintf("Kamino %d reserves", len(k.Reserves))
	for i, x := range k.Reserves {
		s[i+1] = x.String()
	}
	return strings.Join(s, "\n")
}

func (r *Reserve) String() string {
	return fmt.Sprintf("...reserve %s; mint %s; market %s", r.Pubkey, r.Mint, r.LendingMarket)
}
