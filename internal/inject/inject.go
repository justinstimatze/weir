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
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/justinstimatze/weir/internal/guard"
	"github.com/justinstimatze/weir/internal/idioms"
	"github.com/justinstimatze/weir/internal/probe"
	"github.com/justinstimatze/weir/internal/rulehistory"
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

// GotchaBudgetChars caps the bytes spent on the gotcha section, mirroring
// IdiomBudgetChars/CompositionBudgetChars. Belt-and-suspenders: this table
// grew from 4 to 11 entries across a handful of sessions with nothing
// capping it, and the terse-Line convention on the gotcha struct only
// holds if future entries stay terse. 3000 leaves headroom for several
// more terse entries plus the one deliberately long exception
// (command-not-found) before truncation engages.
const GotchaBudgetChars = 3000

// Render builds the additionalContext string from the probe manifest +
// the embedded idiom corpus. Doesn't perform I/O — easy to test.
//
// hist and mode control gotcha muting (see renderGotchas): hist is this
// project's rule-fire history from internal/rulehistory, mode is the
// resolved WEIR_GOTCHAS value ("always"/"never"/"auto").
func Render(m probe.Manifest, c *idioms.Corpus, hist rulehistory.Status, mode string) string {
	var b strings.Builder
	renames := renameMap(m.Present)

	// --- present tools (one line each, sorted by name) ---
	b.WriteString("[weir] Modern shell tools on this host:")
	if len(m.Present) == 0 {
		b.WriteString(" (none beyond coreutils)")
	} else {
		present := append([]probe.Entry(nil), m.Present...)
		sort.Slice(present, func(i, j int) bool { return present[i].Name < present[j].Name })
		for _, e := range present {
			name, note := invocableName(e)
			if e.Replaces == "" {
				fmt.Fprintf(&b, "\n- %s (additive%s) -> %s", name, note, e.Path)
			} else {
				fmt.Fprintf(&b, "\n- %s (prefer over %s%s) -> %s", name, e.Replaces, note, e.Path)
			}
		}
	}

	// --- apt install line for absent-but-available tools ---
	if pkgs := m.AbsentAptPkgs(); len(pkgs) > 0 {
		b.WriteString("\n\nMissing but installable from stock apt:")
		fmt.Fprintf(&b, "\n  sudo apt install %s", strings.Join(pkgs, " "))
		b.WriteString("\n\nEncourage the user to install these when relevant tasks come up. Once installed, weir picks them up at the next session. Note: on Debian/Ubuntu, fd and bat install under different binary names; the manifest above prints the invocable name for anything already present, but a freshly installed one will not appear there until the next session.")
	}

	b.WriteString("\n\nWhen the modern tool fits, prefer it over the classic listed under `replaces`. Entries with kind=function/alias are shell shims (may not behave as the underlying tool — verify before relying on them).")

	// --- idiom block (per-tool, from tldr-pages) ---
	if c != nil && len(c.Idioms) > 0 {
		idiomBlock := renderIdioms(m.Present, c, renames)
		if idiomBlock != "" {
			b.WriteString("\n\n[weir] Idiomatic uses (top ")
			fmt.Fprintf(&b, "%d", IdiomsPerTool)
			b.WriteString(" per installed tool, from tldr-pages):\n")
			b.WriteString(idiomBlock)
		}
	}

	// --- composition block (cross-tool, goal -> pipeline) ---
	if c != nil && len(c.Compositions) > 0 {
		compBlock := renderCompositions(m.Present, c, renames)
		if compBlock != "" {
			b.WriteString("\n\n[weir] Composition idioms (goal -> pipeline, filtered to installed tools):\n")
			b.WriteString(compBlock)
		}
	}

	// --- silent-failure gotchas (present-tool filtered) ---
	if gotchas := renderGotchas(m.Present, renames, hist, mode); gotchas != "" {
		b.WriteString("\n\n[weir] Silent-failure gotchas (classic-tool habits that fail QUIET in the replacement — success with corrupted output, not a loud error):\n")
		b.WriteString(gotchas)
	}

	return b.String()
}

// invocableName renders the manifest's name column so that what the
// reader types is what the shell can run.
//
// The manifest normalizes to canonical upstream names, but Debian and
// Ubuntu ship fd and bat as `fdfind` and `batcat` to dodge a namespace
// clash, and the canonical name is then not on PATH at all. The old
// `name (why) -> path` layout put the invocable name in the column that
// looks like provenance, so the line read as "use the left one" while
// only the right one worked.
//
// The failure that produced this is the quietest one weir has on file.
// A session searched twice with `fd` under a directory whose contents it
// then reported as absent — `fd: command not found` went to stderr,
// `2>/dev/null` swallowed it because searches are noisy, and an empty
// result is what a search that finds nothing looks like. The files were
// at depth 1 of the directory it had just claimed to search. Reported
// 2026-08-05.
//
// Returns the name to type and a parenthetical note to append when it
// differs from the canonical one (empty when they agree).
func invocableName(e probe.Entry) (name, note string) {
	base := onDiskName(e)
	if base == e.Name {
		return e.Name, ""
	}
	return base, fmt.Sprintf("; upstream name %s, which is NOT on PATH — type `%s`", e.Name, base)
}

func onDiskName(e probe.Entry) string {
	base := e.Path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if base == "" {
		return e.Name
	}
	return base
}

// renameMap is canonical name -> on-disk name, for present tools where the
// two differ. Empty on a host that installs everything under its upstream
// name, which is the common case outside Debian and Ubuntu.
func renameMap(present []probe.Entry) map[string]string {
	var m map[string]string
	for _, e := range present {
		if base := onDiskName(e); base != e.Name {
			if m == nil {
				m = make(map[string]string, 2)
			}
			m[e.Name] = base
		}
	}
	return m
}

// renameBinaries rewrites canonical tool names to their on-disk names
// inside a command string. Fixing only the manifest line would leave the
// idiom and composition blocks directly beneath it still teaching a
// command that exits 127 — the manifest would say `fdfind` and the ten
// example pipelines under it would say `fd`.
//
// Only command positions are rewritten: start of string, after a
// backtick, after a pipe or statement separator, after `$(`, and after
// `-X` / `-exec`. That leaves prose mentions and flag values alone.
func renameBinaries(s string, renames map[string]string) string {
	for canonical, onDisk := range renames {
		re := renameRe(canonical)
		s = re.ReplaceAllString(s, "${1}"+onDisk)
	}
	return s
}

// renameGotchaLine rewrites a gotcha line for a renamed binary: the
// leading `name:` label, and any command inside backticks.
func renameGotchaLine(g gotcha, renames map[string]string) string {
	line := renameBinaries(g.Line, renames)
	if on, ok := renames[g.Tool]; ok {
		line = strings.Replace(line, g.Tool+":", on+":", 1)
	}
	return line
}

var renameReCache sync.Map // canonical name -> *regexp.Regexp

func renameRe(name string) *regexp.Regexp {
	if v, ok := renameReCache.Load(name); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile("(^|[`(|;&]\\s*|\\$\\(|\\s(?:-X|-exec)\\s+)" + regexp.QuoteMeta(name) + `\b`)
	renameReCache.Store(name, re)
	return re
}

// gotcha names a preferred tool whose failure mode from a specific
// classic-tool habit is SILENT (exit 0, wrong data) rather than loud
// (unknown flag, no output). Only surfaces if the tool is installed;
// an empty Tool means the hazard is in the shell itself and always
// surfaces.
//
// Bar to add here is high: the habit must be common AND the failure
// mode must be silent-with-corruption, not a mere loud error. Loud
// errors surface themselves; silent-success-with-wrong-data does not.
//
// Second bar, added 2026-07-25 after the sd in-place entry sat here for
// two releases and then failed to stop the loss it described: does this
// also need a PreToolUse rule? Prose delivered once at SessionStart is
// documentation. It cannot interrupt at the moment of danger, which is
// the whole job. Every entry below that describes a DESTRUCTIVE habit
// carries the matching rule's name in Rule; TestGotchaRulesExist keeps
// that reference honest at build time. Rule: "" is the explicit
// exception, not an oversight.
//
// Third bar, added 2026-08-31 after a token-cost audit found this section
// was over half of weir's entire SessionStart injection and the single
// biggest hook-cost line account-wide: Line should be TERSE — mechanism,
// one concrete failure shape, the fix. No incident narrative, no second
// example. The full story belongs in the matching Rule's Fix text, which
// only gets paid for on the turn the risky command is actually typed.
// SessionStart's job is priming, not the deep dive.
type gotcha struct {
	Tool string
	Line string
	// Rule names the internal/suggest.Rule that intercepts this habit at
	// the moment it's typed. Empty is permitted ONLY when the habit is
	// genuinely not regex-detectable — currently just command-not-found,
	// where this file's copy is the sole explanation that exists anywhere.
	Rule string
}

// Ordering note: if this list ever exceeds GotchaBudgetChars, renderGotchas
// truncates from the END of this slice. Insert new entries by severity,
// not by append-to-the-bottom, once that ever matters — it doesn't yet at
// today's content size.
var gotchas = []gotcha{
	{
		Tool: "rg",
		Rule: "rg-r-misfire",
		// Reported via dispatch 2026-07-23: someone lost an hour after
		// `rg -rn PATTERN path` (grep -rn muscle memory) rewrote every
		// match to the literal "n", quietly, on a live auth path. Full
		// story lives in rg-r-misfire's Fix text now — this is the radar
		// version.
		Line: "rg: `-r` is `--replace`, NOT recursive. `rg -rn PATTERN path` silently rewrites every match to \"n\", exit 0. Use `rg -n PATTERN path` (drop the `-r`).",
	},
	{
		Tool: "rg",
		Rule: "rg-ignore-file-hides-target",
		// Contributed by mars-1868431 via dispatch 2026-07-23 (v0.1.4
		// call for gotchas); reframed 2026-08-06 to lead with --no-ignore
		// instead of --hidden after the first framing caused a repeat
		// miss 17h later. Full story (the ~/.claude case, --debug check)
		// lives in rg-ignore-file-hides-target's Fix text.
		Line: "rg (and fd): honours .gitignore even with no git command involved — a deny-by-default ignore file returns zero matches, exit 1, indistinguishable from \"not there.\" `--hidden` does NOT fix this. Use `--no-ignore` (`-uu` covers both halves).",
	},
	{
		Tool: "rg",
		Rule: "rg-h-is-help",
		// Reported 2026-07-29 while extracting URLs from markdown files.
		// grep's -h is --no-filename; rg's -h is --help.
		Line: "rg: `-h` is `--help`, NOT `--no-filename` (that's `-I`/`-N`). `rg -oh PAT files` prints usage text and exits 0 — pattern and files never read. Use `rg -o -N PAT files`.",
	},
	{
		Tool: "fd",
		Rule: "rg-ignore-file-hides-target",
		// Same class as the rg-ignore case above, shared rule (pattern
		// covers rg/fd/fdfind together).
		Line: "fd: hides gitignored and hidden files BY DEFAULT (unlike `find`). Use `fd -HI PATTERN` (or `--hidden --no-ignore`) to include them.",
	},
	{
		Tool: "sd",
		Rule: "sd-replacement-shell-var",
		// Reported 2026-07-28, measured against sd 1.0.0. Orthogonal to
		// the in-place entry below: the write is intended, the CONTENT is
		// wrong.
		Line: "sd: the REPLACEMENT string has its own `$` grammar — `$NAME` is a capture-group reference, and an unmatched one expands to EMPTY, not an error. `sd 'x' 'a$HERE/b'` emits `a/b`, exit 0. Double the `$` for a literal (`$$NAME`), or use double quotes and let the shell expand it first.",
	},
	{
		Tool: "sd",
		Rule: "sd-in-place-write",
		// Sibling hazard to the rg case: `sd modifies files in-place by
		// default` (per `sd --help`), unlike sed which needs `-i`. This
		// is the entry that proved prose-alone doesn't stop the loss —
		// see the "Second bar" note on the struct above. 3 block
		// escalations exist as siblings in rules.go
		// (sd-in-place-write-secret-file/-redaction/-empty).
		Line: "sd: modifies files IN PLACE by default (no `-i` needed, unlike sed). `sd 'foo' 'bar' file.txt` overwrites immediately. Preview: `sd -p 'foo' 'bar' file.txt`. Stdout: `cat file.txt | sd 'foo' 'bar'`.",
	},
	{
		Tool: "sponge",
		Rule: "sponge-eats-failed-pipeline",
		// Reported 2026-08-07 after `grep -vxF "$LINE" "$M" | sponge "$M"`
		// took an auto-memory index to 0 bytes. Measured, not assumed:
		// `false | sponge m.md` takes a 17-byte file to 0 with exit 0.
		Line: "sponge: `cmd | sponge FILE` writes an EMPTY file if `cmd` fails — exit 0, no warning, original gone. Guard: `out=$(cmd) && printf '%s\\n' \"$out\" > FILE`.",
	},
	{
		Rule: "grep-dash-pattern",
		// Shell-level, so no Tool gate. Reported 2026-08-07 alongside the
		// sponge entry above; the two stacked into one loss.
		Line: "grep/rg: an OPERAND starting with `-` is parsed as an OPTION. `grep -E '-v' f` exits 1 with NO message (`-v` = invert-match) — reads as \"no matches\" instead of an error. Pass `--` first or name it with `-e`.",
	},
	{
		Rule: "pipe-eats-exit-status",
		// Shell-level, so no Tool gate. Reported 2026-08-06, three
		// sightings in two sessions, two with an explicit `echo "EXIT=$?"`
		// that printed the filter's status, not the command's.
		Line: "pipes: exit status is the LAST stage's — `cmd | tail`/`head`/`grep`/`rg` reports the trimmer's status, so a failed `git push` or `go install` piped through one reads as exit 0. Use `set -o pipefail` or check `${PIPESTATUS[0]}` immediately.",
	},
	{
		Rule: "pgrep-f-self-match",
		// Shell-level. Reported 2026-08-05 from a session that found two
		// of its own predecessors' wait loops still running, days old.
		// pkill-f-self-match is the sibling escalation in rules.go.
		Line: "pgrep/pkill: `-f` matches the FULL command line — including the `bash -c` shell running THIS command. A wait/kill loop on `-f` can match itself (silent hang, or self-kill). Match a PID instead: `cmd & PID=$!`, then `kill -0 $PID` / `kill $PID`.",
	},
	{
		Rule: "",
		// Not a silent-CORRUPTION case like the rest — the tool never
		// ran at all. Not regex-detectable (absence of a binary, not a
		// command shape), so this is the ONLY place this hazard is ever
		// explained — kept at full length deliberately, unlike the
		// terse entries above. Reported 2026-08-05.
		// Deliberately backtick-free where the canonical names appear:
		// renameBinaries rewrites a name that follows a backtick, so
		// "`fd` is fdfind here" would render as "fdfind is fdfind here".
		Line: "command-not-found is the QUIETEST failure a search has. A search that cannot run and a search that finds nothing both produce nothing on stdout, and every idiom around a search hides the difference — 2>/dev/null because searches are noisy, a head because they are long, a || echo none because empty is expected. Never turn an empty search result into \"the file does not exist\" without either checking the binary exists first (command -v) or dropping the stderr redirect. Names are the usual cause: the manifest above prints the invocable one, which on Debian and Ubuntu is not the upstream one.",
	},
}

// muteGotcha decides, under "auto" mode, whether g's SessionStart priming
// copy should be suppressed: only when its matching suggest.Rule has never
// fired in this project's own history AND that history has cleared
// rulehistory.MinEvidenceBashCalls. g.Rule == "" (command-not-found — not
// regex-detectable) is never muted, since no signal exists to mute it on.
// This never touches the live PreToolUse rule itself, which fires
// regardless of anything here.
func muteGotcha(g gotcha, hist rulehistory.Status, mode string) bool {
	switch mode {
	case "always":
		return false
	case "never":
		return g.Rule != ""
	default: // "auto"
		return g.Rule != "" && hist.Evidence && hist.Fired[g.Rule] == 0
	}
}

func renderGotchas(present []probe.Entry, renames map[string]string, hist rulehistory.Status, mode string) string {
	have := make(map[string]bool, len(present))
	for _, e := range present {
		have[e.Name] = true
	}
	var lines []string
	muted := 0
	for _, g := range gotchas {
		if g.Tool != "" && !have[g.Tool] {
			continue
		}
		if muteGotcha(g, hist, mode) {
			muted++
			continue
		}
		lines = append(lines, "- "+renameGotchaLine(g, renames))
	}

	var b strings.Builder
	truncated := false
	for _, line := range lines {
		if b.Len()+len(line)+1 > GotchaBudgetChars {
			truncated = true
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	if truncated {
		b.WriteString("\n- (gotcha list truncated to fit budget; see the `gotchas` var in internal/inject/inject.go, and the matching rule's Fix text in internal/suggest/rules.go, for the rest)")
	}
	// Muting must never be silent: a suppressed gotcha still leaves a
	// one-line trace so a session reading this output can notice and
	// override it, mirroring the truncation marker above.
	if muted > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "- (%d gotcha(s) muted: matching rule has never fired in this project's history; WEIR_GOTCHAS=always shows everything for one session)", muted)
	}
	return b.String()
}

// renderCompositions returns the bulleted cross-tool idiom list, filtered
// to entries whose tool deps are all present, capped at CompositionBudgetChars.
func renderCompositions(present []probe.Entry, c *idioms.Corpus, renames map[string]string) string {
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
		line := fmt.Sprintf("- %s: `%s`", m.Intent, renameBinaries(m.Cmd, renames))
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
func renderIdioms(present []probe.Entry, c *idioms.Corpus, renames map[string]string) string {
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
			label := e.Name
			if on, ok := renames[e.Name]; ok {
				label = on
			}
			lines = append(lines, fmt.Sprintf("- %s: `%s` — %s", label, renameBinaries(idiom.Cmd, renames), idiom.Intent))
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

// sessionStartInput is the subset of Claude Code's SessionStart hook stdin
// this cares about. Best-effort only: a decode failure or an empty/missing
// field is not an error here (unlike suggest's stricter hookInput decode) —
// inject's whole job is to render regardless of whether project identity
// resolves, so an undecodable payload just falls through to
// rulehistory.Load("", "") -> Status{Evidence:false} -> every gotcha shown.
type sessionStartInput struct {
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
}

// gotchaMode resolves WEIR_GOTCHAS to one of "always"/"never"/"auto".
// Unset or unrecognized falls to "auto" rather than erroring, so a typo
// fails toward full display, never toward silent suppression.
func gotchaMode() string {
	switch os.Getenv("WEIR_GOTCHAS") {
	case "always":
		return "always"
	case "never":
		return "never"
	default:
		return "auto"
	}
}

func cmdInjectInner(_ []string, in io.Reader, stdout io.Writer) int {
	if os.Getenv("WEIR_SKIP") != "" {
		return 0
	}
	var si sessionStartInput
	_ = json.NewDecoder(in).Decode(&si) // best-effort; zero value on any failure is fine
	mode := gotchaMode()
	var hist rulehistory.Status
	if mode == "auto" {
		hist = rulehistory.Load(si.TranscriptPath, si.CWD)
	}
	m := probe.Run()
	corpus, _ := idioms.Load() // OK if nil — Render handles it
	ctx := Render(m, corpus, hist, mode)
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
