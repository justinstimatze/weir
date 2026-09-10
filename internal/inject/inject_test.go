package inject

import (
	"strings"
	"testing"

	"github.com/justinstimatze/weir/internal/idioms"
	"github.com/justinstimatze/weir/internal/probe"
	"github.com/justinstimatze/weir/internal/rulehistory"
)

// noHist/alwaysMode are what every pre-existing test below passes: mode
// "always" makes muteGotcha always return false regardless of hist, so
// these tests keep exercising exactly what they did before gotcha muting
// existed. Muting itself is covered separately below.
var noHist = rulehistory.Status{}

const alwaysMode = "always"

// TestRenderEmpty — no installed tools should produce a clean "(none beyond
// coreutils)" line, not a stray empty block.
func TestRenderEmpty(t *testing.T) {
	got := Render(probe.Manifest{Version: 2}, nil, noHist, alwaysMode)
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
	got := Render(m, nil, noHist, alwaysMode)
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
	got := Render(m, nil, noHist, alwaysMode)
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
	got := Render(m, nil, noHist, alwaysMode)
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
	got := Render(m, c, noHist, alwaysMode)
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
	got := Render(m, c, noHist, alwaysMode)
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
	got := renderGotchas(m.Present, nil, noHist, alwaysMode)
	if strings.Contains(got, "sponge:") {
		t.Error("sponge gotcha surfaced despite sponge being absent")
	}
	if !strings.Contains(got, "command-not-found") {
		t.Error("Tool-less gotcha (command-not-found) should always surface")
	}

	m.Present = []probe.Entry{{Name: "sponge", Kind: "file", Path: "/usr/bin/sponge"}}
	got = renderGotchas(m.Present, nil, noHist, alwaysMode)
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

	got := renderGotchas(nil, nil, noHist, alwaysMode)
	if !strings.Contains(got, "gotcha list truncated") {
		t.Errorf("expected truncation marker for over-budget list; got len=%d", len(got))
	}
}

// gotchaWithRule finds the live gotchas entry for a given rule name, so
// muting tests exercise the real table rather than a synthetic stand-in —
// the table's actual Tool gating matters for these cases (a Tool-gated
// entry must also pass the tool-presence filter to render at all).
func gotchaWithRule(t *testing.T, rule string) gotcha {
	t.Helper()
	for _, g := range gotchas {
		if g.Rule == rule {
			return g
		}
	}
	t.Fatalf("no gotcha entry with Rule=%q", rule)
	return gotcha{}
}

// firedForAllExcept returns a Fired map with count 1 for every rule-gated
// gotcha's Rule EXCEPT the ones listed, so a test can isolate "this one
// specific gotcha has zero evidence" without every other tool-less,
// always-eligible gotcha also going to zero and confounding the assertion.
func firedForAllExcept(except ...string) map[string]int {
	skip := make(map[string]bool, len(except))
	for _, r := range except {
		skip[r] = true
	}
	fired := map[string]int{}
	for _, g := range gotchas {
		if g.Rule == "" || skip[g.Rule] {
			continue
		}
		fired[g.Rule] = 1
	}
	return fired
}

// TestRenderGotchasAutoModeMutesOnZeroEvidence — a rule-gated gotcha whose
// rule has never fired in a project with enough history is muted in "auto"
// mode, and the breadcrumb reports it, while every other gotcha stays shown.
func TestRenderGotchasAutoModeMutesOnZeroEvidence(t *testing.T) {
	g := gotchaWithRule(t, "sponge-eats-failed-pipeline")
	m := probe.Manifest{Present: []probe.Entry{{Name: "sponge", Kind: "file", Path: "/usr/bin/sponge"}}}
	hist := rulehistory.Status{Evidence: true, Fired: firedForAllExcept("sponge-eats-failed-pipeline")}

	got := renderGotchas(m.Present, nil, hist, "auto")
	if strings.Contains(got, g.Line) {
		t.Errorf("expected sponge gotcha muted on zero evidence; got: %q", got)
	}
	if !strings.Contains(got, "1 gotcha(s) muted") {
		t.Errorf("expected a muted-count breadcrumb naming exactly 1; got: %q", got)
	}
}

// TestRenderGotchasAutoModeShowsWhenRuleFired — with every rule-gated
// gotcha's rule showing at least one fire, nothing is muted and there's no
// breadcrumb.
func TestRenderGotchasAutoModeShowsWhenRuleFired(t *testing.T) {
	g := gotchaWithRule(t, "sponge-eats-failed-pipeline")
	m := probe.Manifest{Present: []probe.Entry{{Name: "sponge", Kind: "file", Path: "/usr/bin/sponge"}}}
	hist := rulehistory.Status{Evidence: true, Fired: firedForAllExcept()}

	got := renderGotchas(m.Present, nil, hist, "auto")
	if !strings.Contains(got, g.Line) {
		t.Errorf("expected sponge gotcha shown when its rule has fired; got: %q", got)
	}
	if strings.Contains(got, "muted") {
		t.Errorf("expected no muted breadcrumb when nothing is muted; got: %q", got)
	}
}

// TestRenderGotchasAutoModeShowsWithoutEvidence — below the evidence bar,
// nothing is muted regardless of Fired, matching the safe cold-start default.
func TestRenderGotchasAutoModeShowsWithoutEvidence(t *testing.T) {
	g := gotchaWithRule(t, "sponge-eats-failed-pipeline")
	m := probe.Manifest{Present: []probe.Entry{{Name: "sponge", Kind: "file", Path: "/usr/bin/sponge"}}}
	hist := rulehistory.Status{Evidence: false, Fired: map[string]int{}}

	got := renderGotchas(m.Present, nil, hist, "auto")
	if !strings.Contains(got, g.Line) {
		t.Errorf("expected sponge gotcha shown when evidence bar isn't cleared; got: %q", got)
	}
}

// TestRenderGotchasNeverModeMutesRegardlessOfEvidence — "never" mutes every
// rule-gated gotcha even with a fresh, non-zero fire count.
func TestRenderGotchasNeverModeMutesRegardlessOfEvidence(t *testing.T) {
	g := gotchaWithRule(t, "sponge-eats-failed-pipeline")
	m := probe.Manifest{Present: []probe.Entry{{Name: "sponge", Kind: "file", Path: "/usr/bin/sponge"}}}
	hist := rulehistory.Status{Evidence: true, Fired: map[string]int{"sponge-eats-failed-pipeline": 5}}

	got := renderGotchas(m.Present, nil, hist, "never")
	if strings.Contains(got, g.Line) {
		t.Errorf("expected sponge gotcha muted in never mode; got: %q", got)
	}
	if !strings.Contains(got, "command-not-found") {
		t.Error("command-not-found (Rule==\"\") must never be muted, even in never mode")
	}
}

// TestRenderGotchasAlwaysModeIgnoresCache — "always" shows everything even
// against a cache claiming zero evidence anywhere.
func TestRenderGotchasAlwaysModeIgnoresCache(t *testing.T) {
	g := gotchaWithRule(t, "sponge-eats-failed-pipeline")
	m := probe.Manifest{Present: []probe.Entry{{Name: "sponge", Kind: "file", Path: "/usr/bin/sponge"}}}
	hist := rulehistory.Status{Evidence: true, Fired: map[string]int{}}

	got := renderGotchas(m.Present, nil, hist, "always")
	if !strings.Contains(got, g.Line) {
		t.Errorf("expected always mode to show every gotcha; got: %q", got)
	}
	if strings.Contains(got, "muted") {
		t.Errorf("expected no muted breadcrumb in always mode; got: %q", got)
	}
}
