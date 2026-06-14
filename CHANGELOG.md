# Changelog

All notable changes to weir are documented here. Format loosely follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions are git tags.

## v0.1.2 — 2026-06-13

### Fixed
- `grep-head-trim` Fix text presented `grep PATTERN | head -N` → `grep -m N PATTERN` as a clean swap. `-m N` caps PER FILE while `| head -N` caps TOTAL across all files — they diverge on multi-file/recursive searches. Reworded to flag the divergence so the rule no longer silently misleads on multi-file greps. (Reported via dispatch by a sibling session.)

## v0.1.1 — 2026-06-13

### Fixed
- Block-mode rules (`uuoc`, `which-vs-command-v`) now suppress matches that land inside a single- or double-quoted shell string. Productive commands like `git commit -m "...which..."` and heredoc commit bodies are no longer refused. Quote walker tracks `'` and `"` with backslash-escape handling; `$'...'`, heredoc bodies, and backticks are NOT modeled (documented limitation).

## v0.1.0 — 2026-06-13

### Added
- Initial release. PreToolUse:Bash hook (`weir suggest`) with advisory + block-mode antipattern rules. SessionStart hook (`weir inject`) with modern-tool detection and tldr-pages idiom injection. CLI surfaces: `install`, `uninstall`, `status`, `suggest`, `inject`, `measure`, `build-idioms`.
