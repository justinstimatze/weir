package suggest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Positive cases: each Cmd MUST match the named Rule (it may also match others).
// Corpus is mirrored from the historical Python prototype; kept here because
// every fixture is a real-world command we've observed (or a tight FP variant).
var positives = []struct {
	Cmd  string
	Want string
}{
	{"grep -i error /tmp/log | head -10", "grep-head-trim"},
	{"ls /tmp | grep .log", "ls-grep"},
	{"grep -i error /tmp/log | wc -l", "grep-wc"},
	{"ps aux | grep transcribe | grep -v grep | wc -l", "grep-wc"},
	{"cat /etc/hosts | grep localhost", "uuoc"},
	{"cat benchmark/results/summary.md | head -200", "uuoc"},
	{`find . -name '*.py' -exec wc -l {} \;`, "find-exec-semi"},
	{"sort file.txt | uniq", "sort-uniq"},
	{"awk '{print $1}' file | awk '{print $2}'", "awk-awk"},
	{"python3 deploy/eval.py 2>/tmp/eval-stderr2.log; echo '---'; cat /tmp/eval-stderr2.log | grep -i error | head -10", "uuoc"},
	{"which python3", "which-vs-command-v"},
	{"which python3 && python3 -V", "which-vs-command-v"},
	{"ls /tmp/foo && which convert", "which-vs-command-v"},
	{"ls /home/x/captions | wc -l", "ls-pipe-wc-l"},
	{"find . -name '*.go' | xargs grep -l 'foo'", "find-xargs-no-null"},
	// Promoted from pass B:
	{"head -n 100 file.log | tail -n 10", "head-tail-range"},
	{"head -100 file.log | tail -10", "head-tail-range"},
	{"head -n 1000 /var/log/app.log | tail -n 50", "head-tail-range"},
	{"ps aux | grep transcribe", "ps-grep-vs-pgrep"},
	{"ps -ef | grep python", "ps-grep-vs-pgrep"},
	{"ps aux | grep -E 'foo|bar' | grep -v grep", "ps-grep-vs-pgrep"},
	// git staging guard — broad forms that sweep in untracked files.
	{"git add -A", "git-add-all"},
	{"git add --all", "git-add-all"},
	{"git add .", "git-add-all"},
	{"git add ./", "git-add-all"},
	{"git add -A && git commit -m x", "git-add-all"},
	{"git add -v .", "git-add-all"},
	// rg -r misfire — silent data corruption from grep -rn muscle memory.
	{"rg -rn CRON_SECRET /tmp/t.txt", "rg-r-misfire"},
	{"rg -rn PATTERN .", "rg-r-misfire"},
	{"rg -r n /tmp/t.txt", "rg-r-misfire"},
	{"rg -rl PATTERN /tmp", "rg-r-misfire"},
	{"rg -ri PATTERN src/", "rg-r-misfire"},
}

// Negative cases: each MUST match NO rules.
var negatives = []string{
	"grep -m 10 -i error /tmp/log",
	"ls /tmp/*.log",
	"grep -c -i error /tmp/log",
	"grep -i error /tmp/log",
	`find . -name '*.py' -exec wc -l {} +`,
	"sort file.txt | uniq -c",
	"sort file.txt | uniq -d",
	"awk '{print}' file",
	"cat /etc/hosts",
	"cat /etc/hosts > /tmp/copy",
	"echo hello",
	"git log --oneline | head -20",
	// git staging guard negatives — explicit paths and tracked-only must NOT block.
	"git add server.py",
	"git add foo.py bar.py",
	"git add src/ docs/",
	"git add ./foo",
	"git add -u",
	"git add -p",
	"command -v python3",
	"bmg describe -intent 'assess which mode the lens used'",
	`echo 'which is best' > /tmp/x`,
	`find DIR -mindepth 1 -maxdepth 1 -printf '.\n' | wc -l`,
	`find . -name '*.go' -print0 | xargs -0 grep -l 'foo'`,
	`find . -name '*.go' -exec grep -l 'foo' {} +`,
	// head-tail negatives — should NOT fire across statements or without -n on both
	`cmd | head ; echo --- ; cmd | tail -10`,                   // two separate statements
	`go build ./... 2>&1 | head -20; go test ./... | tail -25`, // separate, semicolon
	`head file.txt | tail`,                                     // no numeric args on either
	// ps-grep negatives
	`pgrep -af python`,  // already using pgrep
	`ps aux | head -10`, // no grep
	`ps -p 1234`,        // ps without pipe
	// uuoc negatives — surfaced by the eval (mlr's `cat` verb was being
	// false-matched as coreutils cat; would have blocked legitimate mlr usage)
	`mlr --c2p cat /tmp/sales.csv | less -S`,
	`mlr --c2p cat /tmp/sales.csv | bat`,
	`awk cat /tmp/x.txt`, // hypothetical, but exercises the boundary
	// rg -r misfire negatives — the trap only fires on bare single-letter
	// replacements that match a common rg short flag; ordinary rg usage
	// and legitimate replacements must NOT match.
	`rg -n PATTERN file`,           // no -r
	`rg PATTERN file`,              // no flags at all
	`rg -r 'foo bar' file`,         // legit multi-char replacement, quoted
	`rg -r foo file`,               // legit multi-char replacement, unquoted
	`rg -r 'n' file`,               // legit single-letter replacement, quoted (advisory can't inspect quotes; pattern excludes because next char after -r space is ')
	`rg -e PATTERN -r replacement`, // legit long replacement
}

func names(rs []Rule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func TestPositives(t *testing.T) {
	for _, p := range positives {
		got := names(Match(p.Cmd))
		if !contains(got, p.Want) {
			t.Errorf("positive: %q\n  expected %q in matches; got %v", p.Cmd, p.Want, got)
		}
	}
}

func TestNegatives(t *testing.T) {
	for _, n := range negatives {
		got := names(Match(n))
		if len(got) > 0 {
			t.Errorf("negative: %q\n  expected no matches; got %v", n, got)
		}
	}
}

// TestRunBlockingPath: a blocked rule (which-vs-command-v) emits deny.
func TestRunBlockingPath(t *testing.T) {
	in := bytes.NewBufferString(`{"tool_name":"Bash","tool_input":{"command":"which python3"}}`)
	var out bytes.Buffer
	if code := Run(nil, in, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if out.Len() == 0 {
		t.Fatal("expected hook output, got empty")
	}
	var got blockOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if got.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("expected deny; got %q", got.HookSpecificOutput.PermissionDecision)
	}
	if !strings.Contains(got.HookSpecificOutput.PermissionDecisionReason, "which-vs-command-v") {
		t.Errorf("expected rule name in reason; got: %q", got.HookSpecificOutput.PermissionDecisionReason)
	}
}

// TestRunAdvisoryPath: a non-blocked rule (head-tail-range) emits additionalContext.
func TestRunAdvisoryPath(t *testing.T) {
	in := bytes.NewBufferString(`{"tool_name":"Bash","tool_input":{"command":"head -n 100 file | tail -n 10"}}`)
	var out bytes.Buffer
	if code := Run(nil, in, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if out.Len() == 0 {
		t.Fatal("expected hook output, got empty")
	}
	var got adviseOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if got.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("wrong hookEventName: %q", got.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(got.HookSpecificOutput.AdditionalContext, "head-tail-range") {
		t.Errorf("expected head-tail-range in context; got: %q", got.HookSpecificOutput.AdditionalContext)
	}
}

// Non-Bash tool calls should produce no output.
func TestRunSkipsNonBash(t *testing.T) {
	in := bytes.NewBufferString(`{"tool_name":"Read","tool_input":{"command":"which python3"}}`)
	var out bytes.Buffer
	Run(nil, in, &out)
	if out.Len() != 0 {
		t.Errorf("expected no output for non-Bash tool; got %q", out.String())
	}
}

// Malformed input should fail-open silently.
func TestRunFailOpen(t *testing.T) {
	in := bytes.NewBufferString(`not json at all`)
	var out bytes.Buffer
	if code := Run(nil, in, &out); code != 0 {
		t.Errorf("expected exit 0 on bad input; got %d", code)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output on bad input; got %q", out.String())
	}
}
