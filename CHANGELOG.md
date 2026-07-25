# Changelog

All notable changes to weir are documented here. Format loosely follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions are git tags.

## v0.1.5 — 2026-07-25

### Added
- Three new suggest rules for the `sd` in-place trap — the second silent-destructive incident reported this week (rg-r-misfire being the first). Reported by cope-1184525 after `sd '=.*' '=<set>' .env` overwrote a live `ANTHROPIC_API_KEY` with the literal string `<set>` (file went 232 → 129 bytes, recovered only because they kept a separate key .txt out-of-band).
  - **`sd-in-place-write`** (advise): fires on any `sd PAT REP FILE` shape with no `-p`/`--preview`. Legitimate persistence is a real use case, so advisory rather than block.
  - **`sd-in-place-write-secret-file`** (BLOCK): base shape + file operand matches `.env` / `.pem` / `credential*` / `.key` / `.p12` / `.pfx`. Overwriting these is close to unrecoverable in practice.
  - **`sd-in-place-write-redaction`** (BLOCK): base shape + replacement contains `<set>` / `<redacted>` / `***` / `REDACTED`. A user typing a redaction almost never wants it persisted — the replacement's shape reveals the "for display" intent.
- All three suppress on `-p` / `--preview`. Stdin form (`cat FILE | sd PAT REP`) has only two positional args and naturally doesn't match.

### Fixed
- Removed `sd` from the `uuoc` rule's target alternation. The uuoc rewrite for `cat FILE | sd PAT REP` (safe stdin form) → `sd PAT REP FILE` is the exact destructive in-place shape the new sd rules block. Two rules would fight. sd is the only tool on the uuoc list whose "TOOL FILE" form differs destructively from the "cat FILE | TOOL" form; sed's `-i`-less default is stdout, so sed stays.

### Notes
- Cope's diagnosis on why the v0.1.3 sd gotcha didn't stop this: prose in the SessionStart inject block is documentation, not a guard. On the same session, `ls-pipe-wc-l` and `grep-head-trim` blocked style nits while the destructive command sailed through. A gotcha that only lives in the session-start digest can't interrupt at the moment of danger — that's the PreToolUse rule's job. Bar for adding a gotcha entry now includes: does this also need a PreToolUse rule?

## v0.1.4 — 2026-07-23

### Fixed
- Block-mode rules now suppress matches that land inside a heredoc body, in addition to the existing single/double-quoted suppression. Motivating case (reported by aipotluckorg-3288452): `which-vs-command-v` fired on a `git commit -F - <<'EOF' ... EOF` whose commit-message prose contained the English word "which" three times, refusing a productive commit. The v0.1.1 walker only tracked `'` and `"`, so heredoc bodies were a blind spot. Walker now recognizes `<<[-]?['"]?DELIM['"]?` openers and terminator lines (dedent-aware for `<<-`); `<<<` (herestring) correctly does NOT open a body. Documented gaps unchanged: `$'...'`, backticks, nested heredocs, and heredoc openers inside quoted strings.

### Changed
- `rg-r-misfire` split into two rules:
  - **`rg-r-misfire-bundled`** (BLOCK, new): the bundled form `-r[nliwcv]` has virtually no legitimate use — a real single-letter replacement is written `-r n` (separated) or `--replace=n`. Bundled `-r[letter]` is nearly always the `grep -rn` muscle-memory trap that silently rewrites stdout. Blocking outright.
  - **`rg-r-misfire`** (advise, kept): separated `-r X` where X is a single letter, `''`, or `""`. Legitimate but rare, so advisory rather than block. Now also catches `-r ''` and `-r ""` (empty replacement — silently strips matches from output), reported by aipotluckorg-3288452 as a same-class variant.

### Added
- New gotchas section entries, contributed by mars-1868431 via dispatch:
  - **rg**: respects `.gitignore` and hides hidden files by default — silently under-matches vs `grep -r` when the target lives in an ignored path. `.env` is the classic tripwire. Use `rg -uu` (or `--no-ignore --hidden`).
  - **fd**: same class — hides gitignored and hidden files by default (unlike `find`). Use `fd -HI`.

## v0.1.3 — 2026-07-23

### Added
- New SessionStart injection section: **Silent-failure gotchas** — classic-tool habits that fail QUIET (success + corrupted output) in the modern replacement, rather than loud (unknown flag, no output). Only surfaces per-tool when the tool is installed. Seeds two entries:
  - **rg**: `-r` is `--replace`, NOT recursive. `rg -rn PATTERN path` (grep -rn muscle memory) parses as `--replace=n` and silently rewrites every match to the literal "n" with exit 0.
  - **sd**: modifies files IN PLACE by default (unlike sed's opt-in `-i`). `sd 'foo' 'bar' file.txt` overwrites file.txt immediately.
- New suggest rule `rg-r-misfire` (advisory): flags `rg -r X` where X is a bare single letter matching a common rg short flag (`n l i w c v`). Targets the exact grep-rn habit-swap that motivated the gotcha entry. Quoted single-letter replacements (`rg -r 'n' file`) are excluded by shape, so no quote-aware suppression is needed. (Reported via dispatch by aipotluck-org after an hour lost misreading `env.CRON_SECRET` as `env.n` on a live auth path.)

## v0.1.2 — 2026-06-13

### Fixed
- `grep-head-trim` Fix text presented `grep PATTERN | head -N` → `grep -m N PATTERN` as a clean swap. `-m N` caps PER FILE while `| head -N` caps TOTAL across all files — they diverge on multi-file/recursive searches. Reworded to flag the divergence so the rule no longer silently misleads on multi-file greps. (Reported via dispatch by a sibling session.)

## v0.1.1 — 2026-06-13

### Fixed
- Block-mode rules (`uuoc`, `which-vs-command-v`) now suppress matches that land inside a single- or double-quoted shell string. Productive commands like `git commit -m "...which..."` and heredoc commit bodies are no longer refused. Quote walker tracks `'` and `"` with backslash-escape handling; `$'...'`, heredoc bodies, and backticks are NOT modeled (documented limitation).

## v0.1.0 — 2026-06-13

### Added
- Initial release. PreToolUse:Bash hook (`weir suggest`) with advisory + block-mode antipattern rules. SessionStart hook (`weir inject`) with modern-tool detection and tldr-pages idiom injection. CLI surfaces: `install`, `uninstall`, `status`, `suggest`, `inject`, `measure`, `build-idioms`.
