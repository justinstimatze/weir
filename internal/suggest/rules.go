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
		Name:    "uuoc",
		Pattern: regexp.MustCompile(`(?:^|[;&\n]\s*|&&\s*|\|\|\s*|\|\s*)cat\s+[^-\s]\S*\s*\|\s*(grep|head|tail|sed|awk|jq|less|more|wc|sort|uniq|sd|mlr|bat|rg|fd|fzf)\b`),
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
	{
		// In rg, `-r` is `--replace`, NOT recursive (rg recurses by default).
		// `rg -rn PATTERN path` — the natural `grep -rn` muscle-memory reach
		// — parses as `--replace=n` and rewrites every match to the literal
		// "n" with exit 0, matching filenames, and matching line numbers. No
		// warning of any kind. Reported via dispatch 2026-07-23 after an
		// hour lost misreading `env.CRON_SECRET` as `env.n` on a live auth
		// path (and initially blaming the harness, not rg).
		//
		// Advisory (not block): a legit single-letter replacement like
		// `rg -r n file` is rare but real; false-blocking it is worse than
		// surfacing a warning. Pattern targets the specific misfire shape:
		// `-r` followed (bundled or space-separated) by one of the common
		// rg short flags (n, l, i, w, c, v) as a bare word. Quoted
		// replacements like `rg -r 'n' file` do NOT match (the char after
		// `-r ` is `'`, not a bare letter), so quote-aware suppression
		// isn't needed even though this rule is advisory.
		Name:    "rg-r-misfire",
		Pattern: regexp.MustCompile(`\brg\b[^|\n;&]*\s-r(?:[nliwcv]\b|\s+[nliwcv]\b)`),
		Fix:     "`rg -r X` sets `--replace=X`, NOT recursion — rg recurses by default. `rg -rn PATTERN path` (grep -rn muscle memory) parses as `--replace=n` and silently rewrites every match to the literal \"n\" with exit 0. Drop the `-r`: use `rg -n PATTERN path`.",
	},
}

// Match returns the subset of Rules whose patterns match cmd, after applying
// any per-rule Suppress antidote. Block-action rules additionally suppress
// matches that land inside a quoted shell string — see isInsideShellQuotes.
// Without that guard, a `git commit -m "...which..."` heredoc would refuse a
// productive commit.
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
		if r.Action == "block" && isInsideShellQuotes(cmd, loc[0]) {
			continue
		}
		out = append(out, r)
	}
	return out
}
