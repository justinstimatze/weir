package suggest

import (
	"regexp"
	"runtime"
)

// Rule describes an antipattern detection: Pattern fires the rule; Suppress
// (optional) is an antidote — if it ALSO matches the same command, the rule
// is suppressed.
//
// Action controls how the suggester reacts when the rule fires:
//   - "advise" (default): emit a hookSpecificOutput.additionalContext suggestion;
//     the command still runs. Use for rules whose rewrites have edge cases.
//   - "block":  emit permissionDecision=deny with the fix text as reason;
//     Claude Code refuses to run the command, model retries with the suggestion.
//     Use ONLY for rules with mechanically-safe rewrites where blocking is
//     unambiguously the right move (no semantic risk, no UX surprise).
//
// v0 limitations documented in suggest.go package doc.
type Rule struct {
	Name     string
	Pattern  *regexp.Regexp
	Suppress *regexp.Regexp
	Fix      string
	Action   string // "" | "advise" (default) | "block"
	OS       string // "" (default, fires everywhere) | a runtime.GOOS value
}

// Shell-word fragments, shared by the rules that count positional arguments.
//
// shGap is the "flags and other noise" run between a binary name and its
// first positional argument. It must not cross a statement separator, a
// pipe, or a redirect.
//
// shWord is one positional argument: a quoted string, or a bare token
// containing NO shell metacharacter. The metacharacter exclusion is the
// whole point. The obvious spelling `[^\s'"]\S*` happily consumes `|`,
// `>` or `<` as if it were an argument, which lets an N-positional-arg
// pattern step across a pipe boundary and match a command that has
// nothing of the sort in it. That bug made a tag-stripping curl pipeline
// with an empty replacement, piped onward to head — the safe stdout form
// the sd rules recommend as the remedy — fire sd-in-place-write, by
// taking the second `|` as the file operand. Reported from
// two independent sessions (2026-07-28 twip, 2026-08-06 a sibling);
// the second one counted nine false positives in ten advisory fires and
// named this shape as five of them.
//
// shFile is the file-operand position: same idea, but it must start with
// a path-ish character so a flag or redirect can never land there.
//
// cmdPos anchors a binary name to a command position — statement start,
// or just after a newline, `;`, `&&`, `||`, or a pipe. RE2 has no
// look-behind, so the separator is consumed as part of the match. Short
// binary names need this: `\bsd\b` alone matches inside the identifier
// `sd-in-place-write`, so grepping the rule table for its own rule name
// fired the rule. Reported 2026-07-28 from twip, and reproduced here on
// the very command that went looking for it.
//
// redirGap is shGap's sibling for rules that must reach across a
// redirection to find a pipe. The usual `[^|\n;&]*` stops dead at the `&`
// in `2>&1`, which is present on almost every command whose output someone
// bothered to trim — so a gap that excludes `&` outright never sees the
// pipe in `git push origin main 2>&1 | tail -5`. This admits `&` only in
// the redirect forms (`2>&1`, `&>file`) and still stops at `&&`.
//
// Bounded, unlike the other gaps, because it is the only one that spans a
// whole command's argument list rather than a flag run. Unbounded it
// reached from a `git push` to a pipe 800 characters downstream in the
// same statement, which is not a pipeline the push is part of. 120 is
// past any real argument list and short of a paragraph.
const (
	shGap    = `[^|\n;&<>]*`
	shWord   = `(?:'[^']*'|"[^"]*"|[^\s'"|;&<>()][^\s|;&<>()]*)`
	shFile   = `(?:'[^']*'|"[^"]*"|[a-zA-Z0-9_./~][\w./~-]*)`
	cmdPos   = `(?:^|[;&\n]\s*|&&\s*|\|\|\s*|\|\s*)`
	redirGap = `(?:[^|\n;&]|&\d|&>){0,120}`
	// argGap is same-statement-only whitespace: the space before a flag, a
	// file operand, or the next positional argument. Go's \s includes \n,
	// so a rule using bare \s/\s+ in this position can match across a
	// newline into an unrelated next statement: `sd 'PAT' ''` on one line
	// followed by `echo something` on the next let \s+ consume the newline
	// and read `echo` as sd's FILE argument, firing sd-in-place-write-empty
	// on a command that never wrote anything. Found 2026-09-08, first in
	// the four sd-in-place-write* rules, then swept across the rest of
	// this file and found in twelve more (which-vs-command-v,
	// git-add-all, both rg-r-misfire rules, pkill-f-self-match,
	// rg-h-is-help, rg-cap-l-misfire, rg-ignore-file-hides-target,
	// grep-dash-pattern, both sd-replacement-shell-var rules) -- every one
	// confirmed to actually cross-match with a script, not assumed from
	// the shape alone.
	argGap = `[ \t]+`
)

// Rules is the live antipattern set. Edits here are the rule table.
//
// Inter-statement-separator-safe gaps use [^|\n;&], NOT [^|] — without that,
// multi-line commands like `... | head -20\necho ---\n... | tail -10` will
// false-match cross-statement "patterns" that aren't really there.
var Rules = []Rule{
	{
		Name:    "grep-head-trim",
		Pattern: regexp.MustCompile(`\bgrep\b[^|\n;&]*\|\s*head\b`),
		Fix:     "`grep PATTERN FILE | head -n N` -> `grep -m N PATTERN FILE` (or `rg -m N` with rg installed). Stops at the source instead of relying on the pipe for early-exit. Caveat: `-m N` caps PER FILE, `| head -N` caps TOTAL across all files — they diverge on multi-file/recursive searches.",
	},
	{
		Name:    "ls-grep",
		Pattern: regexp.MustCompile(`\bls\b[^|\n;&]*\|\s*grep\b`),
		Fix:     "`ls | grep PATTERN` -> use a glob (`ls *pattern*` or `*pattern*` directly) or `find -name PATTERN` / `fd PATTERN` for recursive. Skips ls's column formatting + the grep stage.",
	},
	{
		Name:    "grep-wc",
		Pattern: regexp.MustCompile(`\bgrep\b[^|\n;&]*\|\s*wc\s+-l\b`),
		Fix:     "`grep PATTERN | wc -l` -> `grep -c PATTERN` (or `rg -c PATTERN`). One process; works on streams too.",
	},
	{
		// Anchor `cat` to a statement boundary so `mlr cat FILE | ...` (cat is
		// mlr's verb, not coreutils) doesn't false-fire. The eval surfaced this
		// bug: with uuoc in block-mode, the model would be refused from a
		// CORRECT use of mlr that weir's own manifest was suggesting.
		//
		// `sd` deliberately NOT in the alternation: `cat FILE | sd PAT REP`
		// is the SAFE stdin form (writes stdout), and the uuoc rewrite
		// `sd PAT REP FILE` is the destructive in-place form the
		// sd-in-place-write rules now block. Two rules would fight.
		Name:    "uuoc",
		Pattern: regexp.MustCompile(`(?:^|[;&\n]\s*|&&\s*|\|\|\s*|\|\s*)cat\s+[^-\s]\S*\s*\|\s*(grep|head|tail|sed|awk|jq|less|more|wc|sort|uniq|mlr|bat|rg|fd|fzf)\b`),
		Fix:     "Useless use of cat — `cat FILE | TOOL` -> `TOOL FILE` (or `TOOL ARGS FILE`). grep/head/tail/sed/awk/jq/wc/sort/uniq/less/more all accept a file argument directly. Rewrite the command and retry.",
		Action:  "block",
	},
	{
		Name:    "find-exec-semi",
		Pattern: regexp.MustCompile(`\bfind\b[^|\n;&]*-exec\s+[^+]+\\;`),
		Fix:     "`find ... -exec CMD {} \\;` spawns one process per match. `-exec CMD {} +` batches — same semantics for grep/rm/chmod/wc/etc., far fewer execs.",
	},
	{
		// Go's RE2 doesn't support lookaround, so we keep the negative check
		// as a separate Suppress regex below.
		Name:     "sort-uniq",
		Pattern:  regexp.MustCompile(`\bsort\b[^|\n;&]*\|\s*uniq\b`),
		Suppress: regexp.MustCompile(`\buniq\s+-[a-zA-Z]*[cdu]\b`),
		Fix:      "`sort | uniq` -> `sort -u` (one pass, no second process). Keep the pipeline when you need `uniq -c` (count), `-d` (only dupes), or `-u` (only uniques).",
	},
	{
		Name:    "awk-awk",
		Pattern: regexp.MustCompile(`\bawk\b[^|\n;&]*\|\s*awk\b`),
		Fix:     "Two awks in a row can usually fuse into one — the second awk's actions become a follow-up block in the first. Advisory only; the correct fusion depends on the awk code.",
	},
	// --- Promoted from data/mine_extra.py pass A (2026-05-20) ---------------
	{
		// `which CMD` at statement start (^, ;, &&, ||, newline, after a pipe).
		// RE2 can't do "preceded by"; we anchor by including the separator in the
		// match (consumes one char of context but selftests still verify behavior).
		Name:    "which-vs-command-v",
		Pattern: regexp.MustCompile(cmdPos + `which` + argGap + `[A-Za-z_][\w.-]*`),
		Fix:     "`which CMD` -> `command -v CMD`. `which` is non-POSIX with inconsistent cross-distro behavior — can't see shell functions/aliases, exit codes vary. `command -v` is POSIX, sees functions/aliases, exits non-zero cleanly when missing. Rewrite the command and retry.",
		Action:  "block",
	},
	{
		Name:    "ls-pipe-wc-l",
		Pattern: regexp.MustCompile(`\bls\b[^|\n;&]*\|\s*wc\s+-l\b`),
		Fix:     "`ls | wc -l` overcounts on filenames with newlines and miscounts hidden entries depending on flags. Use `find DIR -mindepth 1 -maxdepth 1 -printf '.\\n' | wc -l`, or `arr=(DIR/*); echo ${#arr[@]}` with `shopt -s nullglob dotglob`.",
	},
	{
		Name:     "find-xargs-no-null",
		Pattern:  regexp.MustCompile(`\bfind\b[^|\n;&]*\|\s*xargs\b[^\n;&]*`),
		Suppress: regexp.MustCompile(`-print0|\bxargs\b[^\n;&]*\s-0\b`),
		Fix:      "`find ... | xargs CMD` splits on whitespace — filenames with spaces or newlines get mangled. Use `find ... -print0 | xargs -0 CMD`, or `find ... -exec CMD {} +`.",
	},
	// --- Promoted from data/mine_discover.py pass B (2026-05-20) -------------
	// Note: discovery's raw `head|tail` chain count of 238 was inflated by
	// multi-statement artifacts (cmd|head ; cmd|tail). The tight regex
	// (no statement separators in the gap) targets the real line-range
	// shape `head -nN FILE | tail -nM`. Real volume after the fix is closer
	// to 30-50 in the user's corpus, but the rule is sharp and the fix is
	// strictly better.
	{
		Name:    "head-tail-range",
		Pattern: regexp.MustCompile(`\bhead\b[^|\n;&]*?-n?\s*\d+[^|\n;&]*\|\s*tail\b[^|\n;&]*?-n?\s*\d+`),
		Fix:     "`head -n N FILE | tail -n M` to extract a line range reads to line N before discarding the prefix. `sed -n 'START,ENDp' FILE` or `awk 'NR>=START && NR<=END' FILE` reads only what's needed and exits at END.",
	},
	{
		// `ps aux | grep PATTERN` (and the variants `ps -ef | grep`, `ps | grep | grep -v grep`).
		// ~250 hits across variants in the user's corpus after the pass-B per-statement fix.
		Name:    "ps-grep-vs-pgrep",
		Pattern: regexp.MustCompile(`\bps\b[^|\n;&]*\|\s*grep\b`),
		// The "for killing, use pkill -f" clause this Fix used to carry was
		// itself the trap that pkill-f-self-match now flags — see the
		// self-match comment below. Recommending the safe form is the job;
		// recommending it in the tool's own remedy text is how the previous
		// uuoc/sd collision happened.
		Fix: "`ps aux | grep PATTERN` -> `pgrep -af PATTERN` for a one-shot look. Atomic, sees the full cmdline, handles empty matches cleanly, no `grep -v grep` self-match dance. To WAIT on or KILL something, don't reach for a pattern at all — capture the PID when you start the job (`cmd & PID=$!`) and use `kill -0 $PID` / `kill $PID`. Under Claude Code every Bash call runs as `bash -c '<the whole command>'`, so `pgrep -f`/`pkill -f` match the shell issuing them.",
	},
	// --- git staging guard -------------------------------------------------
	{
		// `git add -A` / `--all` / `.` / `./` stage every UNTRACKED file too — the
		// classic way stray build artifacts, debug dumps, or secrets sneak into a
		// commit. Block the broad forms; explicit `git add <path>` and `git add -u`
		// (restage tracked-only) are fine. The leading `\s` before the token avoids
		// matching the dot in `foo.py` or `--all` inside a path; `\./?` matches a
		// lone `.`/`./` but not `./foo` (an explicit path).
		Name:    "git-add-all",
		Pattern: regexp.MustCompile(`\bgit` + argGap + `add\b[^|\n;&]*?` + argGap + `(?:-A|--all|\./?)(?:\s|$)`),
		Fix:     "`git add -A` / `--all` / `.` stage EVERY untracked file too — stray build artifacts, debug dumps, or secrets slip into the commit. Stage explicit paths instead: `git add path/to/file ...`, or `git add -u` to restage only already-tracked changes. Rewrite with the specific paths and retry.",
		Action:  "block",
	},
	// --- silent-corruption trap: rg's -r is --replace, not recursive -------
	// In rg, `-r` is `--replace`, NOT recursive (rg recurses by default).
	// `rg -rn PATTERN path` — the natural `grep -rn` muscle-memory reach
	// — parses as `--replace=n` and rewrites every match to the literal
	// "n" with exit 0, matching filenames, and matching line numbers. No
	// warning of any kind. First reported via dispatch 2026-07-23 after an
	// hour lost misreading `env.CRON_SECRET` as `env.n` on a live auth path.
	// Aipotluck confirmed a variant on the same day: `-r ''` (empty replace)
	// silently strips every match from the output.
	//
	// v0.1.4 split the rule in two:
	//   - Bundled `-r[nliwcv]` BLOCKS. The bundle form has virtually no
	//     legitimate use — a real single-letter replacement is written
	//     `-r n` separated or `--replace=n`. Blocking the bundle is safe
	//     and stops the trap outright.
	//   - Separated `-r X` (X = single-letter, '', or "") advises. The
	//     separated form CAN be a legitimate replacement, so blocking
	//     would be worse than a warning.
	// Quoted replacements like `rg -r 'foo' file` do NOT match either
	// pattern — the char after `-r ` is `'`, not a bare letter or the
	// specific empty-string shape — so quote-aware suppression is not
	// needed here.
	{
		Name:    "rg-r-misfire-bundled",
		Pattern: regexp.MustCompile(`\brg\b[^|\n;&]*` + argGap + `-r[nliwcv]\b`),
		Fix:     "`rg -r[X]` (bundled) sets `--replace=X`, not recursion — rg recurses by default. `rg -rn PATTERN path` (grep -rn muscle memory) parses as `--replace=n` and silently rewrites every match to the literal \"n\" with exit 0. A real single-letter replacement is written `-r n` (separated) or `--replace=n`; the bundle form is virtually always the muscle-memory trap. Rewrite as `rg -n PATTERN path` (drop the `-r`) and retry.",
		Action:  "block",
	},
	{
		Name:    "rg-r-misfire",
		Pattern: regexp.MustCompile(`\brg\b[^|\n;&]*` + argGap + `-r` + argGap + `(?:[nliwcv]\b|''|"")`),
		Fix:     "`rg -r X` sets `--replace=X`, not recursion — rg recurses by default. `-r n` (or `-r ''` / `-r \"\"`) is a legitimate single-letter or empty-string replacement, but it's rare — the same shape shows up when someone reaches for grep-like recursion or trails a `-r` at the end of a command. Verify you meant to rewrite matches; if you're searching, drop the `-r` and use `rg -n PATTERN path`.",
	},
	// --- silent-corruption trap: sd writes IN PLACE by default ------------
	// Same class as rg-r-misfire, different mechanism: the sed muscle-memory
	// invocation `sed 'PAT' file` prints to stdout, and sed needs `-i` to
	// write in place. sd inverts that default: `sd PAT REP FILE` overwrites
	// FILE. There is no `-i` in sd — the file operand IS the write target.
	//
	// First reported via dispatch 2026-07-25 by cope-1184525 after
	// `sd '=.*' '=<set>' .env` overwrote a live ANTHROPIC_API_KEY with the
	// literal string "<set>" (file went 232 -> 129 bytes), recovered only
	// because they kept a separate key .txt out-of-band. Cope's diagnosis:
	// the v0.1.3 sd gotcha entry in inject.go carried the correct warning,
	// but was ambient prose delivered once at SessionStart — the PreToolUse
	// checker (this file) had no sd rule, so the destructive command sailed
	// through while `ls-pipe-wc-l` and `grep-head-trim` blocked style nits
	// on the same session. Severity ordering was inverted.
	//
	// Split into a base advisory + two block-mode escalations, matching the
	// rg-r-misfire shape:
	//   - Base: `sd PAT REP FILE` with no -p/--preview. Legitimate but
	//     easy-to-accidentally-persist, so advisory rather than block.
	//   - Escalation A (BLOCK): FILE matches a secret-ish pattern
	//     (.env, .pem, credential*, .key, .p12, .pfx). Overwriting these
	//     is close to unrecoverable in practice.
	//   - Escalation B (BLOCK): REPLACEMENT is redaction-shaped (<set>,
	//     <redacted>, ***, REDACTED). A user typing a redaction almost
	//     never wants it persisted — the replacement's SHAPE reveals the
	//     "for display, not disk" intent. Zero-FP by construction.
	//   - Escalation C (BLOCK): REPLACEMENT is the EMPTY string. Same
	//     shape-reveals-intent argument as B, one step stronger: `sd PAT ''
	//     FILE` does not substitute, it DELETES, and persisting a deletion
	//     is what "strip this out so I can look at the rest" turns into
	//     when the file operand is present. Added 2026-07-27 after a
	//     session ran `sd '^---[\s\S]*?---' '' "$f"` in a loop to strip
	//     frontmatter before a word count and blanked 11 tracked .mdoc
	//     files instead — every count came back 0, which is the only
	//     reason anyone noticed. Recovered via `git checkout --`.
	//
	//     That session had the base advisory fire and could not use it: for
	//     a destructive write the hook text arrives with the tool RESULT,
	//     after the loop has run. It also had the v0.1.3 SessionStart prose
	//     loaded. Neither is a mitigation — same inverted severity ordering
	//     cope hit, one tier up. If the replacement is empty and a file
	//     operand is present, refuse and make them say `-p` or pipe it.
	//
	// All four rules suppress on `-p` / `--preview` — the safe form.
	// Stdin form (`cat FILE | sd PAT REP`) has only 2 positional args and
	// doesn't match the 3-arg pattern.
	//
	// v0.1.6 rewrote all four onto the cmdPos/shGap/shWord/shFile
	// fragments after the field reported the base rule as the single
	// largest source of advisory noise. Two distinct bugs, both fixed
	// there rather than here: `\bsd\b` matched inside identifiers like
	// `sd-in-place-write`, and the old bare-token class `[^\s'"]\S*`
	// consumed `|` and `>` as positional arguments.
	{
		Name:     "sd-in-place-write",
		Pattern:  regexp.MustCompile(cmdPos + `sd\b` + shGap + argGap + shWord + argGap + shWord + argGap + shFile + `(?:\s|$)`),
		Suppress: regexp.MustCompile(`\bsd\b` + shGap + `\s(?:-p\b|--preview\b)`),
		Fix:      "sd writes to FILE IN PLACE — unlike sed, no `-i` is needed. `sd PAT REP FILE` overwrites FILE immediately. For a preview use `sd -p PAT REP FILE`; for stdout use `cat FILE | sd PAT REP`.",
	},
	{
		Name:     "sd-in-place-write-secret-file",
		Pattern:  regexp.MustCompile(cmdPos + `sd\b` + shGap + argGap + shWord + argGap + shWord + argGap + `[^\s|;&<>()]*(?:\.env\b|\.pem\b|credential|\.key\b|\.p12\b|\.pfx\b)`),
		Suppress: regexp.MustCompile(`\bsd\b` + shGap + `\s(?:-p\b|--preview\b)`),
		Fix:      "sd writes IN PLACE + you named a secret-ish file (.env, .pem, credentials, .key, .p12, .pfx). Overwriting the file destroys the real secret with the replacement string. If you meant to mask for DISPLAY, pipe to stdout: `cat FILE | sd PAT REP`. If you meant to persist, verify first with `sd -p PAT REP FILE`. Rewrite and retry.",
		Action:   "block",
	},
	{
		Name:     "sd-in-place-write-redaction",
		Pattern:  regexp.MustCompile(cmdPos + `sd\b` + shGap + argGap + shWord + argGap + `(?:'[^']*` + redactionAlt + `[^']*'|"[^"]*` + redactionAlt + `[^"]*"|[^\s'"|;&<>()]*` + redactionAlt + `[^\s|;&<>()]*)` + argGap + shFile),
		Suppress: regexp.MustCompile(`\bsd\b` + shGap + `\s(?:-p\b|--preview\b)`),
		Fix:      "sd writes IN PLACE + the replacement looks like a redaction pattern (<set>, <redacted>, ***, REDACTED). Users typing redactions almost never want them persisted to disk — the shape says \"for display\". Pipe to stdout instead: `cat FILE | sd PAT REP` (safe, no write). Rewrite and retry.",
		Action:   "block",
	},
	{
		Name:     "sd-in-place-write-empty",
		Pattern:  regexp.MustCompile(cmdPos + `sd\b` + shGap + argGap + shWord + argGap + `(?:''|"")` + argGap + shFile + `(?:\s|$)`),
		Suppress: regexp.MustCompile(`\bsd\b` + shGap + `\s(?:-p\b|--preview\b)`),
		Fix:      "sd writes IN PLACE + the replacement is EMPTY, so this DELETES the matched text from FILE rather than substituting anything. Stripping a section to read what's left is a display job: pipe it — `cat FILE | sd PAT ''` (safe, no write). To persist a deletion, confirm with `sd -p PAT '' FILE` first, or use an editor. Rewrite and retry.",
		Action:   "block",
	},
	// --- silent-corruption trap: sd's replacement has its own $ grammar ---
	// Orthogonal to the four in-place rules above: the write was intended,
	// the CONTENT is wrong. sd's replacement string reads `$NAME` as a
	// reference to a named capture group, and an unmatched reference
	// expands to EMPTY instead of erroring. So
	// `sd 'BIN=".*"' 'BIN="${TWIP_BIN:-$HERE/target/release/twip}"' f.sh`
	// writes `BIN=""` and exits 0.
	//
	// The sed muscle memory is not merely absent here, it is inverted.
	// Single-quoting is what makes `sed 's/x/$FOO/'` emit a literal
	// `$FOO` — the quotes stop the shell and sed has no `$` grammar of
	// its own. In sd the quotes still stop the shell and then sd
	// interpolates anyway, so the protective reflex produces the
	// opposite of its usual result. Reported 2026-07-28 from twip
	// against sd 1.0.0, with a measured table.
	//
	// Fires on the single-quoted replacement, AND on a double-quoted
	// replacement where the `$` is backslash-escaped (`"...\$NAME..."`) —
	// the escape is exactly what stops the shell from expanding it, so sd
	// still sees a literal `$NAME` and still reads it as a capture-group
	// reference. A double-quoted, UN-escaped `$NAME` is expanded by the
	// shell before sd ever sees it and correctly stays silent; that shape
	// has its own ruleNegative fixture.
	// `$$NAME` is sd's escape for a literal `$` and is excluded by the
	// `(?:[^'$]|\$\$)*` run — but only in the single-quoted branch. The
	// double-quoted branch has no equivalent exclusion: `"..."` gives `$$`
	// its own special shell meaning (the shell's PID) whether escaped or
	// not, so a double-quoted `\$\$NAME` is not a case this rule reasons
	// about — known gap, not worth a second RE2 branch for.
	// `$1` / `${1}` never trip it — a digit is neither a letter nor `_`.
	// Both positional forms match: the pipe form corrupts its output
	// exactly as badly as the in-place form, so this one deliberately does
	// NOT require a file operand.
	//
	// Second sighting 2026-09-02 from aipotluck.org: this rule fired
	// (via the single-quoted branch, coincidentally — the search pattern
	// was a double-quoted TS import string whose own literal `'./$types'`
	// quotes happened to satisfy that branch) on a double-quoted, escaped
	// `\$types`/`\$lib` replacement, printed its Fix, and the command ran
	// anyway: two import paths silently lost their `$` prefix. The double-
	// quoted branch above closes the coincidence — the same command now
	// matches for the actual reason, not a lucky accident of the source
	// text. That sighting is also why this Fix text below now says the
	// failure is silent and deferred, and why the block escalation right
	// after this rule exists.
	//
	// Suppressed when a named group (`(?P<name>` or `(?<name>`) appears
	// anywhere in the command, which is the legitimate use and
	// demonstrates the author knows the syntax. Anywhere rather than in
	// the pattern argument specifically: the suppressor runs against the
	// whole string, and a named group elsewhere in a command that also
	// contains an sd is not worth a second regex to distinguish.
	{
		Name:     "sd-replacement-shell-var",
		Pattern:  regexp.MustCompile(cmdPos + `sd\b` + shGap + argGap + shWord + argGap + `(?:'(?:[^'$]|\$\$)*\$\{?[A-Za-z_][^']*'|"[^"]*\\\$\{?[A-Za-z_][^"]*")`),
		Suppress: regexp.MustCompile(`\(\?P?<[A-Za-z_]`),
		Fix:      "sd's replacement string reads `$NAME` as a CAPTURE-GROUP reference, not a shell variable — and an unmatched reference expands to EMPTY rather than erroring, so the text silently disappears (`sd 'x' 'a$HERE/b'` emits `a/b`, exit 0; `sd 'x' \"a\\$HERE/b\"` — backslash-escaped inside double quotes — does the same). You will NOT see this fail: exit 0, no warning, and in source code the result is often still syntactically valid, just wrong (an import path missing its prefix, not a parse error). This is the opposite of sed, where single quotes make `$NAME` literal. For a literal `$`, double it: `$$NAME`. To interpolate a shell variable, use double quotes with NO backslash and let the shell expand it before sd sees it. Numeric refs (`$1`) and named groups you actually defined are fine.",
	},
	// --- same trap, narrowed to the unambiguous case: block it -------------
	// Second sighting above showed advise alone isn't enough for this one:
	// the rule fired, printed the fix, and the corruption landed anyway,
	// because an advisory can be skimmed — and this failure gives no
	// second chance to notice, unlike e.g. rg -r's visible bad output.
	//
	// This does NOT promote the base rule wholesale — CONTRIBUTING's block
	// bar wants no legitimate reading of the command as typed, and the base
	// rule's own Suppress only rules out a NAMED group; a plain unnamed
	// group (`(x)` with `$1`) is a legitimate case the base rule's char
	// class already excludes by construction (digits never match
	// `[A-Za-z_]`), but a plain group's mere PRESENCE says the author
	// knows capture-group syntax, same evidentiary weight as a named one —
	// so it's folded into this rule's Suppress too, not the base rule's.
	//
	// Fires only when the base pattern matches AND the whole command
	// defines no capture group of ANY kind — no named group, no plain
	// `(...)`, only `(?:...)` (non-capturing) allowed. In that state
	// `$NAME` cannot resolve under any reading; there is nothing to weigh
	// against blocking. `\((?:\?P?<[A-Za-z_]|[^?)])` matches an opening
	// paren immediately followed by either a named-group opener or any
	// character that isn't `?`/`)` — i.e. any capturing form, named or
	// plain — while `(?:` (the `?` right after `(`) and `()` fall through.
	{
		Name:     "sd-replacement-shell-var-no-capture-group",
		Pattern:  regexp.MustCompile(cmdPos + `sd\b` + shGap + argGap + shWord + argGap + `(?:'(?:[^'$]|\$\$)*\$\{?[A-Za-z_][^']*'|"[^"]*\\\$\{?[A-Za-z_][^"]*")`),
		Suppress: regexp.MustCompile(`\((?:\?P?<[A-Za-z_]|[^?)])`),
		Fix:      "sd's replacement references `$NAME`/`\\$NAME` and the search pattern defines NO capture group at all — not even a numbered one. There is no reading under which that reference resolves to anything but empty, so this refuses rather than advise: an unmatched capture-group reference silently deletes text, exit 0, and the result can be valid-looking source (a shortened import path, not a parse error) that nobody re-reads. For a literal `$`, double it: `$$NAME`. To interpolate a shell variable, use double quotes with NO backslash and let the shell expand it before sd sees it. Rewrite and retry.",
		Action:   "block",
	},
	// --- silent-corruption trap: BSD sed's -i takes a MANDATORY, glued
	// argument, unlike GNU's optional one --------------------------------
	// macOS/BSD only. First rule in this file gated by OS -- see the OS
	// field on Rule and goos in Match(). Everywhere else this is
	// completely correct GNU usage; gating it universally would misfire
	// on every Linux install, this one included.
	//
	// `sed -i extension` per BSD's own man page: the extension is
	// mandatory, and it is read as the very next characters glued to
	// `-i`, never a separate flag. `sed -i '' 's/x/y/' file` (explicit
	// empty-string extension) is the correct BSD form. `sed -i 's/x/y/'
	// file` -- the GNU-muscle-memory form, no separate extension -- gets
	// `'s/x/y/'` consumed AS the extension, and `file` treated as the sed
	// SCRIPT, with no file operand left at all. Reads clean, no error in
	// the common case, and the source file is never touched. Found
	// 2026-09-08 auditing weir against BSD tool behavior, not from a live
	// incident.
	//
	// Suppressed on the explicit empty-string form, which is the correct
	// usage this rule must not flag.
	{
		Name:     "bsd-sed-i-mandatory-arg",
		Pattern:  regexp.MustCompile(cmdPos + `sed\b[^|\n;&]*` + argGap + `-i(?:\s|$)`),
		Suppress: regexp.MustCompile(`\bsed\b[^|\n;&]*` + argGap + `-i` + argGap + `(?:''|"")`),
		Fix:      "On macOS/BSD, sed's `-i` takes a MANDATORY argument glued to the flag -- unlike GNU sed, where it's optional. `sed -i 's/x/y/' file` reads `'s/x/y/'` as the -i extension and `file` as the sed SCRIPT, leaving no file operand at all -- it runs clean, no error, and never touches `file`. Use `sed -i '' 's/x/y/' file` (explicit empty-string extension) for the GNU-equivalent in-place edit, or `sed -i.bak 's/x/y/' file` to keep a backup.",
		OS:       "darwin",
	},
	// --- self-match trap: a pattern that matches the shell running it -----
	// Claude Code runs every Bash call as `bash -c '<the whole command>'`,
	// so that shell's /proc/<pid>/cmdline contains the literal pattern
	// text. `pgrep -f` matches full command lines, so it matches the shell
	// asking the question.
	//
	// For a one-shot look the self-match costs one extra line of output
	// and nothing else, which is why this rule is gated on co-occurrence
	// with a loop or a kill rather than on `-f` alone. In a wait loop it
	// is fatal and silent: `until ! pgrep -f X; do sleep 10; done` never
	// exits, and the sleep makes it cheap enough to go unnoticed
	// indefinitely. Reported 2026-08-05 from lexicon, where two earlier
	// instances of exactly that loop were found still running with etime
	// in DAYS — they had outlived the job, the session that started them,
	// and every session since, on a box that was 11G into 14G of RAM.
	//
	// The classic habit it displaces (`ps aux | grep foo | grep -v grep`)
	// has the self-match in its folklore. pgrep looks like the clean
	// replacement that made `grep -v grep` unnecessary, and for `pgrep
	// foo` (match on process NAME) it is. `-f` puts the self-match back
	// and nothing says so.
	{
		Name: "pgrep-f-self-match",
		Pattern: regexp.MustCompile(
			`\b(?:until|while)\b[^\n;&]*\bpgrep\b[^|\n;&]*` + argGap + `(?:-[a-zA-Z]*f\b|--full\b)` +
				`|\bpgrep\b[^|\n;&]*` + argGap + `(?:-[a-zA-Z]*f\b|--full\b)[^\n;&]*\b(?:until|while|kill)\b`),
		Fix: "`pgrep -f PATTERN` matches the FULL command line of every process, including the `bash -c` shell running THIS command — the pattern text is in its own cmdline. So `until ! pgrep -f foo; do sleep 10; done` matches itself and never exits, with no error and no output. Wait on a PID instead, which cannot match itself: `cmd & PID=$!` then `until ! kill -0 $PID 2>/dev/null; do sleep 10; done`. If you must use a pattern, match the process NAME (`pgrep foo`, no `-f`).",
	},
	{
		Name:    "pkill-f-self-match",
		Pattern: regexp.MustCompile(`\bpkill\b[^|\n;&]*` + argGap + `(?:-[a-zA-Z]*f\b|--full\b)`),
		Fix:     "`pkill -f PATTERN` matches full command lines, and under Claude Code the `bash -c` shell running this command has the pattern text in its own cmdline — so this kills its own shell, usually before you learn whether it killed the target. Ungated, unlike the pgrep case: there is no safe one-shot form. Capture the PID when you start the job (`cmd & PID=$!`) and `kill $PID`, or match the process NAME with `pkill foo` (no `-f`).",
	},
	// --- silent-corruption trap: rg's -h is --help ------------------------
	// In grep, `-h` is `--no-filename`, and `grep -oh PAT f1 f2` is the
	// standard "just the matches, no file: prefixes" idiom. In ripgrep,
	// `-h` is `--help` and `--no-filename` is `-I` (or `-N`).
	//
	// So `rg -oh 'https?://[^ ]+' a.md b.md` prints the usage text on
	// stdout and exits 0; the pattern and the file list are never read.
	// Piped into `sort -u | head`, which is what an extraction command
	// does, it yields a tidy sorted list of flag descriptions and the
	// author's email address, and reads as "the files contained these
	// strings." Reported 2026-07-29.
	//
	// Gated so a deliberate `rg -h` / `rg --help` stays silent: fires only
	// on an h-bearing bundle of two or more letters, or on `-h` followed
	// by something that looks like a pattern or a path.
	{
		Name:    "rg-h-is-help",
		Pattern: regexp.MustCompile(cmdPos + `rg\b[^|\n;&]*` + argGap + `(?:-[a-z]*h[a-z]+\b|-[a-z]+h[a-z]*\b|-h` + argGap + `['"a-zA-Z_./~])`),
		Fix:     "In rg, `-h` is `--help`, NOT `--no-filename` — that is `-I` or `-N`. `rg -oh PAT files` prints the usage text on stdout with exit 0; the pattern and file list are never read, and piped into `sort`/`head` it reads as a clean result set that happens to contain no matches. Use `rg -o -N PAT files` (or `-I`). `-o` alone is enough for a single file.",
	},
	// --- silent-scope trap: grep -L's meaning does not survive to rg ------
	// Found by data/flag_overlap.py, a systematic sweep of every --help
	// flag letter shared between a classic tool and its weir-suggested
	// replacement — not a live incident like the other rg rules on this
	// page. Recorded here as-found rather than backdated to look like one.
	//
	// grep -L (--files-without-match) prints the names of files that do
	// NOT contain a match — the standard "find files missing X" idiom for
	// an audit or batch-fix script. In ripgrep, -L is --follow (follow
	// symlinks while searching); rg's actual files-without-match has no
	// short flag at all, only the long form. `rg -L PATTERN dir` runs
	// clean, exit 0, and prints files that DO contain a match, the
	// opposite selection from what -L means in grep — silent, not a
	// parse error, because -L is a valid rg flag on its own.
	//
	// Advisory, not block: unlike -r and -h above, there is no shape here
	// that is unambiguously the mistake. `rg -L` is also plain English for
	// "follow symlinks," a real thing to want, and this rule has no way to
	// tell the two intentions apart from the command text alone.
	{
		Name:    "rg-cap-l-misfire",
		Pattern: regexp.MustCompile(cmdPos + `rg\b[^|\n;&]*` + argGap + `-[A-Za-z]*L[A-Za-z]*\b`),
		Fix:     "In rg, `-L` is `--follow` (follow symlinks), NOT `--files-without-match` — that grep flag has no short form in rg at all. `rg -L PATTERN dir` runs clean and lists files that DO match, the opposite of what -L selects in grep. If you want files lacking a match, use `rg --files-without-match PATTERN dir`. If you meant to follow symlinks, this is a false alarm — carry on.",
	},
	// --- silent-scope trap: an ignore file governs search, not just git ---
	// rg and fd honour .gitignore whether or not a git operation is
	// anywhere in view, so a deny-by-default ignore file makes an entire
	// tree return zero matches with exit 1 — indistinguishable from "the
	// string is not there."
	//
	// `--hidden` does NOT fix this, which is the part that costs time:
	// ripgrep descends into a hidden directory fine when you name it as an
	// explicit path argument, so the dotted path name misdirects toward
	// the wrong flag. The decisive flag is `--no-ignore` (`-uu` for both).
	//
	// Reported 2026-08-06 by a sibling session after a search for stope hook
	// markers under ~/.claude/projects returned zero rows and was about to
	// be written up as "these hooks never fired." With `-uu` the same
	// search returns 20+ files; one marker alone occurs 159 times.
	// `~/.claude/.gitignore` is deny-by-default with an allowlist — a good
	// decision for keeping ~10G of transcripts and a credentials file out
	// of git, which silently became a control on search scope too.
	//
	// Gated on the PATH ARGUMENT, not on the absence of a flag. The same
	// report measured both gates against 39,827 tool calls on this host:
	// "rg/fd without --hidden" fires on 11% of all calls at 6.4%
	// precision (base rate 5.1%, so barely above chance); adding "and the
	// command names a dot-path" moves precision to 28.5% and cuts fire
	// volume 34x, to 0.33%. A rule that fires on 11% of tool calls is a
	// tax; one that fires on 0.33% is a tripwire.
	{
		Name:     "rg-ignore-file-hides-target",
		Pattern:  regexp.MustCompile(cmdPos + `(?:rg|fd|fdfind)\b[^|\n;&]*` + argGap + dotPath),
		Suppress: regexp.MustCompile(`--no-ignore\b|--unrestricted\b|` + argGap + `-[a-zA-Z]*[uI][a-zA-Z]*(?:\s|$)|` + dotFileArg),
		Fix:      "rg and fd honour `.gitignore` even when no git command is involved, so a deny-by-default ignore file makes a whole tree return zero matches with exit 1 — indistinguishable from \"the string is not there.\" `--hidden` does NOT fix this; the flag is `--no-ignore` (`-uu` for rg, `-HI` for fd, which covers both halves). `~/.claude` is deny-by-default on this host, so any search under it needs `-uu`. Confirm what is being skipped with `rg --debug ... 2>&1 | rg ignoring`.",
	},
	// --- silent-status trap: a pipeline reports its LAST stage's status ---
	// `git push origin main 2>&1 | tail -5` exits 0 when the push was
	// rejected, because `tail` succeeded at tailing a failure. The error
	// text is right there on stdout, so it does not feel hidden — it gets
	// hidden one layer up, by anything that reports the STATUS rather than
	// showing the text: a background-task runner, a `set -e` script, a CI
	// step, `&&` chaining, an agent harness summarising "exit code 0".
	//
	// `2>&1` makes it worse rather than better. Without it, stderr reaches
	// the terminal unmixed; with it, the error becomes more lines for
	// `tail -5` to discard.
	//
	// A filter as the last stage inverts the sense instead of flattening
	// it: `go test ./... | rg -v '^ok '` exits 1 when it filtered
	// everything away, so a fully-green suite reports failure.
	//
	// Reported 2026-08-06, three sightings in two sessions. The first was
	// a background `git push` that reported exit 0 on a rejected push and
	// was caught minutes later by comparing `git rev-parse HEAD` against
	// `origin/develop`.
	//
	// Fourth sighting, 2026-09-05 (aipotluck.org): advisory fired on exactly
	// this shape — `git push ... 2>&1 | tail` from a background task — and
	// the command ran anyway. The rejected push (a failing pre-push hook)
	// then reported "completed (exit code 0)" to the harness, which was
	// briefly treated as proof of a successful push. Same escalation logic
	// as `sd-in-place-write-*`: for a command whose whole point is a status
	// nobody else will see (a background runner, a CI step, an agent
	// harness's own "exit code 0" summary), the advisory text arrives in
	// the same turn as the tool result — after the pipeline has already
	// run and already reported the wrong status upstream. An advisory can
	// be skimmed past exactly like it was here; blocking is what forces the
	// rewrite before that status ever gets consumed by something that
	// trusts it.
	{
		Name:     "pipe-eats-exit-status",
		Pattern:  regexp.MustCompile(stateChanging + redirGap + `\|\s*(?:tail|head|grep|rg)\b`),
		Suppress: regexp.MustCompile(`pipefail|PIPESTATUS`),
		Fix:      "A pipeline exits with its LAST stage's status, so `CMD | tail`/`| head` reports the trimmer's success and a failed CMD reads as exit 0 — a rejected `git push` or a `go install` that printed \"does not contain package\" both report success to a background runner, a `set -e` script, or a CI step. `2>&1` does not help; it just gives `tail` more lines to discard. Use `set -o pipefail`, or redirect and read the status directly: `CMD > out.log 2>&1; echo \"exit: $?\"`. When the command's job is to produce or replace an artifact, check the artifact — a `stat` timestamp or a `--version` is a measurement, a status is a claim. Rewrite the command and retry.",
		Action:   "block",
	},
	{
		Name:     "pipe-status-echo",
		Pattern:  regexp.MustCompile(`\|\s*(?:tail|head|grep|rg|sed|awk|jq|wc|sort|uniq|tee)\b[^\n;&]*(?:\n|;)\s*echo\b[^\n]*\$\?`),
		Suppress: regexp.MustCompile(`pipefail|PIPESTATUS`),
		Fix:      "`$?` after a pipeline is the LAST stage's status, not the command you care about — `go test ./... | rg -v '^ok '` then `echo $?` prints rg's status, and rg exits 1 when it filtered everything away, so a green suite reports failure. Check `${PIPESTATUS[0]}` INSTEAD, and immediately: it is clobbered by the next command, including by the `echo` that reads it. `set -o pipefail` makes the pipeline take the first non-zero status if you would rather not index.",
	},
	// --- silent-destruction trap: sponge commits an empty stream ----------
	// `cmd | sponge FILE` truncates FILE to zero whenever cmd fails,
	// because sponge faithfully writes whatever the pipeline produced and
	// a failed command produces nothing. Exit 0, no warning, original
	// gone. Measured here rather than assumed:
	//
	//	printf 'alpha\nbeta\ngamma\n' > m.md   # 17 bytes
	//	false | sponge m.md                    # 0 bytes, exit 0
	//
	// sponge's whole reason to exist is that `cmd < FILE > FILE` truncates
	// before cmd reads, so it is reached for precisely when the target and
	// the source are the same file — which is also when an empty write is
	// unrecoverable. `sd` and `awk` fail without emptying their target.
	//
	// Reported 2026-08-07 by documents-66700 after
	// `grep -vxF "$LINE" "$M" | sponge "$M"` destroyed an auto-memory
	// index. Two causes stacked: grep-dash-pattern below made the left
	// side fail, and this made the failure destroy the input. Either
	// alone is survivable.
	//
	// No Suppress. Whether the left side can fail is not knowable from the
	// command text, so this advises on every piped sponge and accepts
	// that some of them were safe.
	{
		Name:    "sponge-eats-failed-pipeline",
		Pattern: regexp.MustCompile(`\|\s*sponge\b`),
		Fix:     "`cmd | sponge FILE` writes an EMPTY file if cmd fails — sponge commits whatever the pipeline produced, and a failed command produces nothing. Exit 0, no warning, and the original is gone (verified: `false | sponge m.md` takes a 17-byte file to 0). Guard it: `out=$(cmd) && printf '%s\\n' \"$out\" > FILE`, or write a temp path and `mv` after checking the status. `sd` and `awk` fail without emptying the target.",
	},
	// --- silent-failure trap: a pattern that starts with a dash -----------
	// `grep -vxF \"- [Leave it]\" FILE` parses the pattern as an option
	// bundle and exits 2 with a usage error and no matches. A markdown
	// list item, a diff line, a CLI flag being searched for — all start
	// with `-`. GNU grep does this too; on this host `grep` is ugrep
	// 7.5.0, whose error text is unfamiliar enough to read as a different
	// failure:
	//
	//	grep -vxF "- [Leave it]" n.md   -> ugrep: invalid option, exit 2
	//	grep -vxF -- "- [Leave it]" n.md -> alpha, exit 0
	//
	// The reported case is loud-ish — exit 2 and a message on stderr —
	// and it earns a rule anyway for two reasons. The message goes to
	// stderr while stdout stays empty, and the shapes that swallow stderr
	// are the same ones that make an empty result look like an answer; in
	// the incident, the empty result went straight into a sponge.
	//
	// The second reason came out of measuring the corpus. Sweeping the
	// rule turned up `grep -E "->"` and `grep "- id:"` as expected, and
	// also this, which is worse than the reported case:
	//
	//	grep -E '->'        -> rc=2, ugrep: invalid option ->
	//	grep -E '--- FAIL'  -> rc=2, grep: unrecognized option
	//	grep -E '-v'        -> rc=1, NO MESSAGE
	//
	// A pattern that happens to BE a valid flag does not error at all.
	// `-v` is consumed as --invert-match, so grep silently searches for
	// nothing, inverts, and exits 1 — which reads as "no matches" and is
	// the quietest form of the bug. `-i`, `-c`, `-l`, `-o` and `-w` all
	// behave the same way.
	//
	// Gated on a QUOTED operand starting with `-`, because an unquoted one
	// is indistinguishable from a flag and quoting is what signals the
	// author meant it as data. cmdPos keeps `git log --grep "-foo"` out,
	// where `\bgrep\b` alone would match inside `--grep`. A quoted
	// leading-dash FILE operand (`grep PAT "-weird-name.jsonl"`) fires too
	// and should: it fails identically and takes the same `--` fix.
	{
		Name:     "grep-dash-pattern",
		Pattern:  regexp.MustCompile(cmdPos + `grep\b[^|\n;&]*` + argGap + `(?:'-[^']*'|"-[^"]*")`),
		Suppress: regexp.MustCompile(`\bgrep\b[^|\n;&]*` + argGap + `(?:--\s|-e\b|--regexp\b)`),
		Fix:      "An operand starting with `-` is parsed as an OPTION, not as your pattern or filename. Two outcomes, and the second is the dangerous one: `grep -E '--- FAIL' f` exits 2 with a usage error, while `grep -E '-v' f` exits 1 with NO message at all — `-v` is a valid flag, so grep quietly inverts the match instead of searching for the literal text. Markdown list items, diff lines, arrows, and flag names all start with `-`. Pass `--` first: `grep -- \"$PATTERN\" FILE`, or name it with `-e \"$PATTERN\"`.",
	},
}

// redactionAlt is the replacement-shape signal for
// sd-in-place-write-redaction: a replacement that looks like a mask is a
// replacement meant for a screen, not for disk.
const redactionAlt = `(?:<set>|<redacted>|\*\*\*|REDACTED)`

// dotPath matches an argument naming a dot-DIRECTORY in a path —
// `~/.claude/projects`, `/home/x/.config`, `.git/hooks`. Requires a slash
// so that a bare `.env` pattern or a `./src` / `../lib` relative path does
// not trip it. The optional leading quote lets `"$HOME/.claude"` through,
// which is how a path with a variable in it usually gets written.
//
// `!` and `*` are excluded because the first corpus sweep of this rule
// found its false positives concentrated in one shape: a glob EXCLUSION,
// `rg -g '!**/.git/**' -g '!**/.venv/**' PATTERN dir`. That names a
// dot-directory in order to skip it, which is the opposite of walking
// into an ignore file unawares — and someone writing exclusions by hand
// is the last person who needs telling that ignore rules exist.
// The argument must also LOOK LIKE A DIRECTORY, which here means every
// component after the dot segment is dot-free and the argument ends
// there. ripgrep applies ignore rules only while descending; an
// explicitly-named path argument is read regardless of them. Measured on
// a scratch tree with a deny-by-default `*` gitignore:
//
//	rg NEEDLE .env    -> 1        (explicit file: read, ignore rules skipped)
//	rg NEEDLE sub     -> nothing  (directory: descended, ignore rules apply)
//
// So `rg -oN 'phc_…' frontend/.env.production.local` and
// `rg -n node .github/workflows/ci.yml` are not the trap — a dot in the
// final component ends the match. Two of the first sweep's six sampled
// fires were exactly that. `dotFileArg` below catches the remainder, the
// extensionless dot-files like `.gitignore` that this shape cannot tell
// from a directory.
//
// The same measurement retires `.env` as the canonical example the v0.1.3
// gotcha text used, which is why the reframed entry names `~/.claude`
// instead: a tree is the thing that returns a clean zero.
const dotSeg = `\.[A-Za-z_][A-Za-z0-9_-]*`
const dirTail = `(?:/[A-Za-z0-9_][A-Za-z0-9_-]*)`
const dotPath = `['"]?(?:[^\s'"|;&<>*!]*/` + dotSeg + dirTail + `*|` + dotSeg + dirTail + `+)/?['"]?(?:\s|$)`

// dotFileArg catches the dot-files that dotPath cannot distinguish from
// a directory, because they carry no extension: `frontend/.gitignore`
// reads exactly like `~/.config` to a regex. Named list rather than a
// shape, since there is no shape to match.
//
// Whole-command, so a command naming both a dot-file and a real dot-tree
// is suppressed. Rare enough to accept over a second regex.
const dotFileStart = `(?:^|[\s'"/])`
const dotFileArg = dotFileStart + `\.(?:gitignore|gitattributes|gitmodules|dockerignore|npmrc|nvmrc|` +
	`editorconfig|prettierrc|eslintrc|babelrc|bashrc|zshrc|profile|env)[\w.-]*(?:\s|$|['"])` +
	`|` + dotFileStart + `\.git/(?:hooks/\S+|config|HEAD|COMMIT_EDITMSG|index)\b`

// stateChanging lists commands that PUBLISH or INSTALL — where the status
// is the only signal the work happened, because the visible evidence of
// failure is indistinguishable from the evidence of success (a binary that
// is still there, a remote that still has the old commit).
//
// The first draft of this list also carried the build and test verbs:
// `make`, `go build`, `go test`, `npm run`, `cargo build`. Measured
// against 210,202 Bash calls it fired 15,571 times — 7.4% of everything,
// noisier than any rule weir has ever shipped, and dominated by a single
// deliberate shape: `make check 2>&1 | tail -15`. That is not the trap.
// Someone trimming a check target is READING the tail; the status being
// eaten costs them nothing because the output is right there and they are
// looking at it. The trap needs a reader that consumes the status instead
// of the text, and the failure has to leave no other trace.
//
// Cut to the verbs where both conditions hold, this fires ~0.1%. The
// build and test half is covered by pipe-status-echo (which gates on the
// author explicitly asking for `$?`, and so is right regardless of verb)
// and by the shell-level gotcha entry in internal/inject.
const stateChanging = `\b(?:git\s+(?:push|pull)` +
	`|go\s+install|cargo\s+(?:install|publish)|pip\s+install|npm\s+publish` +
	`|terraform\s+(?:apply|destroy)|kubectl\s+apply|docker\s+push` +
	`|gh\s+release|rsync|scp)\b`

// goos is runtime.GOOS, held in a var so tests can exercise an OS-gated
// rule without actually running on that platform.
var goos = runtime.GOOS

// Match returns the subset of Rules whose patterns match cmd, after applying
// any per-rule Suppress antidote. Block-action rules additionally suppress
// matches that land inside a shell string context — quoted, or the body of
// a heredoc — see isInsideShellString. Without that guard, a `git commit
// -F -` heredoc whose message prose contains the word "which" would refuse
// a productive commit.
func Match(cmd string) []Rule {
	out := make([]Rule, 0, 2)
	for _, r := range Rules {
		if r.OS != "" && r.OS != goos {
			continue
		}
		loc := r.Pattern.FindStringIndex(cmd)
		if loc == nil {
			continue
		}
		if r.Suppress != nil && r.Suppress.MatchString(cmd) {
			continue
		}
		if r.Action == "block" && isInsideShellString(cmd, loc[0]) {
			continue
		}
		out = append(out, r)
	}
	return out
}
