# Contributing

weir is a small personal project maintained by [@justinstimatze](https://github.com/justinstimatze). PRs and issues welcome.

## Ground rules

- **Go stdlib only.** No third-party Go dependencies. If you think we need one, open an issue first. (`go.mod` currently has zero `require` entries beyond the module declaration.)
- **No network calls in any code path.** weir is purely local. The synthetic eval at `data/eval/` *does* call the Anthropic API, but that's a maintainer-only Python harness — it never runs as part of normal weir usage.
- **No LLM calls in any hook path.** Hooks must be fast and deterministic.
- **`guard.Hook(...)` wraps every hook entry point.** A panic inside weir must never break a Claude Code session — the recover sets exit code 0 and lets the tool call proceed.
- **Settings.json operations are non-destructive.** Every write backs up first to `<path>.weir-bak-<timestamp>` (nanosecond resolution). Uninstall removes only weir-owned entries (matched by binary-path prefix).

## Dev setup

```sh
git clone https://github.com/justinstimatze/weir
cd weir
go test -race ./...
go build .
```

Runs on the Go version pinned in `go.mod` (currently 1.26). CI uses
`go-version-file: go.mod`; local dev should track the same. Pure-stdlib;
no external Go tools needed beyond `go` and (for full lint parity with CI)
`golangci-lint`.

## Tests

Each pure-logic package in `internal/` has a `*_test.go` covering the
primary paths (suggest's rule corpus + JSON I/O, probe's schema + apt
mapping + dedup, inject's render + budget). The CLI surface
(install/uninstall/status/measure) is exercised via `scripts/smoke.sh`
— a 7-check install-cycle test against a temp settings.json with
pre-existing unrelated state.

Before submitting a PR:

```sh
go test -race ./...
go vet ./...
gofmt -l .                # must be empty
scripts/smoke.sh          # builds binary + runs cycle
golangci-lint run         # if installed locally
```

CI runs all of the above on every push/PR.

## Adding an antipattern rule

Rules live in [`internal/suggest/rules.go`](internal/suggest/rules.go) as a `[]Rule`. Each rule needs a unique `Name`, a `Pattern` (Go RE2), an optional `Suppress` regex (antidote — if matched, suppresses the rule even if Pattern matched), a one-paragraph `Fix` text, and an `Action` of `"advise"` (default) or `"block"`.

**Block mode is conservative.** Only mark a rule `"block"` if the suggested rewrite is mechanically lossless and unambiguous — Claude Code will refuse to run the command and force a retry, so a false positive blocks productive work. Two shapes have earned it. One is a safe rewrite (`uuoc`, `which-vs-command-v`, `git-add-all`, `rg-r-misfire-bundled`). The other is a destructive write whose *arguments reveal it was never meant to persist*: the `sd-in-place-write-*` escalations block on a secret-ish file operand, a redaction-shaped replacement, or an empty replacement, because someone typing `<redacted>` or `''` is masking for a screen. Their base rule stays advisory, because persisting a substitution is an ordinary thing to want.

Blocking also has a timing argument behind it that advisories cannot answer. For a destructive command the advisory text arrives in the same message as the tool *result* — after the write. A rule that is right and late is not a mitigation.

When adding a rule, also add at least one positive case + one tight negative case to [`internal/suggest/suggest_test.go`](internal/suggest/suggest_test.go). Cases like `"bmg describe -intent 'assess which mode the lens used'"` (a false-positive for the naive `which CMD` regex) catch the most important class of bugs. `TestEveryRuleHasAPositive` fails the build if you skip the positive.

Three tables, and picking the right one matters:

- `positives` — this command MUST fire this rule (it may fire others too).
- `negatives` — this command must fire NOTHING.
- `ruleNegatives` — this command must not fire *this* rule, but is free to fire others. Reach for this whenever a fixture is a true positive for one rule and a false positive for its neighbour, which is most of them once a tool has more than one rule. `sd '(x)' 'got:$1' file.txt` really is an in-place write and really is not a bad capture-group reference; putting it in `negatives` asserts something false.

**Measure the fire rate before you ship it.** A rule's pattern is a claim about a corpus, and the claim is usually wrong the first time. Sweep the host's own transcripts:

```sh
WEIR_SAMPLE=1 WEIR_SAMPLE_N=8 go test ./internal/measure -run TestSampleFires -v -timeout 25m
```

That prints every rule's fire count as a percentage of all Bash calls, plus up to N distinct matching commands each, so you can read what you actually caught. Set `WEIR_SAMPLE_RULE=<name>` to restrict the samples to one rule. It skips unless `WEIR_SAMPLE` is set, so it costs CI nothing.

Rough bands, from what has held up: **under ~0.5%** is a tripwire and fine; **1–2%** is defensible only if you have read the spans and they are overwhelmingly true positives; **above ~5%** wants narrowing whatever the samples say. A rule that fires constantly teaches the reader to stop reading the advisory tier, which costs more than the rule was ever going to save.

Read the spans before you accept a number in the 1–2% band, and read them before you accept a low one too. `pipe-eats-exit-status` measured 7.4% on its first draft, and the samples showed the dominant match was `make check 2>&1 | tail -15` — a correct, deliberate workflow, not the trap. Narrowed to publish and install verbs it measures ~2%, and the spans there are `git push 2>&1 | tail` and `pip install -e . -q 2>&1 | tail`, which are the trap exactly. Same band, opposite verdict; only the spans separate them.

To see which rule matched where in a single command, and whether its `Suppress` fired:

```sh
WEIR_SPAN='your command here' go test ./internal/suggest -run TestSpanProbe -v
```

## Adding a gotcha

Silent-failure gotchas live in [`internal/inject/inject.go`](internal/inject/inject.go) as a `[]gotcha`, surfaced unconditionally at SessionStart. Because that block is injected once and then resent at cache-read rates every turn until compaction, its size compounds far past what a single firing suggests — a 2026-08 token-cost audit found this section alone was over half of weir's entire SessionStart injection and the single biggest hook-cost line account-wide. Keep `Line` **terse**: mechanism, one concrete failure shape, the fix — no incident narrative, no second example. The full story belongs in the matching rule's `Fix` text in `rules.go`, which only gets paid for on the turn the risky command is actually typed.

Every gotcha describing a destructive habit needs a `Rule` field naming the `internal/suggest` rule that catches it live — `Rule: ""` is permitted only when the habit genuinely isn't regex-detectable (currently just `command-not-found`, which stays at full length as the sole place that hazard is ever explained). `TestGotchaRulesExist` fails the build if a non-empty `Rule` doesn't match a name in `suggest.Rules`, so a rename on the rules.go side shows up here instead of rotting silently.

`GotchaBudgetChars` caps the section the same way `IdiomBudgetChars`/`CompositionBudgetChars` cap theirs — belt-and-suspenders against the table growing unbounded again, since it once did.

## Adding a composition idiom

Cross-tool idioms live in [`internal/idioms/composition.json`](internal/idioms/composition.json) as `{intent, cmd, tools}` entries. `tools` is the list of binaries that must be present on the host for the idiom to surface. The idiom is filtered into the SessionStart block by `inject.go` based on the live probe.

A good composition idiom describes a *goal* (not a means), and the pipeline is *significantly better* than the obvious-coreutils approach to that goal. tldr-pages already covers "what one tool does"; the composition file is for "how tools combine."

## Commit style

Short imperative subject lines. Body explains *why*, not what. Reference issues as `#123`.

## Scope discipline

If a change adds a feature, there must be a one-paragraph argument for why it's on-by-default or doesn't exist at all. weir's product promise is "install once, then mostly invisible" — opt-in flags on the value path are a regression.

## Release process (maintainer)

1. Update README + ROADMAP + WHAT_DIDNT_WORK if relevant.
2. Tag `vX.Y.Z` on `main`.
3. `.github/workflows/release.yml` runs `goreleaser` automatically; binaries for linux/darwin × amd64/arm64 land on the GitHub release page.
4. Verify `go install github.com/justinstimatze/weir@vX.Y.Z` works.
