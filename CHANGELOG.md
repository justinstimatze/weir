# Changelog

All notable changes to weir are documented here. Format loosely follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions are git tags.

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
