# What didn't work

A running log of dead ends, false starts, and decisions reversed during the build of weir. Each entry should describe what was tried, why it failed, and what we did instead. Future maintainers (and future-me) should be able to skim this before re-proposing something already tried.

## bash + Python prototype — kept "because it worked"

Layers 1–3 first shipped in bash (`bin/probe.sh`, `bin/inject.sh`, `bin/install.sh`) and Python (`bin/suggest.py`). The Python PreToolUse hook had a ~41 ms cold-start, vs ~2.7 ms for the Go binary measured under hyperfine. Across a typical session that adds up to seconds of wall time burned on interpreter startup, plus brittleness from `python3 path/to/suggest.py` distribution.

The default I instinctively picked — "don't migrate working code" — was wrong. Migration cost was ~3 hours; ongoing friction would have been forever. The reframe came from the user: "is python actually giving us anything? my default is go." Recorded as a global preference in `~/.claude/CLAUDE.md`.

**Lesson:** for hot-path hooks, never Python. Default to Go even when the existing code works.

## "Re-mine weekly" as the measurement loop

First framing of measurement was "re-run `weir measure` weekly and watch the modern-share line move." This is a metric, not a decision input. For the discrete decisions actually on the table (ship v0.1, graduate more rules to block, kill rules), weeks-long lag is useless.

Replaced with the synthetic eval at `data/eval/`: ~50 neutral prompts grounded in the user's antipattern profile, run against three models with and without weir's SessionStart text in the system prompt, graded mechanically by piping each generated command through `weir suggest`. Resolves in ~10 min wall time per run.

**Lesson:** when the decision is discrete, build an evidence pipeline, not a metric pipeline.

## Pass-A mining regexes without statement-separator anchors

First version of `data/mine_extra.py` used `[^|]*` for inter-token gaps. Multi-line commands like `cmd | head -20\necho '---'\n... | tail -10` matched the `head_pipe_tail_range` rule even though they're two independent statements. Inflated counts ~3–12× for chain-shaped rules.

Fix: use `[^|\n;&]*` to forbid spanning shell-statement separators. Cut `head|tail` count from 238 → 20, killed multiple phantom "antipattern" categories (`head|head`, `tail|tail`, `cd|head`). Same fix later applied per-statement in `data/mine_discover.py`.

**Lesson:** when mining shell, decompose by statement first (`;`, `&&`, `||`, `\n`), then by pipeline. Naive top-level pipe splitting is necessary but not sufficient.

## `uuoc` rule that blocked legitimate `mlr cat`

The `uuoc` rule was promoted to block mode because the rewrite is mechanically safe (`cat FILE | tool` → `tool FILE`). But the regex `\bcat\s+[^-\s]\S*\s*\|\s*(grep|head|tail|...)` doesn't know that `cat` is a verb in mlr's grammar (`mlr --c2p cat file.csv`). The synthetic eval surfaced this when treatment-condition Opus + Sonnet started emitting `mlr --c2p cat /tmp/sales.csv | less -S` and getting flagged.

In production, with uuoc in block mode, this would have *blocked the model from a correct use of mlr that weir's own manifest was simultaneously recommending*. The eval caught it before any real user ran into it.

Fix: anchor `cat` to a statement boundary (`(?:^|[;&\n]\s*|&&\s*|\|\|\s*|\|\s*)cat ...`) so it only matches when `cat` starts a statement, not when it's an arg to another command.

**Lesson:** block-mode rules need eval coverage from realistic prompts, not just isolated test cases. Rule patterns must consider that the matched keyword might be a verb/arg in another tool's syntax.

## Eval grader counted `[weir-suggest]` as a rule name

First version of `data/eval/grade.py` extracted rule names from `weir suggest` output via `re.findall(r"\[([a-z][-a-z]+)\]", text)`. This caught the banner prefix (`[weir-suggest] antipattern(s) in this Bash command:`) along with the real `[rule-name]` markers, reporting "weir-suggest 49.3% control / 0% treatment" as if it were a rule.

Fix: explicit `BANNER = {"weir-suggest", "weir"}` filter set in the grader. Trivial change; only embarrassing because it was the first row of the very first report.

**Lesson:** when the artifact you're parsing contains both data and chrome, name the chrome explicitly. Regex catch-alls are fine until they catch your own structure.

## Treating synthetic-eval numbers as production-effect predictions

The first eval result showed a +70pp swing in modern-tool share. The natural impulse is to claim "weir produces a 70-percentage-point shift in real-world Claude Code sessions." That overclaims.

The eval's *treatment* system prompt is **only** weir's `inject` output (~1000 tokens). In real Claude Code sessions, Claude's own system prompt is much longer, and weir's text is one block among many. Production effect will be smaller — probably substantial, but not 70pp. The eval measures the **upper bound of prompt-side intervention efficacy**, not production effect.

The honest framing was added to the eval README and to commit messages. The number isn't wrong; the unconditional reading of it would have been.

**Lesson:** eval conditions diverge from production. When reporting a result, explicitly name what the production analog is and how it differs.

## Binary-update timing during a self-modifying bash call

When `go install` runs from inside a bash command, the PreToolUse hook for that bash call fires **before** the install completes, so the hook runs the *previous* binary. Looked like the new code wasn't taking effect; really it just takes effect on the *next* bash call.

No fix; documented as a known quirk in `SESSION.md`. Worth knowing only if you're surprised by it.

## Per-tool idiom rendering: alphabetical truncation

First version of `inject`'s idiom block listed installed tools alphabetically. When the cumulative character count hit the budget cap, truncation hit the alphabetical *tail* — which happened to be `rg`, `sd`, `sponge`, `tail` (the ones most worth surfacing because they replace coreutils everyone reaches for).

Fix: sort `present` so `replaces != null` tools come first, additive tools last. The truncation now clips additive idioms (less load-bearing) instead of coreutils-replacers.

**Lesson:** when a budget cap can truncate, sort the input by importance, not by name.
