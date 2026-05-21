# Security

## Reporting a vulnerability

Email: **justin@justinstimatze.com** with `weir-security` in the subject. Please do not file public GitHub issues for suspected vulnerabilities.

Expect a response within a week. Fixes for high-severity issues will be published as patch releases; disclosure follows the fix.

## Threat model

weir runs entirely on your local machine. It never sends data over the network. It does NOT store any of your transcript data, prompt text, or model output on disk — its only writes are to Claude Code's settings.json (to register hooks) and to timestamped backups of that same file.

### What weir reads (read-only)

| path | when |
|---|---|
| `$PATH` directories (via `exec.LookPath`) | every SessionStart (probe) |
| `~/.claude/projects/**/*.jsonl`, `~/.claude/projects/**/subagents/*.jsonl` | only when `weir measure` is invoked manually |
| `~/.claude/settings.json` (or `$CLAUDE_SETTINGS`) | install / uninstall / status |
| Bash command text from PreToolUse hook stdin | every Bash tool call (in-memory only) |

### What weir writes

| path | contents |
|---|---|
| `~/.claude/settings.json` | weir adds two `hooks` entries (SessionStart + PreToolUse:Bash); never touches unrelated keys or hooks. |
| `~/.claude/settings.json.weir-bak-<timestamp>` | a verbatim copy of the file before each install or uninstall. Nanosecond-resolution timestamp. |
| stdout / stderr | hook-protocol JSON (for `weir inject` and `weir suggest`) and human-readable messages (for everything else). |

That's the complete list. No `~/.weir/` directory. No JSONL records. No salt, no hashed tokens, no caches.

### What never touches disk

- **Bash command text** that the PreToolUse hook examines. It's read from stdin, regex-matched in memory, and discarded when the process exits.
- **Probe results** for SessionStart. Rendered into JSON on stdout and not persisted.
- **Anything from `~/.claude/projects/**/*.jsonl`.** `weir measure` streams these files, counts matches, and discards them — the counts are printed; the per-line text is not.

### Adversary access levels

**Local attacker with your user privileges.**
- Can read your `~/.claude/settings.json` (which contains the absolute path to the weir binary that the hooks invoke).
- Can swap the weir binary at `~/go/bin/weir` for a malicious one (your hooks will run it).
- weir adds no new attack surface beyond the standard Unix filesystem trust boundary.

**Network attacker.**
- weir makes zero network calls in any hook or CLI path. The synthetic eval at `data/eval/run_eval.py` calls the Anthropic API, but that is a maintainer-only Python harness, never invoked as part of normal weir usage.

**Concurrent install attacker.**
- Two simultaneous `weir install` invocations are not file-locked. Last-write wins; either invocation's backup file preserves the prior state. Don't run `weir install` in parallel.

### Install-time risks

- `weir install` modifies `~/.claude/settings.json`. A timestamped backup is written first. Backups are not auto-deleted; rotate them yourself if you re-install repeatedly.
- The merge is done by deserializing settings.json to `map[string]any`, modifying the `hooks` subtree, and re-serializing. Go's `encoding/json` does NOT preserve key ordering inside maps — your settings.json keys may shift positions after `weir install` even though the data is preserved. The file still parses correctly.

### Uninstall guarantees

`weir uninstall` removes hook entries whose `command` field begins with the weir binary path (per `os.Executable()`). It never touches unrelated hooks, never touches non-hook keys, and never deletes the weir binary itself.

If you've moved the weir binary since installing, run `weir uninstall` from the *new* location of the binary (or manually grep `~/.claude/settings.json` for stale weir entries and remove them).

### Supply chain

weir is a pure-Go binary with zero third-party Go dependencies. Verifiable via `go.mod` having no `require` entries beyond the standard module declaration.

The module path is `github.com/justinstimatze/weir`. Install via `go install github.com/justinstimatze/weir@VERSION`. Pin a tagged version in scripted installs.

The embedded `data/idioms.json` is parsed from [tldr-pages](https://github.com/tldr-pages/tldr) at maintainer build time; the source URL and the `weir build-idioms` script that produces it are both in-repo.

The embedded `data/baseline_2026-05-20.json` is the sanitized aggregate of one maintainer's transcript mining; the sanitizer is at `scripts/sanitize-baseline.py`. It contains no PII, no project names, no literal user commands — only whitelisted-binary aggregate counts.

### Known limitations

- **Regex-based rule matching does not parse shell quoting.** A pipe inside a quoted string (e.g. `grep "a|b" file`) can trigger any rule that looks for an unquoted pipe. weir errs on the side of suggesting; for `block`-mode rules this means a rare false-positive will refuse a syntactically valid command. Bypass with `WEIR_SUGGEST_SKIP=1` in the environment.
- **Symlink-aware dedup uses `os.SameFile`.** If your `eza` and `exa` (or similar) binaries share an inode, only `eza` is reported in the manifest. If you want both surfaced, separate the binaries.
- **`weir build-idioms` reads from a path you supply** (`--root`). The default is `/tmp/tldr/pages`. The script does NOT validate that the path is a tldr-pages clone — bad input produces an empty idioms file rather than corrupting state.
