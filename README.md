# weir

> A barrier across a stream that's also the standard instrument for measuring its flow rate.

weir is a Claude Code addon that closes a specific gap: the model writes shell pipelines fluently, but reaches for a 1995 toolbox.

## The thesis

Mining one user's JSONL session transcripts across dozens of project directories (the sanitized aggregate is in [`internal/measure/baseline.json`](internal/measure/baseline.json)):

- **25,216 Bash invocations**
- **49.7%** contain a top-level pipe — the model writes pipelines constantly
- **0** of those calls reached for `rg`, `fd`, `bat`, `sd`, `mlr`, `eza`, or any other modern alternative — not "underused", *never reached for*
- A handful of antipatterns recur in volume — counts from the embedded baseline: `grep | head` (810), `ls | grep` (225), UUOC variants `cat FILE | tool` (~120 across head/grep/tail/jq/wc), `grep | wc -l` (99), `awk | awk` (38), `find ... -exec ... \;` (13). A separate pass-A discovery on the same corpus (`data/mine_extra.py`) added `which CMD` (~179), `find | xargs` without `-print0` (~61), and `ls | wc -l` (~152).

The model's pipeline *shapes* are fine. What's broken is which binaries it reaches for inside each shape, and a small cluster of stereotyped antipatterns.

## What weir does

Three layers, registered as Claude Code hooks. All fail-open; bypass with `WEIR_SUGGEST_SKIP=1` / `WEIR_SKIP=1`.

### Layer 1 — Capability manifest (SessionStart)

At the start of every session, weir probes `$PATH` for ~30 modern shell tools, emits a JSON manifest, and injects it into the model's context as `additionalContext`. The model now knows what's reachable on *this* host instead of defaulting to coreutils.

Sample output on a freshly-stocked Ubuntu 26.04:

```
[weir] Modern shell tools on this host:
- bat (prefer over cat) -> /usr/bin/batcat
- delta (prefer over diff) -> /usr/bin/delta
- fd (prefer over find) -> /usr/bin/fdfind
- jq (additive) -> /usr/bin/jq
- mlr (prefer over awk) -> /usr/bin/mlr
- rg (prefer over grep) -> /usr/bin/rg
- sd (prefer over sed) -> /usr/bin/sd
...
```

When tools are missing but apt-installable, weir surfaces a `sudo apt install ...` line at install time and in `weir status`.

### Layer 2 — Antipattern suggester (PreToolUse:Bash)

Before each Bash invocation, weir lints the command against ~12 rules. Two modes:

- **Advisory** (default): the suggestion is injected as context; the command still runs. Used for rules whose rewrites have edge cases.
- **Block**: weir refuses to run the command and shows the suggested rewrite; the model retries with the better form. Used only for *mechanically-safe* rewrites where the rewrite is lossless and unambiguous.

Currently block: `uuoc` (`cat FILE | tool` → `tool FILE`) and `which-vs-command-v` (`which X` → `command -v X`). Everything else stays advisory.

```
$ which python3
[weir-suggest] blocking — antipattern with a mechanical-safe rewrite:
- [which-vs-command-v] `which CMD` -> `command -v CMD`. `which` is non-POSIX
  with inconsistent cross-distro behavior — can't see shell functions/aliases,
  exit codes vary. `command -v` is POSIX, sees functions/aliases, exits
  non-zero cleanly when missing. Rewrite the command and retry.
(To bypass this block for the rest of the session, set WEIR_SUGGEST_SKIP=1 in the env.)
```

### Layer 3 — Idiom library (SessionStart, additive)

After the manifest, weir injects two more blocks:

1. **Per-tool idioms** from a parsed tldr-pages corpus — the first 2 examples per installed modern tool.
2. **Composition idioms** — 30 hand-curated goal-shaped pipelines (find files modified today and grep them; pretty-print every JSON in a tree; benchmark two command variants). Filtered to entries whose required tools are all present on the host; capped at 10 surfaced per session.

Layer 3 teaches "*how* to compose"; layer 1 teaches "*what's* installed."

## Install

Requires Go 1.26 or later.

```sh
go install github.com/justinstimatze/weir@latest
weir install
```

`weir install` does a non-destructive merge into `~/.claude/settings.json` — it backs up first, only adds its two hook entries (SessionStart + PreToolUse:Bash), and never touches other hooks. Re-running is idempotent; `weir uninstall` is clean.

If you want it scoped to one project instead of globally, point at a project settings file:

```sh
CLAUDE_SETTINGS=/path/to/project/.claude/settings.json weir install
```

## Subcommands

| command | what it does |
|---|---|
| `weir suggest` | PreToolUse hook entry point — reads JSON on stdin, emits suggestion JSON |
| `weir inject` | SessionStart hook entry point — renders manifest + idioms |
| `weir probe` | emit the capability manifest JSON for the current host |
| `weir install` | register weir's hooks; idempotent; backs up settings first |
| `weir uninstall` | remove weir's hooks; leaves unrelated hooks untouched |
| `weir status` | report which weir hooks are registered + suggest missing-apt tools |
| `weir measure` | re-mine `~/.claude/projects/**/*.jsonl` and diff against the embedded baseline (modern-vs-classic tool counts, per-rule hit counts) |
| `weir review CMD` | interactive spot-check — given a bash command, print whether weir would block, advise, or stay silent (use for rule tuning) |
| `weir build-idioms` | maintainer-only: parse a tldr-pages clone into `internal/idioms/idioms.json` (then rebuild the binary to re-embed) |

## How weir avoids being annoying

- **Fail-open everywhere.** Any error in the hook → silent exit 0, command runs. weir cannot break your session.
- **Block mode is conservative.** Only on rewrites where the substitution is mechanically lossless. Edge-case rules stay advisory.
- **Bypass is one env var.** `WEIR_SUGGEST_SKIP=1` disables suggest output for the session; `WEIR_SKIP=1` disables the SessionStart inject.
- **Suggestions are concise.** Rule fix-text is one paragraph max. SessionStart injection is bounded: the per-tool tldr idiom section caps at 2000 chars (~500 tokens), the cross-tool composition section caps at 1500 chars (~375 tokens), and the manifest itself scales with installed-tool count. Worst-case total on a richly-stocked host is ~1000 tokens.
- **Settings.json edits are non-destructive + reversible.** Every write backs up first to `<path>.weir-bak-<timestamp>`; uninstall removes only weir-owned entries.

## Architecture

Single self-contained binary (~2.6 MB, stripped). All runtime data (idiom corpus, baseline snapshot for `weir measure`) is embedded via `//go:embed` — no source-tree dependency at runtime.

```
main.go                        # subcommand dispatch + --version
internal/
  probe/                       # PATH discovery + apt-pkg mapping (+ symlink dedup)
  suggest/                     # rule table + match engine + selftest + review
  inject/                      # SessionStart prose renderer (manifest + idioms + composition)
  idioms/                      # per-tool (tldr) + composition idioms; build-idioms parser
  install/                     # non-destructive settings.json merge + status + uninstall
  measure/                     # corpus streamer + baseline diff (embedded baseline.json)
  guard/                       # panic-recover wrapper for all hook entry points
data/
  baseline_2026-05-20.json     # sanitized aggregate baseline (the empirical evidence)
  mine_pipes.py                # the original baseline-extraction script (Python, run rarely)
  mine_extra.py                # pass-A: hunt named antipatterns
  mine_discover.py             # pass-B: frequency-rank for unknowns
  eval/                        # synthetic eval harness (uv venv, prompts.jsonl, run/grade/report)
scripts/
  smoke.sh                     # install/uninstall cycle test (CI runs on every push)
  sanitize-baseline.py         # PII-strip raw mining output for the public artifact
```

## Prior art

The compiler/dataflow lineage (PaSh, PaSh-JIT, Koala, Smoosh, POSH),
surface-tooling lineage (ShellCheck, Ultimate Plumber), agent-side lineage
(Warp, Butterfish, NL2SH, Terminal-Bench), and documentation lineage
(tldr-pages, explainshell). Full citations + how each relates to weir
in [INFLUENCES.md](INFLUENCES.md).

## Roadmap

Layer 4 (streaming/blocking classifier), eval extensions (block-and-retry
simulation, cross-host probe), rule-table extensions, and distribution
work. See [ROADMAP.md](ROADMAP.md). Historical dead ends in
[WHAT_DIDNT_WORK.md](WHAT_DIDNT_WORK.md).

## License

MIT. See `LICENSE`.
