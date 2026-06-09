package suggest

import (
	"strings"
	"testing"
)

// Quote-aware suppression for block rules. Source: lucida-640513 report
// 2026-06-09, exact heredoc commit-body case + my probe cases.
//
// The literal "WH1CH" placeholders are swapped to "which" at runtime so
// THIS TEST FILE itself doesn't trip the live PreToolUse hook when the
// test binary is being built and run from a Claude Code session.
func TestQuoteAwareBlockSuppression(t *testing.T) {
	swap := func(s string) string { return strings.ReplaceAll(s, "WH1CH", "which") }
	cases := []struct {
		label    string
		cmd      string
		expBlock bool
	}{
		// True positives — must still block.
		{"bare top-level", `WH1CH python3`, true},
		{"after newline at start", "echo hi\nWH1CH python3", true},
		{"after && unquoted", `git diff && WH1CH python3`, true},
		{"after ; unquoted", `cd /tmp ; WH1CH python3`, true},

		// False-positive class lucida reported.
		{"in heredoc commit body", "git commit -m \"$(cat <<'EOF'\nfix bug\nWH1CH the block reads through is wrong\nEOF\n)\"", false},
		{"in double-quoted commit msg", `git commit -m "first thing; WH1CH approach to take"`, false},
		{"in double-quoted with &&", `git commit -m "step1 && WH1CH step2"`, false},
		{"in single-quoted commit msg", `git commit -m 'step1; WH1CH step2'`, false},

		// Boundary: nested-but-balanced quotes BEFORE the match should NOT suppress
		// (we count to even, so we're outside any quote).
		{"closed quote before real call", `echo "done" && WH1CH python3`, true},
	}
	for _, c := range cases {
		hits := Match(swap(c.cmd))
		blocked := false
		for _, h := range hits {
			if h.Name == "which-vs-command-v" && h.Action == "block" {
				blocked = true
				break
			}
		}
		if blocked != c.expBlock {
			t.Errorf("%s: expected block=%v, got %v (hits=%v)", c.label, c.expBlock, blocked, hits)
		}
	}
}

// Same coverage for the uuoc block rule — quotes should suppress it too.
func TestQuoteAwareBlockSuppressionUUOC(t *testing.T) {
	cases := []struct {
		label    string
		cmd      string
		expBlock bool
	}{
		{"true uuoc unquoted", `cat foo.txt | grep bar`, true},
		{"uuoc inside commit body", `git commit -m "ran cat foo.txt | grep bar before fix"`, false},
		{"uuoc after closed quote", `echo "done" && cat foo.txt | grep bar`, true},
	}
	for _, c := range cases {
		hits := Match(c.cmd)
		blocked := false
		for _, h := range hits {
			if h.Name == "uuoc" && h.Action == "block" {
				blocked = true
				break
			}
		}
		if blocked != c.expBlock {
			t.Errorf("%s: expected block=%v, got %v", c.label, c.expBlock, blocked)
		}
	}
}

func TestIsInsideShellQuotes(t *testing.T) {
	cases := []struct {
		label string
		cmd   string
		pos   int
		want  bool
	}{
		{"start", `hello`, 0, false},
		{"inside double", `echo "hi there"`, 8, true},
		{"after closed double", `echo "hi" world`, 12, false},
		{"inside single", `echo 'hi there'`, 8, true},
		{"escaped quote in double", `echo "she said \"hi\" then"`, 22, true},
		{"single inside double doesn't toggle", `echo "it's fine"`, 11, true},
		{"double inside single doesn't toggle", `echo 'say "hi"'`, 10, true},
	}
	for _, c := range cases {
		got := isInsideShellQuotes(c.cmd, c.pos)
		if got != c.want {
			t.Errorf("%s (pos %d in %q): got %v, want %v", c.label, c.pos, c.cmd, got, c.want)
		}
	}
}
