package suggest

import (
	"bytes"
	"encoding/json"
	"testing"
)

func runHook(t *testing.T, payload map[string]any) rewriteOutput {
	t.Helper()
	b, _ := json.Marshal(payload)
	var out bytes.Buffer
	if code := Run(nil, bytes.NewReader(b), &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got rewriteOutput
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("output not JSON: %v", err)
		}
	}
	return got
}

func TestRewriteAppliesUnderBypass(t *testing.T) {
	cases := map[string]string{
		"cat notes.txt | wc -l":                "< notes.txt wc -l",
		"cd /x && cat a.log | rg -n err":       "cd /x && < a.log rg -n err",
		"which python3":                        "command -v python3",
		"cat f | head -3; which go && echo ok": "< f head -3; command -v go && echo ok",
		"rg -rn TODO internal/":                "rg -n TODO internal/",
		"rg -i -rl foo . && echo done":         "rg -i -l foo . && echo done",
	}
	for in, want := range cases {
		got := runHook(t, map[string]any{
			"tool_name":       "Bash",
			"permission_mode": "bypassPermissions",
			"tool_input":      map[string]any{"command": in, "description": "d", "timeout": 5000},
		})
		h := got.HookSpecificOutput
		if h.PermissionDecision != "allow" || h.UpdatedInput["command"] != want {
			t.Errorf("%q: got decision %q command %v, want allow %q", in, h.PermissionDecision, h.UpdatedInput["command"], want)
		}
		if h.UpdatedInput["description"] != "d" || h.UpdatedInput["timeout"] != float64(5000) {
			t.Errorf("%q: other tool_input fields not carried: %v", in, h.UpdatedInput)
		}
	}
}

func TestRewriteBlocksOutsideBypass(t *testing.T) {
	got := runHook(t, map[string]any{
		"tool_name":       "Bash",
		"permission_mode": "default",
		"tool_input":      map[string]any{"command": "which python3"},
	})
	if got.HookSpecificOutput.PermissionDecision != "deny" || got.HookSpecificOutput.UpdatedInput != nil {
		t.Errorf("outside bypass a block rule still denies and rewrites nothing; got %+v", got.HookSpecificOutput)
	}
}

func TestRewriteBlocksWhenNoSafeRewrite(t *testing.T) {
	// git-add-all has no mechanical rewrite, so a command that also trips it is refused whole.
	got := runHook(t, map[string]any{
		"tool_name":       "Bash",
		"permission_mode": "bypassPermissions",
		"tool_input":      map[string]any{"command": "which python3 && git add -A"},
	})
	if got.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("a block with no safe rewrite stays a deny; got %q", got.HookSpecificOutput.PermissionDecision)
	}
}

func TestRewriteNeverEditsQuotedText(t *testing.T) {
	cmd := "cat a | grep x && git commit -m 'first; cat f | grep y'"
	got, ok := rewriteAll(cmd, uuocRewrite, "${1}< ${2} ${3}")
	want := "< a grep x && git commit -m 'first; cat f | grep y'"
	if !ok || got != want {
		t.Errorf("got %q (%v), want %q", got, ok, want)
	}
}

func TestRgHBlocksButNotInsideQuotes(t *testing.T) {
	if hits := blockHits(Match(`rg -oh 'https?://[^ ]+' a.md b.md`)); len(hits) != 1 || hits[0].Name != "rg-h-is-help" {
		t.Errorf("rg -oh should block; got %v", hits)
	}
	for _, c := range []string{
		`git commit -m 'rg -oh prints help, use rg -o -I'`,
		"git commit -F - <<'EOF'\nrg -oh PAT files prints usage\nEOF",
		`rg -h`,
		`rg -o -I PAT a.md b.md`,
	} {
		if hits := blockHits(Match(c)); len(hits) != 0 {
			t.Errorf("%q must not block; got %v", c, hits)
		}
	}
}

func TestPkillFullBlocksButNotInsideQuotes(t *testing.T) {
	if hits := blockHits(Match("pkill -f stope-api")); len(hits) != 1 || hits[0].Name != "pkill-f-self-match" {
		t.Errorf("pkill -f should block; got %v", hits)
	}
	if hits := blockHits(Match(`rg -c 'pkill -f' internal/`)); len(hits) != 0 {
		t.Errorf("a quoted mention must not block; got %v", hits)
	}
	got := runHook(t, map[string]any{
		"tool_name":       "Bash",
		"permission_mode": "bypassPermissions",
		"tool_input":      map[string]any{"command": "pkill -f stope-api"},
	})
	if got.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("pkill -f has no rewrite, so it denies even under bypass; got %q", got.HookSpecificOutput.PermissionDecision)
	}
}
