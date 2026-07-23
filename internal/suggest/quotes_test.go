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

func TestIsInsideShellString_Quotes(t *testing.T) {
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
		got := isInsideShellString(c.cmd, c.pos)
		if got != c.want {
			t.Errorf("%s (pos %d in %q): got %v, want %v", c.label, c.pos, c.cmd, got, c.want)
		}
	}
}

// TestIsInsideShellString_Heredocs — v0.1.4 addition. Bodies opened by
// `<<[-]?['"]?DELIM['"]?` and closed by a line equal to DELIM (dedented
// if `<<-`) suppress block-mode matches that land inside. Motivating case:
// aipotluckorg-3288452's `git commit -F -` heredoc whose commit-message
// prose contained the English word "which" three times, tripping
// which-vs-command-v's block-mode rule on a productive commit.
func TestIsInsideShellString_Heredocs(t *testing.T) {
	swap := func(s string) string { return strings.ReplaceAll(s, "WH1CH", "which") }
	cases := []struct {
		label  string
		cmd    string
		marker string
		want   bool
	}{
		{"plain heredoc body", "cmd <<EOF\nWH1CH is best\nEOF", "WH1CH", true},
		{"quoted delimiter (aipotluck's case)", "git commit -F - <<'EOF'\nfixed the bug WH1CH broke auth\nEOF", "WH1CH", true},
		{"double-quoted delimiter", "cat <<\"MSG\"\ncat the log\nMSG", "cat the", true},
		{"dedented heredoc, tab-prefixed body line", "cmd <<-EOF\n\tWH1CH fires\n\tEOF", "WH1CH", true},
		{"unterminated heredoc runs to EOF", "cmd <<EOF\nWH1CH never terminates", "WH1CH", true},
		{"position before heredoc opener is outside", "WH1CH python ; cmd <<EOF\nbody\nEOF", "WH1CH", false},
		{"position after heredoc terminator is outside", "cmd <<EOF\nbody\nEOF\n; WH1CH python", "WH1CH python", false},
	}
	for _, c := range cases {
		cmd := swap(c.cmd)
		marker := swap(c.marker)
		pos := strings.Index(cmd, marker)
		if pos < 0 {
			t.Fatalf("%s: marker %q not found", c.label, marker)
		}
		if got := isInsideShellString(cmd, pos); got != c.want {
			t.Errorf("%s: got %v, want %v\ncmd: %q\npos: %d", c.label, got, c.want, cmd, pos)
		}
	}
}

// TestHerestringNotAHeredoc — `<<<` (bash herestring) must not open a
// heredoc scan. Regression guard for the `<<<` skip path in findHeredocs.
func TestHerestringNotAHeredoc(t *testing.T) {
	// Two `which`es: one inside the herestring arg (a quoted string, so
	// suppressed via quotes), one after a newline on the next command
	// (must NOT be suppressed as if the herestring had opened a heredoc).
	cmd := strings.ReplaceAll("cmd <<< 'WH1CH_arg'\nWH1CH python", "WH1CH", "which")
	second := strings.LastIndex(cmd, "which")
	if isInsideShellString(cmd, second) {
		t.Errorf("second `which` (after herestring on next line) should NOT be flagged as inside a shell string; cmd=%q", cmd)
	}
}
