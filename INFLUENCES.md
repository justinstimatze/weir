# Influences

Work that shaped weir's design, sorted by lineage. Each entry includes
how it relates to weir specifically — what was lifted, what was deliberately
not lifted, and where the lineage diverges.

## Compiler / dataflow line of work

The strongest foundation. Shell-as-DFG (dataflow graph) thinking gave
weir the rule-table-as-data shape and the streaming-vs-blocking
classification waiting in layer 4.

- **PaSh** (Vasilakis et al.) — DFG-based parallelizing compiler for
  POSIX shell. [github.com/binpash/pash](https://github.com/binpash/pash).
  Ships an annotation language describing **parallelizability classes**
  per command (streaming, parallelizable-by-keys, blocking). The
  annotation JSON is directly reusable as weir's eventual layer-4
  streaming/blocking classifier. Now hosted by the Linux Foundation.

- **PaSh-JIT** (Kallas et al.) — runtime variant of PaSh; introduced
  the "practically correct" framing weir borrows for advisory rules.

- **Order-Aware Dataflow Model** (Handa et al.) — formal substrate where
  input consumption order matters for shell-pipeline semantics. Relevant
  if weir ever wants to prove rewrite correctness rather than rely on
  test corpora.

- **POSH** (Raghavan, Fouladi, Levis, Zaharia; Stanford) — data-aware
  shell that offloads I/O-bound stages to proxies near storage. Same
  annotation-driven architecture as PaSh, different optimization axis.
  Out of scope for weir but a useful reference for what annotation-driven
  optimization can do.

- **Smoosh** (Greenberg & Blatt) — mechanized POSIX shell semantics in
  Lem. Substrate for any *correct* static analyzer. weir's regex-based
  rule matcher sits at the un-rigorous end of the spectrum Smoosh
  anchors the rigorous end of.

- **Koala** (Lamprou et al.; Brown / NTUA / Stevens / UCLA; USENIX ATC
  2025) — [paper](https://www.usenix.org/conference/atc25/presentation/lamprou),
  [kben.sh](https://kben.sh). 126 real-world shell scripts in 14 sets
  with multi-tier input storage and characterization tooling. Applied
  to Shark, GNU parallel, hS, PaSh. Same Vasilakis / Kallas lineage as
  PaSh. **Not architecturally similar to weir** — Koala is evaluation
  substrate for shell-optimization *systems*, one layer below where
  weir sits. weir could be a *consumer* of Koala (run candidate rules
  over Koala scripts, diff outputs) but they don't compete.

## Surface tooling

What lint-tier shell analysis already looks like.

- **ShellCheck**. Mature but surface-only — quoting, expansion,
  word-splitting. Catches `cat X | grep` (SC2002) and a few other
  rules weir also catches. Misses streaming/blocking structure,
  modern-tool awareness, agent-specific behaviors. weir's rule
  table is partly inspired by ShellCheck's catalogue.

- **Ultimate Plumber** ([`up`](https://github.com/akavel/up)). Interactive
  REPL with live preview of each pipeline stage. Shows *output*, not
  *behavior*. Complementary, not competitive.

- **Nushell, PowerShell, Elvish, Oil, xonsh**. Structured-pipe shells.
  Solve bytes-vs-records at the language level rather than at the
  per-command annotation level. Adoption-limited; weir's job is to make
  POSIX bash *less* footgun-shaped, not to replace it.

## Agent-side tooling

The closest neighbors — tools that operate at the agent ↔ shell
boundary.

- **Warp** terminal — embedded multi-model agent + MCP. Closest production
  analog of "agent that writes bash." Treats bash as opaque text; does
  not analyze chains or push capability awareness. weir occupies the
  analysis layer Warp leaves empty.

- **Butterfish** (bakks) — capital-letter prompts + Goal Mode. NL → shell.
  No chain optimization or capability awareness.

- **Shell Genie**, **sgpt**, **aichat**. NL → command translators in the
  same vein.

- **NL2SH** (Westenfelder et al.) — NL→shell eval with a
  functional-equivalence heuristic. Measures *correctness* of generated
  commands. weir's eval at `data/eval/` is methodologically similar
  (synthetic prompts, mechanical grader) but measures *quality*
  (modern-tool reach + antipattern rate), not correctness.

- **Terminal-Bench** (Laude Institute) — agentic terminal tasks; an
  end-to-end task-completion benchmark. Could be used as a
  heavier-weight eval substrate for weir's block-and-retry effect
  (where the synthetic eval at `data/eval/` falls short).

## Documentation / cheatsheets

What weir's idiom layer (L3) competes with.

- **explainshell.com** — annotates man-page fragments for a given command.
  Lookup-only; no behavior analysis; no per-host awareness.

- **tldr-pages**, **navi**, **cheat.sh**, **eg**. Cheatsheet collections.
  weir's per-tool idioms (`internal/idioms/idioms.json`) are parsed from
  tldr-pages directly; weir adds the "filtered by what's installed
  on *this* host" angle that lookup tools don't.

## What weir does that the above doesn't

- **Agent-facing context injection** at SessionStart. Pushes capability
  awareness into the model's context before the model has to ask. None
  of the above does this.

- **Per-command lint + selective block** at PreToolUse. Combines
  ShellCheck-style lint with the ability to *refuse* the command and
  show the rewrite. Only the safest rewrites are gated this way.

- **Goal → pipeline idioms** filtered to installed tools. tldr teaches
  what *one tool* does; weir teaches *how tools compose* for a goal,
  scoped to what the host actually has.

- **Mechanical eval** with controlled comparison. Cross-model, scriptable,
  cache-friendly. Most agent-shell tools have no eval at all.
