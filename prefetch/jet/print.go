package jet

import (
	"fmt"
	"strings"
)

func (j *Jet) String() string {
	s := make([]string, len(j.Reserves)+1)
	s[0] = fmt.Sprintf("Jet %d reserves", len(j.Reserves))
	for i, x := range j.Reserves {
		s[i+1] = x.String()
	}
	return strings.Join(s, "\n")
}

func (r *Reserve) String() string {
	return fmt.Sprintf("...reserve %s; mint %s; market %s; vault %s", r.Pubkey, r.Mint, r.Market, r.Vault)
}
