# weir synthetic eval

A "does prompt-side injection actually change bash habits" eval that
resolves in hours, not weeks. Designed to a specific quality bar (the
"not bullshit" bar):

1. **Neutral prompts.** Each prompt describes the GOAL, not the means.
   No "use ripgrep to..." or "give me a pipeline that..." leaks the answer.
2. **Grounded in real workflows.** Prompts are reverse-engineered from
   `data/baseline_2026-05-20.json`'s antipattern samples and from
   common shapes in the user's transcript corpus — not invented from
   the model's training-data priors.
3. **Mechanical grader.** No LLM-as-judge. The grader extracts bash
   commands from model output and runs them through `weir suggest`
   (the same binary the production hook uses). If the model writes an
   antipattern, the production rule fires; the eval counts the fire.
4. **Honest control arm.** Same prompts, same model, same temperature,
   same sample count, same parser. The ONLY difference is whether
   weir's SessionStart text is in the system prompt.
5. **Per-rule reporting.** Not just aggregate "antipattern count went down."
   For each rule, control rate vs treatment rate vs delta. So we can
   see WHICH suggestions actually land.
6. **Honest about scope.** This eval measures **advisory-mode injection
   effect only.** Block-mode effect requires Claude Code's actual
   PreToolUse hook to fire, which means spawning Claude Code as a
   subprocess (deferred to v1). The eval text says so on every report.

## What's measured

For each (prompt, condition) pair:
- The model produces a response, likely containing 1+ bash commands.
- The grader extracts every code block / inline `bash` snippet.
- For each command, `weir suggest` is invoked to check if any rule fires.
- Counts per rule are aggregated per condition.

Conditions:
- **Control.** Vanilla system prompt; no weir injection. Model has no
  knowledge of installed tools or antipattern rules.
- **Treatment.** System prompt prepended with the LIVE output of
  `weir inject` (the same text Claude Code's SessionStart hook injects
  into a real session).

## How to interpret

For each rule:
- **Treatment rate << Control rate** = injection is moving the needle for that rule.
- **Treatment rate ≈ Control rate** = injection isn't enough; either escalate
  the rule to block-mode or rewrite the inject prose.
- **Treatment rate > Control rate** = bad — injection is making the model
  WORSE somehow (maybe by surfacing the wrong tool). Investigate.

Modern-tool counts (`rg`, `fd`, `mlr`, ...) work the same way but inverted:
treatment SHOULD show higher modern-tool reach than control.

## How to run

Requires Python 3.11+ and an Anthropic API key. Setup uses [`uv`](https://docs.astral.sh/uv/)
for the local venv (PEP 668 makes raw `pip install` painful on stock
Ubuntu); fall back to `python3 -m venv` if you prefer.

```sh
# one-time setup (creates data/eval/.venv)
cd data/eval
uv venv .venv
uv pip install --python .venv/bin/python anthropic

# put your key in .env (gitignored)
cp .env.example .env
$EDITOR .env   # paste ANTHROPIC_API_KEY=...

# run a single eval (15 prompts × 2 conditions × 5 samples = 150 API calls)
.venv/bin/python run_eval.py     # API calls + caches raw responses to cache/
.venv/bin/python grade.py        # extracts bash, applies `weir suggest`, writes graded.json
.venv/bin/python report.py       # prints the delta table
```

Expected wall time: ~5-10 minutes per full run (most time is API latency;
caching makes re-runs near-instant for already-completed prompts).
Cost: ~$0.50-$1.00 per full run with Opus 4.7 at current pricing.

To add new prompts: append to `prompts.jsonl` and re-run (cached
prompts won't be re-called). To test a SINGLE prompt during iteration:
`python3 run_eval.py --filter "log analysis"`.

## Scope and known limits

- **Advisory-mode only.** The synthesized control vs treatment measures
  what the *first-attempt* bash looks like. It doesn't simulate the
  block-and-retry loop. To eval block effect properly we'd need to
  spawn Claude Code itself; deferred.
- **Single model.** Eval uses claude-opus-4-7. Other model families
  (Sonnet, Haiku, GPT, Gemini) might respond differently to injection
  and need separate runs.
- **Temperature 0.7.** Variance across samples is the point — we want
  to see how OFTEN the model defaults to the antipattern, not what it
  does with one deterministic sample.
- **Prompt count (15) is a v0.** Statistical power is modest; treat
  first-run numbers as directional, not authoritative. Expand prompts
  to 50+ before drawing strong conclusions.
- **Reverse-engineering from corpus introduces curator bias.** The
  prompts I wrote inevitably reflect what *I* think a real task would
  look like. Let real-usage transcripts add to the prompt set over time.

## Files

| file | what |
|---|---|
| `prompts.jsonl` | one prompt per line: `{id, category, prompt, expected_rules?}` |
| `run_eval.py` | API caller; caches responses by hash to `cache/` |
| `grade.py` | extracts bash, runs `weir suggest`, writes `graded.json` |
| `report.py` | reads `graded.json`, prints control-vs-treatment table |
| `cache/` | gitignored; deterministic re-runs read from here |
