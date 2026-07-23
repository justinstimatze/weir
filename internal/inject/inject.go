// Package inject is weir's SessionStart hook handler. Renders the
// capability manifest + idioms as a hookSpecificOutput.additionalContext
// block. Replaces inject.sh.
//
// Layout of the injected text:
//
//	[weir] Modern shell tools on this host:
//	- <name> (prefer over <classic>) -> <path>           (or "(additive)" for tools with no classic)
//	... one line per present tool, sorted by name ...
//
//	Missing but installable from stock apt:
//	  sudo apt install <pkg1> <pkg2> ...
//
//	(prose footer about modern-tool preference, kind=function caveats)
//
//	[weir] Idiomatic uses (top 2 per installed tool, from tldr-pages):
//	- <name>: `<cmd>` — <intent>
//	... ordered: coreutils-replacers first, additive tools last; capped at IdiomBudgetChars ...
package inject

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/justinstimatze/weir/internal/guard"
	"github.com/justinstimatze/weir/internal/idioms"
	"github.com/justinstimatze/weir/internal/probe"
)

// IdiomBudgetChars caps the bytes spent on the idiom block. ~2000 chars
// ≈ ~500 tokens; bounds SessionStart cost predictably.
const IdiomBudgetChars = 2000

// IdiomsPerTool is the max number of idioms surfaced per installed tool.
const IdiomsPerTool = 2

type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// CompositionBudgetChars caps the bytes spent on the cross-tool
// composition block. Smaller than IdiomBudgetChars — these are denser
// per-line (full pipelines) and we don't want them to crowd out the
// per-tool idioms or the manifest.
const CompositionBudgetChars = 1500
const CompositionMax = 10

// Render builds the additionalContext string from the probe manifest +
// the embedded idiom corpus. Doesn't perform I/O — easy to test.
func Render(m probe.Manifest, c *idioms.Corpus) string {
	var b strings.Builder

	// --- present tools (one line each, sorted by name) ---
	b.WriteString("[weir] Modern shell tools on this host:")
	if len(m.Present) == 0 {
		b.WriteString(" (none beyond coreutils)")
	} else {
		present := append([]probe.Entry(nil), m.Present...)
		sort.Slice(present, func(i, j int) bool { return present[i].Name < present[j].Name })
		for _, e := range present {
			if e.Replaces == "" {
				fmt.Fprintf(&b, "\n- %s (additive) -> %s", e.Name, e.Path)
			} else {
				fmt.Fprintf(&b, "\n- %s (prefer over %s) -> %s", e.Name, e.Replaces, e.Path)
			}
		}
	}

	// --- apt install line for absent-but-available tools ---
	if pkgs := m.AbsentAptPkgs(); len(pkgs) > 0 {
		b.WriteString("\n\nMissing but installable from stock apt:")
		fmt.Fprintf(&b, "\n  sudo apt install %s", strings.Join(pkgs, " "))
		b.WriteString("\n\nEncourage the user to install these when relevant tasks come up. Once installed, weir picks them up at the next session. Note: on Debian/Ubuntu, fd and bat install as `fdfind` and `batcat`; weir's probe normalizes to canonical names in its manifest, but shell calls need the on-disk names (or a user alias).")
	}

	b.WriteString("\n\nWhen the modern tool fits, prefer it over the classic listed under `replaces`. Entries with kind=function/alias are shell shims (may not behave as the underlying tool — verify before relying on them).")

	// --- idiom block (per-tool, from tldr-pages) ---
	if c != nil && len(c.Idioms) > 0 {
		idiomBlock := renderIdioms(m.Present, c)
		if idiomBlock != "" {
			b.WriteString("\n\n[weir] Idiomatic uses (top ")
			fmt.Fprintf(&b, "%d", IdiomsPerTool)
			b.WriteString(" per installed tool, from tldr-pages):\n")
			b.WriteString(idiomBlock)
		}
	}

	// --- composition block (cross-tool, goal -> pipeline) ---
	if c != nil && len(c.Compositions) > 0 {
		compBlock := renderCompositions(m.Present, c)
		if compBlock != "" {
			b.WriteString("\n\n[weir] Composition idioms (goal -> pipeline, filtered to installed tools):\n")
			b.WriteString(compBlock)
		}
	}

	// --- silent-failure gotchas (present-tool filtered) ---
	if gotchas := renderGotchas(m.Present); gotchas != "" {
		b.WriteString("\n\n[weir] Silent-failure gotchas (classic-tool habits that fail QUIET in the replacement — success with corrupted output, not a loud error):\n")
		b.WriteString(gotchas)
	}

	return b.String()
}

// gotcha names a preferred tool whose failure mode from a specific
// classic-tool habit is SILENT (exit 0, wrong data) rather than loud
// (unknown flag, no output). Only surfaces if the tool is installed.
//
// Bar to add here is high: the habit must be common AND the failure
// mode must be silent-with-corruption, not a mere loud error. Loud
// errors surface themselves; silent-success-with-wrong-data does not.
type gotcha struct {
	Tool string
	Line string
}

var gotchas = []gotcha{
	{
		Tool: "rg",
		// Reported via dispatch 2026-07-23: someone lost an hour after
		// `rg -rn PATTERN path` (grep -rn muscle memory) rewrote every
		// match to the literal "n", quietly, on a live auth path.
		Line: "rg: `-r` is `--replace`, NOT recursive (rg recurses by default). `rg -rn PATTERN path` parses as `--replace=n` and silently rewrites every match to the literal \"n\" — exit 0, matching filenames and line numbers, no warning. Use `rg -n PATTERN path` (drop the `-r`).",
	},
	{
		Tool: "sd",
		// Sibling hazard to the rg case: `sd modifies files in-place by
		// default` (per `sd --help`). Sed habit is `sed 's/…/…/' file`
		// prints to stdout; `sed -i` writes in-place. In sd there is no
		// `-i` — the file arg IS the write target. So `sd 'foo' 'bar'
		// file.txt` you typed expecting a preview has already overwritten
		// file.txt. Different shape from rg -r (no flag reassignment,
		// just a different default), same class (silent success + wrong
		// file on disk).
		Line: "sd: modifies files IN PLACE by default (no `-i` needed, unlike sed). `sd 'foo' 'bar' file.txt` overwrites file.txt immediately. For a preview use `sd -p 'foo' 'bar' file.txt`; for stdout use `cat file.txt | sd 'foo' 'bar'`.",
	},
}

func renderGotchas(present []probe.Entry) string {
	have := make(map[string]bool, len(present))
	for _, e := range present {
		have[e.Name] = true
	}
	var b strings.Builder
	for _, g := range gotchas {
		if !have[g.Tool] {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(g.Line)
	}
	return b.String()
}

// renderCompositions returns the bulleted cross-tool idiom list, filtered
// to entries whose tool deps are all present, capped at CompositionBudgetChars.
func renderCompositions(present []probe.Entry, c *idioms.Corpus) string {
	have := make(map[string]bool, len(present))
	for _, e := range present {
		have[e.Name] = true
	}
	matches := c.CompositionsFor(have)
	if len(matches) > CompositionMax {
		matches = matches[:CompositionMax]
	}

	var b strings.Builder
	truncated := false
	for _, m := range matches {
		line := fmt.Sprintf("- %s: `%s`", m.Intent, m.Cmd)
		if b.Len()+len(line)+1 > CompositionBudgetChars {
			truncated = true
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	if truncated {
		b.WriteString("\n- (composition list truncated; see internal/idioms/composition.json for the rest)")
	}
	return b.String()
}

// renderIdioms returns the bulleted idiom list, ordered so coreutils-replacing
// tools come first (so the truncation cap clips additive idioms, not the
// load-bearing replacers).
func renderIdioms(present []probe.Entry, c *idioms.Corpus) string {
	ordered := append([]probe.Entry(nil), present...)
	sort.SliceStable(ordered, func(i, j int) bool {
		// non-empty Replaces sorts before empty Replaces (replacers first)
		if (ordered[i].Replaces != "") != (ordered[j].Replaces != "") {
			return ordered[i].Replaces != ""
		}
		return ordered[i].Name < ordered[j].Name
	})

	var lines []string
	for _, e := range ordered {
		got := c.For(e.Name, IdiomsPerTool)
		for _, idiom := range got {
			lines = append(lines, fmt.Sprintf("- %s: `%s` — %s", e.Name, idiom.Cmd, idiom.Intent))
		}
	}

	// Greedy fill under the char cap. If we hit the cap mid-list, append a
	// truncation marker so the model knows the list isn't exhaustive.
	var b strings.Builder
	truncated := false
	for _, line := range lines {
		// +1 for the trailing newline we'll add between entries
		if b.Len()+len(line)+1 > IdiomBudgetChars {
			truncated = true
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	if truncated {
		b.WriteString("\n- (idiom list truncated to fit budget; see internal/idioms/idioms.json for the rest)")
	}
	return b.String()
}

// CmdInject is the entry point for `weir inject`. Emits the SessionStart
// hookSpecificOutput JSON on stdout. Fail-open: any error -> exit 0,
// no output (Claude Code treats this as "no additionalContext"). Any panic
// is recovered by guard.Hook so the SessionStart hook never blocks Claude.
//
// Honors WEIR_SKIP=non-empty to suppress all output.
func CmdInject(args []string, in io.Reader, stdout io.Writer) int {
	return guard.Hook("inject", func() int { return cmdInjectInner(args, in, stdout) })
}

func cmdInjectInner(_ []string, _ io.Reader, stdout io.Writer) int {
	if os.Getenv("WEIR_SKIP") != "" {
		return 0
	}
	m := probe.Run()
	corpus, _ := idioms.Load() // OK if nil — Render handles it
	ctx := Render(m, corpus)
	if ctx == "" {
		return 0
	}
	var out hookOutput
	out.HookSpecificOutput.HookEventName = "SessionStart"
	out.HookSpecificOutput.AdditionalContext = ctx
	b, err := json.Marshal(out)
	if err != nil {
		return 0
	}
	stdout.Write(b)
	stdout.Write([]byte("\n"))
	return 0
}
