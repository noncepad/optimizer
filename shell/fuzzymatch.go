package shell

import (
	"strings"

	"github.com/adrg/strutil"
	"github.com/adrg/strutil/metrics"
)

type FuzzyMatcher struct {
	listTarget []string
}

func CreateFuzzy(listTarget []string) *FuzzyMatcher {
	list := make([]string, len(listTarget))
	copy(list[:], listTarget[:])
	return &FuzzyMatcher{listTarget: list}
}

func (fm *FuzzyMatcher) Match(arg string) string {
	target, _ := findBestMatch(arg, fm.listTarget)
	return target
}

func findBestMatch(target string, choices []string) (string, float64) {
	metric := metrics.NewJaroWinkler()

	normalizedTarget := normalize(target)
	bestMatch := ""
	bestScore := -1.0

	for _, choice := range choices {
		normalizedChoice := normalize(choice)

		// Calculate similarity score between 0.0 and 1.0
		score := strutil.Similarity(normalizedTarget, normalizedChoice, metric)

		if score > bestScore {
			bestScore = score
			bestMatch = choice
		}
	}

	return bestMatch, bestScore
}

// Normalize converts spaces/underscores/hyphens so "raydium clmm pool"
// and "raydium_clmm" format similarly before matching.
func normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	return strings.TrimSpace(s)
}
