package orca

import (
	"fmt"
	"strings"
)

func (orca *Orca) String() string {
	s := make([]string, len(orca.Pools)+1)
	s[0] = fmt.Sprintf("Orca %d pools", len(orca.Pools))
	for i, x := range orca.Pools {
		s[i+1] = x.String()
	}
	return strings.Join(s, "\n")
}

func (w *Whirlpool) String() string {
	return fmt.Sprintf("...whirlpool %s; Vault (%s -> %s); sqrt %d/%d; price %f", w.Pubkey, w.VaultA, w.VaultB, w.SqrtPriceLo, w.SqrtPriceHi, w.SpotPrice())
}
