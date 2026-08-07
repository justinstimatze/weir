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
	if gotchas := renderGotchas(m.Present, renames); gotchas != "" {
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
// now has a matching rule in internal/suggest/rules.go.
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
		Tool: "rg",
		// Contributed by mars-1868431 via dispatch 2026-07-23 (v0.1.4
		// call for gotchas): rg respects .gitignore and skips hidden
		// files by default, so it silently under-matches vs `grep -r`.
		//
		// Reframed 2026-08-06 after the entry was read at the top of a
		// session and the mistake made anyway seventeen hours later. The
		// remedy it gave (-uu) was right; the framing was what failed.
		// Leading with hidden files and citing .env made it read as a
		// rule about dotfiles, and the natural narrower fix for a dotfile
		// problem is `--hidden` — which does nothing here, because rg
		// descends into a hidden directory fine when you name it as an
		// explicit path argument. The decisive flag is `--no-ignore`.
		// So: lead with the ignore half, and name the deny-by-default
		// tree that every agent grepping its own history walks into.
		Line: "rg: honours .gitignore even when no git command is involved — a deny-by-default ignore file makes an ENTIRE TREE return zero matches with exit 1, indistinguishable from \"the string is not there.\" `--hidden` does NOT fix this. The flag is `--no-ignore`; `-uu` covers both halves. `~/.claude` is deny-by-default (transcripts and a credentials file, correctly kept out of git — which silently made them un-searchable too), so any search under it needs `-uu`. Confirm what got skipped with `rg --debug ... 2>&1 | rg ignoring`.",
	},
	{
		Tool: "rg",
		// Reported 2026-07-29 while extracting URLs from markdown files.
		// In grep, `-h` is --no-filename and `grep -oh PAT f1 f2` is the
		// standard "just the matches" idiom. In rg, `-h` is --help.
		Line: "rg: `-h` is `--help`, NOT `--no-filename` (that is `-I` or `-N`). `rg -oh PAT files` prints the usage text on stdout and exits 0 — the pattern and file list are never read. Piped into `sort -u | head`, which is what an extraction command does, you get a tidy sorted list of flag descriptions that reads as a clean result set. Use `rg -o -N PAT files`.",
	},
	{
		Tool: "fd",
		// Same class as the rg-ignore case: `fd` also hides gitignored
		// and hidden files by default (find does not). Silent under-match
		// when searching for a file that lives in an ignored path.
		Line: "fd: hides gitignored and hidden files BY DEFAULT (unlike `find`). Silently under-matches when the target lives in an ignored/hidden path. Use `fd -HI PATTERN` (or `--hidden --no-ignore`) to include them.",
	},
	{
		Tool: "sd",
		// Reported 2026-07-28, measured against sd 1.0.0. Orthogonal to
		// the in-place entry below: the write is intended, the CONTENT is
		// wrong. Listed here because the sed reflex that makes `$` safe
		// is the reflex that breaks it in sd.
		Line: "sd: the REPLACEMENT string has its own `$` grammar — `$NAME` is a named capture-group reference, and an unmatched reference expands to EMPTY instead of erroring. `sd 'x' 'a$HERE/b'` emits `a/b`, exit 0. This inverts the sed habit: single quotes are what make `$FOO` literal in sed, because sed has no `$` grammar of its own; in sd the quotes stop the shell and then sd interpolates anyway. Double the `$` for a literal (`$$NAME`), or use double quotes and let the shell expand it first.",
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
	{
		Tool: "sponge",
		// Reported 2026-08-07 after `grep -vxF "$LINE" "$M" | sponge "$M"`
		// took an auto-memory index to 0 bytes. Measured, not assumed:
		// `false | sponge m.md` takes a 17-byte file to 0 with exit 0.
		Line: "sponge: `cmd | sponge FILE` writes an EMPTY file if cmd fails. sponge commits whatever the pipeline produced, and a failed command produces nothing — exit 0, no warning, original gone. This bites hardest because sponge is reached for precisely when the target and the source are the SAME file, which is when an empty write is unrecoverable. Guard it: `out=$(cmd) && printf '%s\\n' \"$out\" > FILE`, or write a temp path and `mv` after checking the status.",
	},
	{
		// Shell-level, so no Tool gate. Reported 2026-08-07 alongside the
		// sponge entry above; the two stacked into one loss. A pattern
		// beginning with `-` is parsed as an option bundle: `grep -vxF
		// "- [Leave it]" FILE` exits 2 with a usage error and no matches.
		// True of GNU grep; on hosts where `grep` is ugrep the error text
		// is unfamiliar enough to read as a different failure.
		Line: "grep/rg: an OPERAND that starts with `-` is parsed as an OPTION, whether it is your pattern or your filename. Two outcomes and the second is the quiet one: `grep -E '--- FAIL' f` exits 2 with a usage error, but `grep -E '-v' f` exits 1 with NO message — `-v` is a valid flag, so grep inverts the match instead of searching for the literal text, and the empty result reads as \"no matches\". Markdown list items, diff lines, arrows (`->`), and flag names all start with `-`. Pass `--` first (`grep -- \"$PATTERN\" FILE`) or name it with `-e`.",
	},
	{
		// Shell-level, so no Tool gate. Reported 2026-08-06, three
		// sightings in two sessions. The first was a background `git
		// push` that reported exit code 0 on a rejected push and was
		// caught minutes later by comparing rev-parse against the remote.
		// Two of the three had an EXPLICIT `echo "EXIT=$?"` written right
		// after the pipeline — the author was deliberately printing the
		// status of the thing they cared about, and got the filter's.
		Line: "pipes: a pipeline's exit status is the LAST stage's, so `cmd | tail`/`| head` reports the trimmer's success and a failed `cmd` reads as exit 0 — a rejected `git push` or a `go install` that printed \"does not contain package\" both report success to a `set -e` script, a CI step, or a background-task runner. `2>&1` does not help; it just gives `tail` more lines to discard. Use `set -o pipefail`, read `${PIPESTATUS[0]}` before anything else runs (including the `echo` that reads it), or redirect to a file instead of piping. `| grep`/`| rg` additionally exit 1 when nothing matched, so filtering a test run down to its failures reports FAILURE on the green run.",
	},
	{
		// Shell-level. Reported 2026-08-05 from a session that found two
		// of its own predecessors' wait loops still running, days old,
		// having outlived the job, the session, and every session since.
		Line: "pgrep/pkill: `-f` matches the FULL command line of every process, and Claude Code runs each Bash call as `bash -c '<the whole command>'` — so the pattern text is in the issuing shell's own cmdline and `-f` matches it. `until ! pgrep -f foo; do sleep 10; done` matches itself and never exits, silently and cheaply enough to go unnoticed for days; `pkill -f foo` kills its own shell. A PID cannot match itself: `cmd & PID=$!` then `kill -0 $PID` to wait, `kill $PID` to stop it.",
	},
	{
		// Not a silent-CORRUPTION case like the rest — the tool never
		// ran at all. It earns its place because the result is the most
		// ordinary thing a search produces (nothing), and every idiom
		// around a search hides the cause: `2>/dev/null` because searches
		// are noisy, `| head` because they are long, `|| echo none`
		// because empty is expected. Reported 2026-08-05 after a session
		// twice reported files absent from a directory they were at
		// depth 1 of.
		// Deliberately backtick-free where the canonical names appear:
		// renameBinaries rewrites a name that follows a backtick, so
		// "`fd` is fdfind here" would render as "fdfind is fdfind here".
		Line: "command-not-found is the QUIETEST failure a search has. A search that cannot run and a search that finds nothing both produce nothing on stdout, and every idiom around a search hides the difference — 2>/dev/null because searches are noisy, a head because they are long, a || echo none because empty is expected. Never turn an empty search result into \"the file does not exist\" without either checking the binary exists first (command -v) or dropping the stderr redirect. Names are the usual cause: the manifest above prints the invocable one, which on Debian and Ubuntu is not the upstream one.",
	},
}

func renderGotchas(present []probe.Entry, renames map[string]string) string {
	have := make(map[string]bool, len(present))
	for _, e := range present {
		have[e.Name] = true
	}
	var b strings.Builder
	for _, g := range gotchas {
		if g.Tool != "" && !have[g.Tool] {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(renameGotchaLine(g, renames))
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
