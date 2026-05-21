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

**Block mode is conservative.** Only mark a rule `"block"` if the suggested rewrite is mechanically lossless and unambiguous — Claude Code will refuse to run the command and force a retry, so a false-positive blocks productive work. The current block rules (`uuoc`, `which-vs-command-v`) meet this bar; most don't.

When adding a rule, also add at least one positive case + one tight negative case to [`internal/suggest/suggest_test.go`](internal/suggest/suggest_test.go). Cases like `"bmg describe -intent 'assess which mode the lens used'"` (a false-positive for the naive `which CMD` regex) catch the most important class of bugs.

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
