package sanctum

import (
	"fmt"
	"strings"
)

func (s *Sanctum) String() string {
	lines := make([]string, len(s.Lsts)+1)
	lines[0] = fmt.Sprintf("Sanctum %d LSTs", len(s.Lsts))
	for i, x := range s.Lsts {
		lines[i+1] = x.String()
	}
	return strings.Join(lines, "\n")
}

func (e *LstEntry) String() string {
	return fmt.Sprintf("...lst %s; calculator %s; sol_value %d", e.Mint, e.SolValueCalculator, e.SolValue)
}
