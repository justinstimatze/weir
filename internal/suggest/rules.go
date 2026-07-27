package suggest

import "regexp"

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
}

// Rules is the live antipattern set. Edits here are the rule table.
//
// Inter-statement-separator-safe gaps use [^|\n;&], NOT [^|] — without that,
// multi-line commands like `... | head -20\necho ---\n... | tail -10` will
// false-match cross-statement "patterns" that aren't really there.
var Rules = []Rule{
	{
		Name:    "grep-head-trim",
		Pattern: regexp.MustCompile(`\bgrep\b[^|]*\|\s*head\b`),
		Fix:     "`grep PATTERN FILE | head -n N` -> `grep -m N PATTERN FILE` (or `rg -m N` with rg installed). Stops at the source instead of relying on the pipe for early-exit. Caveat: `-m N` caps PER FILE, `| head -N` caps TOTAL across all files — they diverge on multi-file/recursive searches.",
	},
	{
		Name:    "ls-grep",
		Pattern: regexp.MustCompile(`\bls\b[^|]*\|\s*grep\b`),
		Fix:     "`ls | grep PATTERN` -> use a glob (`ls *pattern*` or `*pattern*` directly) or `find -name PATTERN` / `fd PATTERN` for recursive. Skips ls's column formatting + the grep stage.",
	},
	{
		Name:    "grep-wc",
		Pattern: regexp.MustCompile(`\bgrep\b[^|]*\|\s*wc\s+-l\b`),
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
		Pattern: regexp.MustCompile(`\bfind\b[^|]*-exec\s+[^+]+\\;`),
		Fix:     "`find ... -exec CMD {} \\;` spawns one process per match. `-exec CMD {} +` batches — same semantics for grep/rm/chmod/wc/etc., far fewer execs.",
	},
	{
		// Go's RE2 doesn't support lookaround, so we keep the negative check
		// as a separate Suppress regex below.
		Name:     "sort-uniq",
		Pattern:  regexp.MustCompile(`\bsort\b[^|]*\|\s*uniq\b`),
		Suppress: regexp.MustCompile(`\buniq\s+-[a-zA-Z]*[cdu]\b`),
		Fix:      "`sort | uniq` -> `sort -u` (one pass, no second process). Keep the pipeline when you need `uniq -c` (count), `-d` (only dupes), or `-u` (only uniques).",
	},
	{
		Name:    "awk-awk",
		Pattern: regexp.MustCompile(`\bawk\b[^|]*\|\s*awk\b`),
		Fix:     "Two awks in a row can usually fuse into one — the second awk's actions become a follow-up block in the first. Advisory only; the correct fusion depends on the awk code.",
	},
	// --- Promoted from data/mine_extra.py pass A (2026-05-20) ---------------
	{
		// `which CMD` at statement start (^, ;, &&, ||, newline, after a pipe).
		// RE2 can't do "preceded by"; we anchor by including the separator in the
		// match (consumes one char of context but selftests still verify behavior).
		Name:    "which-vs-command-v",
		Pattern: regexp.MustCompile(`(?:^|[;&\n]\s*|&&\s*|\|\|\s*|\|\s*)which\s+[A-Za-z_][\w.-]*`),
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
		Fix:     "`ps aux | grep PATTERN` -> `pgrep -af PATTERN` (or `pgrep -f PATTERN` to omit the cmdline). Atomic, sees full cmdline by default, handles empty matches cleanly, no `grep -v grep` self-match dance. For killing: `pkill -f PATTERN`.",
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
		Pattern: regexp.MustCompile(`\bgit\s+add\b[^|\n;&]*?\s(?:-A|--all|\./?)(?:\s|$)`),
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
		Pattern: regexp.MustCompile(`\brg\b[^|\n;&]*\s-r[nliwcv]\b`),
		Fix:     "`rg -r[X]` (bundled) sets `--replace=X`, not recursion — rg recurses by default. `rg -rn PATTERN path` (grep -rn muscle memory) parses as `--replace=n` and silently rewrites every match to the literal \"n\" with exit 0. A real single-letter replacement is written `-r n` (separated) or `--replace=n`; the bundle form is virtually always the muscle-memory trap. Rewrite as `rg -n PATTERN path` (drop the `-r`) and retry.",
		Action:  "block",
	},
	{
		Name:    "rg-r-misfire",
		Pattern: regexp.MustCompile(`\brg\b[^|\n;&]*\s-r\s+(?:[nliwcv]\b|''|"")`),
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
	// All three rules suppress on `-p` / `--preview` — the safe form.
	// Stdin form (`cat FILE | sd PAT REP`) has only 2 positional args and
	// naturally doesn't match the 3-arg pattern.
	{
		Name:     "sd-in-place-write",
		Pattern:  regexp.MustCompile(`\bsd\b[^|\n;&]*\s(?:'[^']*'|"[^"]*"|[^\s'"]\S*)\s+(?:'[^']*'|"[^"]*"|[^\s'"]\S*)\s+(?:'[^']*'|"[^"]*"|[a-zA-Z0-9_./~][\w./~-]*)(?:\s|$)`),
		Suppress: regexp.MustCompile(`\bsd\b[^|\n;&]*\s(?:-p\b|--preview\b)`),
		Fix:      "sd writes to FILE IN PLACE — unlike sed, no `-i` is needed. `sd PAT REP FILE` overwrites FILE immediately. For a preview use `sd -p PAT REP FILE`; for stdout use `cat FILE | sd PAT REP`.",
	},
	{
		Name:     "sd-in-place-write-secret-file",
		Pattern:  regexp.MustCompile(`\bsd\b[^|\n;&]*\s(?:'[^']*'|"[^"]*"|[^\s'"]\S*)\s+(?:'[^']*'|"[^"]*"|[^\s'"]\S*)\s+\S*(?:\.env\b|\.pem\b|credential|\.key\b|\.p12\b|\.pfx\b)`),
		Suppress: regexp.MustCompile(`\bsd\b[^|\n;&]*\s(?:-p\b|--preview\b)`),
		Fix:      "sd writes IN PLACE + you named a secret-ish file (.env, .pem, credentials, .key, .p12, .pfx). Overwriting the file destroys the real secret with the replacement string. If you meant to mask for DISPLAY, pipe to stdout: `cat FILE | sd PAT REP`. If you meant to persist, verify first with `sd -p PAT REP FILE`. Rewrite and retry.",
		Action:   "block",
	},
	{
		Name:     "sd-in-place-write-redaction",
		Pattern:  regexp.MustCompile(`\bsd\b[^|\n;&]*\s(?:'[^']*'|"[^"]*"|[^\s'"]\S*)\s+(?:'[^']*(?:<set>|<redacted>|\*\*\*|REDACTED)[^']*'|"[^"]*(?:<set>|<redacted>|\*\*\*|REDACTED)[^"]*"|\S*(?:<set>|<redacted>|\*\*\*|REDACTED)\S*)\s+(?:'[^']*'|"[^"]*"|[a-zA-Z0-9_./~][\w./~-]*)`),
		Suppress: regexp.MustCompile(`\bsd\b[^|\n;&]*\s(?:-p\b|--preview\b)`),
		Fix:      "sd writes IN PLACE + the replacement looks like a redaction pattern (<set>, <redacted>, ***, REDACTED). Users typing redactions almost never want them persisted to disk — the shape says \"for display\". Pipe to stdout instead: `cat FILE | sd PAT REP` (safe, no write). Rewrite and retry.",
		Action:   "block",
	},
	{
		Name:     "sd-in-place-write-empty",
		Pattern:  regexp.MustCompile(`\bsd\b[^|\n;&]*\s(?:'[^']*'|"[^"]*"|[^\s'"]\S*)\s+(?:''|"")\s+(?:'[^']*'|"[^"]*"|[a-zA-Z0-9_./~][\w./~-]*)(?:\s|$)`),
		Suppress: regexp.MustCompile(`\bsd\b[^|\n;&]*\s(?:-p\b|--preview\b)`),
		Fix:      "sd writes IN PLACE + the replacement is EMPTY, so this DELETES the matched text from FILE rather than substituting anything. Stripping a section to read what's left is a display job: pipe it — `cat FILE | sd PAT ''` (safe, no write). To persist a deletion, confirm with `sd -p PAT '' FILE` first, or use an editor. Rewrite and retry.",
		Action:   "block",
	},
}

// Match returns the subset of Rules whose patterns match cmd, after applying
// any per-rule Suppress antidote. Block-action rules additionally suppress
// matches that land inside a shell string context — quoted, or the body of
// a heredoc — see isInsideShellString. Without that guard, a `git commit
// -F -` heredoc whose message prose contains the word "which" would refuse
// a productive commit.
func Match(cmd string) []Rule {
	out := make([]Rule, 0, 2)
	for _, r := range Rules {
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
