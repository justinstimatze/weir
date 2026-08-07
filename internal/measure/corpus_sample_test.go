package measure

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/justinstimatze/weir/internal/suggest"
)

// TestSampleFires is a scratch harness, not an assertion. It streams the
// whole transcript corpus and prints up to WEIR_SAMPLE_N distinct commands
// per rule so a human can eyeball the false-positive rate before shipping.
//
//	WEIR_SAMPLE=1 WEIR_SAMPLE_N=12 go test ./internal/measure -run TestSampleFires -v
//
// Set WEIR_SAMPLE_RULE to restrict to one rule.
func TestSampleFires(t *testing.T) {
	if os.Getenv("WEIR_SAMPLE") == "" {
		t.Skip("set WEIR_SAMPLE=1")
	}
	n := 8
	if v, err := strconv.Atoi(os.Getenv("WEIR_SAMPLE_N")); err == nil && v > 0 {
		n = v
	}
	only := os.Getenv("WEIR_SAMPLE_RULE")

	samples := map[string][]string{}
	seen := map[string]map[string]bool{}
	counts := map[string]int{}
	total := 0

	err := streamBash(func(cmd string) {
		total++
		for _, r := range suggest.Match(cmd) {
			if only != "" && r.Name != only {
				continue
			}
			counts[r.Name]++
			if seen[r.Name] == nil {
				seen[r.Name] = map[string]bool{}
			}
			// WEIR_SAMPLE_SPAN prints the substring the rule actually
			// matched rather than the whole command. The command view
			// answers "is this the kind of thing I meant to catch"; the
			// span view answers "why did it fire", which is the question
			// once a rule's count is higher than you expected.
			key := cmd
			if os.Getenv("WEIR_SAMPLE_SPAN") != "" {
				if loc := r.Pattern.FindStringIndex(cmd); loc != nil {
					key = cmd[loc[0]:loc[1]]
				}
			}
			key = strings.Join(strings.Fields(key), " ")
			if len(key) > 160 {
				key = key[:160] + "…"
			}
			if seen[r.Name][key] || len(samples[r.Name]) >= n {
				continue
			}
			seen[r.Name][key] = true
			samples[r.Name] = append(samples[r.Name], key)
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(samples))
	for k := range samples {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool { return counts[names[i]] > counts[names[j]] })

	fmt.Printf("corpus: %d bash calls\n", total)
	for _, name := range names {
		fmt.Printf("\n=== %s  (%d fires, %.3f%% of calls)\n", name, counts[name],
			100*float64(counts[name])/float64(total))
		for _, s := range samples[name] {
			fmt.Printf("    %s\n", s)
		}
	}
}
