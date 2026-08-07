package suggest

import (
	"os"
	"testing"
)

// TestSpanProbe is a debugging aid: WEIR_SPAN=<cmd> go test -run TestSpanProbe
func TestSpanProbe(t *testing.T) {
	cmd := os.Getenv("WEIR_SPAN")
	if cmd == "" {
		t.Skip("set WEIR_SPAN")
	}
	for _, r := range Rules {
		if loc := r.Pattern.FindStringIndex(cmd); loc != nil {
			sup := ""
			if r.Suppress != nil && r.Suppress.MatchString(cmd) {
				sup = "  [SUPPRESSED]"
			}
			t.Logf("%-32s [%d:%d] %q%s", r.Name, loc[0], loc[1], cmd[loc[0]:loc[1]], sup)
		}
	}
}
