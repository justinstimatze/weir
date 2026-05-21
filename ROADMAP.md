# Roadmap

Things weir doesn't do yet, ordered by leverage. v0.1 ships layers 1, 2,
and 3. Layers 4 and the eval extensions below are future work.

## Layer 4 — Streaming/blocking classifier (research extension)

Static analyzer that classifies each pipe stage as:
- **streaming** — emits output as input arrives (`grep`, `sed`, `head`, plain `awk` actions)
- **blocking** — consumes the whole input before emitting anything (`sort`, `sort -u`, `wc`, `tail` without `-f`, any `awk END {…}` body that touches state)
- **bounded-window** — emits after consuming a fixed prefix or suffix (`head -n N`, `tail -n N`)

Note that some commands are conditionally blocking depending on flags
or program structure — `awk` per-record actions are streaming, but the
`END {}` block runs only after EOF; `tail -f` is streaming while `tail -n N` is bounded.

Surfaces "this stage will buffer the whole input" warnings before the
model writes commands that hang on infinite streams.

Seeded from PaSh's annotation JSON (~60 commands classified); weir would
add per-command flag awareness (e.g. `tail -f` is streaming, `tail -n N`
is bounded-window, plain `tail` is blocking).

Lowest immediate ROI; most novel research surface. Should be a paper
before it's a feature. Resist scope-creeping into it before layers 1-3
have real-deployment data behind them.

## Eval extensions

The current eval at `data/eval/` measures **advisory effect** under
**idealized conditions** (weir-inject-only system prompt; no Claude Code
context). Two natural extensions:

- **Block-and-retry simulation.** Multi-turn harness: when the model
  writes a blocked command, inject the deny reason as a tool-result
  and resample. Measures whether the model adopts the suggested
  rewrite on turn 2. Currently advisory-only — block effect is a
  guess based on rule-fire patterns. Requires more than the current
  one-shot API harness.

- **Real Claude Code subprocess eval.** Spawn Claude Code as a
  subprocess, run a task suite, scrape the resulting transcripts for
  rule-fire rates and modern-tool counts. Production-conditions
  measurement instead of idealized. Heavier-weight: needs a task
  framework, fixture management, transcript parsing.

- **Cross-host probe re-run.** The baseline mining was done from one
  user's transcripts on one host. Re-running `weir measure` on other
  hosts (different Linux distros, macOS, different installed-tool
  sets) would confirm whether weir's "the model doesn't reach for rg"
  finding generalizes or is host-specific.

- **Expand prompts.jsonl past 52.** Statistical power is modest at 52;
  the eval result is directional. Doubling the prompt set tightens the
  confidence interval. Hand-curation is the rate limiter.

## Rule-table extensions

The current 12 rules came from the empirical baseline + the pass-A
hunt + the pass-B discovery + one promotion (`head-tail-range`).
Future candidates:

- **`ls -l | awk` for column extraction** → `stat -c '%n %s'` or `find -printf` (avoid ls's column shifting on filenames with spaces).
- **`curl | jq` without `-s`** → ensure flag matches request shape (POST vs GET, content-type).
- **`tar | tar` (re-tarring across hosts)** → `tar -C ... -xf -` direct, or `rsync`.
- **`mkdir then cd` two-step** → `mkdir -p X && cd X` or shell function.

These haven't been promoted because they need either eval coverage to
prove they're real wins, or examples from a corpus to confirm the
recurrence rate justifies the rule weight.

## Rule graduations (advisory → block)

Block-mode is currently uuoc + which-vs-command-v. Candidates for
graduation (only with eval evidence supporting the safety of the
rewrite under production block-and-retry):

- **`ls-pipe-wc-l`** — `ls | wc -l` rewrite is more verbose
  (`arr=(DIR/*); echo ${#arr[@]}` with `shopt -s nullglob`). Verbosity
  is the trade-off; safety case is solid.
- **`find-xargs-no-null`** — rewrite requires changing BOTH ends of the
  pipe. Cleaner result but the user (or model) has to think about it.

Neither is obviously a block-mode candidate yet. Need eval data showing
that block doesn't cause more retries than the suggestion is worth.

## Distribution

- **Brew tap** for macOS users.
- **Apt PPA** or Debian package for Linux.
- **A `weir-quickstart` script** that installs the binary, runs `weir install`,
  and explains what was added in human prose.

## Performance

- **PreToolUse hook startup is 2.7 ms.** Plenty of headroom; no perf work
  warranted unless a session shows 1000+ Bash calls per session AND the
  hook becomes a measurable fraction of wall time.
- **`weir measure` streaming** is dominated by JSON-decode time per line;
  could be 2-3× faster with a hand-rolled scanner, but it runs rarely
  enough not to matter.

## Documentation

- **A short blog post / launch piece** walking through the thesis →
  eval → result → caveats arc. Would lift this from a private repo to
  something people can find and decide whether to install.
- **Per-tool idiom contributions guide.** Currently `internal/idioms/composition.json`
  is hand-curated; the corpus would benefit from community PRs. Need
  a CONTRIBUTING.md scoping what makes a good composition idiom.
