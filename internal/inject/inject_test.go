package inject

import (
	"strings"
	"testing"

	"github.com/justinstimatze/weir/internal/idioms"
	"github.com/justinstimatze/weir/internal/probe"
)

// TestRenderEmpty — no installed tools should produce a clean "(none beyond
// coreutils)" line, not a stray empty block.
func TestRenderEmpty(t *testing.T) {
	got := Render(probe.Manifest{Version: 2}, nil)
	if !strings.Contains(got, "(none beyond coreutils)") {
		t.Errorf("expected coreutils-only marker; got: %q", got)
	}
}

// TestRenderListsPresent — each present entry should appear with its path,
// in alphabetical name order regardless of input order.
func TestRenderListsPresent(t *testing.T) {
	m := probe.Manifest{
		Version: 2,
		Present: []probe.Entry{
			{Name: "rg", Replaces: "grep", Kind: "file", Path: "/usr/bin/rg"},
			{Name: "bat", Replaces: "cat", Kind: "file", Path: "/usr/bin/batcat"},
			{Name: "jq", Replaces: "", Kind: "file", Path: "/usr/bin/jq"},
		},
	}
	got := Render(m, nil)
	// alphabetical order: bat, jq, rg
	posB := strings.Index(got, "bat (prefer over cat)")
	posJ := strings.Index(got, "jq (additive)")
	posR := strings.Index(got, "rg (prefer over grep)")
	if posB < 0 || posJ < 0 || posR < 0 {
		t.Fatalf("expected all three tools in render; got: %q", got)
	}
	if !(posB < posJ && posJ < posR) {
		t.Errorf("expected alphabetical order (bat < jq < rg); positions: bat=%d jq=%d rg=%d", posB, posJ, posR)
	}
}

// TestRenderAptSuggestion — absent tools with apt packages should produce a
// `sudo apt install` line; nothing if no apt-installable absent tools.
func TestRenderAptSuggestion(t *testing.T) {
	m := probe.Manifest{
		Version: 2,
		Absent: []probe.Entry{
			{Name: "rg", Pkg: "ripgrep"},
			{Name: "watchexec", Pkg: ""}, // no pkg, skip
		},
	}
	got := Render(m, nil)
	if !strings.Contains(got, "sudo apt install ripgrep") {
		t.Errorf("expected apt install line for ripgrep; got: %q", got)
	}
	if strings.Contains(got, "watchexec") {
		t.Errorf("watchexec has no pkg; should NOT appear in apt line; got: %q", got)
	}
}

// TestRenderIdiomsFilteredByPresentTools — composition idioms should only
// surface when ALL their required tools are in `present`.
func TestRenderIdiomsFilteredByPresentTools(t *testing.T) {
	m := probe.Manifest{
		Version: 2,
		Present: []probe.Entry{
			{Name: "fd", Kind: "file", Path: "/usr/bin/fdfind"},
			// note: no rg
		},
	}
	c := &idioms.Corpus{
		Compositions: []idioms.Composition{
			{Intent: "find then grep", Cmd: "fd -X rg PATTERN", Tools: []string{"fd", "rg"}},
			{Intent: "fd only", Cmd: "fd PATTERN", Tools: []string{"fd"}},
			{Intent: "no deps", Cmd: "echo hi", Tools: nil},
		},
	}
	got := Render(m, c)
	if strings.Contains(got, "find then grep") {
		t.Error("composition requiring rg surfaced despite rg being absent")
	}
	if !strings.Contains(got, "fd only") {
		t.Error("composition requiring only fd should surface")
	}
	if !strings.Contains(got, "no deps") {
		t.Error("composition with no tool deps should always surface")
	}
}

// TestRenderIdiomsBudgetCap — when the composition list exceeds
// CompositionBudgetChars, the truncation marker must appear.
func TestRenderIdiomsBudgetCap(t *testing.T) {
	m := probe.Manifest{Version: 2}
	// Build a composition list whose combined length exceeds the cap
	// by stuffing it with deps-free entries.
	huge := strings.Repeat("x", CompositionBudgetChars/5+10)
	c := &idioms.Corpus{
		Compositions: []idioms.Composition{
			{Intent: huge, Cmd: huge, Tools: nil},
			{Intent: huge, Cmd: huge, Tools: nil},
			{Intent: huge, Cmd: huge, Tools: nil},
			{Intent: huge, Cmd: huge, Tools: nil},
			{Intent: huge, Cmd: huge, Tools: nil},
			{Intent: huge, Cmd: huge, Tools: nil},
		},
	}
	got := Render(m, c)
	if !strings.Contains(got, "composition list truncated") {
		t.Errorf("expected truncation marker for over-budget list; got len=%d", len(got))
	}
}
