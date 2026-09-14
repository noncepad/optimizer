package drift

import (
	"fmt"
	"strings"
)

func (d *Drift) String() string {
	s := make([]string, len(d.SpotMarkets)+1)
	s[0] = fmt.Sprintf("Drift %d spot markets", len(d.SpotMarkets))
	for i, x := range d.SpotMarkets {
		s[i+1] = x.String()
	}
	return strings.Join(s, "\n")
}

func (m *SpotMarket) String() string {
	return fmt.Sprintf("...spot market %s; mint %s; vault %s", m.Pubkey, m.Mint, m.Vault)
}
