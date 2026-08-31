package inject

import (
	"testing"

	"github.com/justinstimatze/weir/internal/suggest"
)

// TestGotchaRulesExist keeps the gotcha<->rule mapping — until now only a
// comment and a hand-checked table — honest at build time. A rename or
// removal on the rules.go side shows up here instead of rotting silently.
func TestGotchaRulesExist(t *testing.T) {
	known := make(map[string]bool, len(suggest.Rules))
	for _, r := range suggest.Rules {
		known[r.Name] = true
	}
	for _, g := range gotchas {
		if g.Rule == "" {
			continue // explicit exception (command-not-found)
		}
		if !known[g.Rule] {
			t.Errorf("gotcha %q references rule %q, which does not exist in suggest.Rules", g.Tool, g.Rule)
		}
	}
}
