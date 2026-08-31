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
	// alphabetical by CANONICAL name: bat, jq, rg. bat renders under its
	// on-disk name because the Debian package installs it as batcat.
	posB := strings.Index(got, "batcat (prefer over cat;")
	posJ := strings.Index(got, "jq (additive)")
	posR := strings.Index(got, "rg (prefer over grep)")
	if posB < 0 || posJ < 0 || posR < 0 {
		t.Fatalf("expected all three tools in render; got: %q", got)
	}
	if !(posB < posJ && posJ < posR) {
		t.Errorf("expected alphabetical order (bat < jq < rg); positions: bat=%d jq=%d rg=%d", posB, posJ, posR)
	}
}

// TestRenderInvocableName — when the on-disk binary name differs from the
// canonical one, the manifest must lead with the name that actually runs
// and say so. A session that read `fd (prefer over find) -> /usr/bin/fdfind`
// as "use fd" ran `fd: command not found` behind a `2>/dev/null` and twice
// reported files absent from a directory they were at depth 1 of.
func TestRenderInvocableName(t *testing.T) {
	m := probe.Manifest{
		Version: 2,
		Present: []probe.Entry{
			{Name: "fd", Replaces: "find", Kind: "file", Path: "/usr/bin/fdfind"},
			{Name: "rg", Replaces: "grep", Kind: "file", Path: "/usr/bin/rg"},
		},
	}
	got := Render(m, nil)
	if !strings.Contains(got, "- fdfind (prefer over find; upstream name fd, which is NOT on PATH — type `fdfind`)") {
		t.Errorf("expected fdfind to lead with the invocable name; got: %q", got)
	}
	// A tool whose names agree must not grow a note.
	if !strings.Contains(got, "- rg (prefer over grep) ->") {
		t.Errorf("expected rg to render unchanged; got: %q", got)
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

// TestRenderGotchasFilteredByPresentTool — a Tool-gated gotcha only
// surfaces when that tool is present; a Tool-less gotcha (command-not-found)
// always surfaces.
func TestRenderGotchasFilteredByPresentTool(t *testing.T) {
	m := probe.Manifest{
		Version: 2,
		// note: no sponge
	}
	got := renderGotchas(m.Present, nil)
	if strings.Contains(got, "sponge:") {
		t.Error("sponge gotcha surfaced despite sponge being absent")
	}
	if !strings.Contains(got, "command-not-found") {
		t.Error("Tool-less gotcha (command-not-found) should always surface")
	}

	m.Present = []probe.Entry{{Name: "sponge", Kind: "file", Path: "/usr/bin/sponge"}}
	got = renderGotchas(m.Present, nil)
	if !strings.Contains(got, "sponge:") {
		t.Error("sponge gotcha should surface once sponge is present")
	}
}

// TestRenderGotchasBudgetCap — when the gotcha list exceeds
// GotchaBudgetChars, the truncation marker must appear. gotchas is a
// package-level var rather than a parameter, so this substitutes synthetic
// oversized entries and restores the real table afterward.
func TestRenderGotchasBudgetCap(t *testing.T) {
	orig := gotchas
	defer func() { gotchas = orig }()

	huge := strings.Repeat("x", GotchaBudgetChars/5+10)
	gotchas = []gotcha{
		{Line: huge},
		{Line: huge},
		{Line: huge},
		{Line: huge},
		{Line: huge},
		{Line: huge},
	}

	got := renderGotchas(nil, nil)
	if !strings.Contains(got, "gotcha list truncated") {
		t.Errorf("expected truncation marker for over-budget list; got len=%d", len(got))
	}
}
