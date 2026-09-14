package shell_test

import (
	"testing"

	"git.noncepad.com/pkg/optimizer/shell"
)

func TestFuzzyMatch(t *testing.T) {
	choices := []string{"orca_whirlpool", "raydium_clmm", "uniswap_v3", "meteora_dlmm"}
	arg := "raydium clmm pool"
	cf := shell.CreateFuzzy(choices)
	match := cf.Match(arg)
	t.Fatalf("Best Match: %s (Argument: %s)\n", match, arg)
	// Output: Best Match: raydium_clmm (Score: 0.8889)
}
