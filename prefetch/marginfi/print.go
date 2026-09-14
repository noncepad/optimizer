package marginfi

import (
	"fmt"
	"strings"
)

func (m *MarginFi) String() string {
	s := make([]string, len(m.Banks)+1)
	s[0] = fmt.Sprintf("MarginFi %d banks", len(m.Banks))
	for i, x := range m.Banks {
		s[i+1] = x.String()
	}
	return strings.Join(s, "\n")
}

func (b *Bank) String() string {
	return fmt.Sprintf("...bank %s; mint %s; group %s; oracle_setup %d; oracle_key %s",
		b.Pubkey, b.Mint, b.Group, b.OracleSetup, b.OracleKey)
}
