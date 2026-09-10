# Changelog

All notable changes to weir are documented here. Format loosely follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions are git tags.

## v0.1.11 — 2026-09-10

Scoped weir's own rule-fire sweep to individual projects instead of the host-wide corpus: density ranged 1.3%–20.7% of Bash calls across 85 real projects, median 6.3%. Project type genuinely predicts whether the gotcha section's hazards ever come up — the SessionStart cost was being paid identically regardless.

### Added
- **Gotcha muting.** A rule-gated gotcha is dropped from the SessionStart block when its matching `suggest.Rule` has never fired anywhere in that project's own transcript history, once that history clears 200 Bash calls (below the bar, or on any failure, everything shows — fail open, never fail closed). The live `PreToolUse` rule is unaffected; only the SessionStart *reminder* copy is ever muted. `internal/rulehistory` (new package) is the first persistent, weir-owned, cross-invocation cache weir has needed — `<os.UserCacheDir()>/weir/projects/<name>.json`, self-invalidating on a `suggest.RuleSetFingerprint()` mismatch so a rule-pattern edit can't leave stale "never fired" data around.
- **`WEIR_GOTCHAS=always`/`never`/`auto`** overrides the computed decision, checked before any cache I/O. Muting is never silent: a rendered block with anything muted carries a one-line count naming the override.

Validated against the real shipped code path across the whole account's project fleet, not just weir's own repo: of 95 projects with ≥20 Bash calls, 34 clear the evidence bar. `trig`/`sluice` mute 10 of 11 gotchas (2,658 → 732 bytes, a 72% cut to the section); `lexicon`, the densest project at 41,086 Bash calls, mutes only 1 (2,658 → 2,634 bytes) because a project that heavily exercised has already hit almost every hazard at least once. Aggregate across the fleet: 252,510 → 183,547 gotcha-section bytes, a 27.3% reduction, recurring every session after the first.

### Docs
- README now covers the gotcha layer and `WEIR_GOTCHAS` (previously undocumented outside `CONTRIBUTING.md`), states plainly that SessionStart's value scales with how much raw shell work a project actually does, and lists `internal/rulehistory/` in the architecture tree.

## v0.1.10 — 2026-09-08

### Added

- **`rg-cap-l-misfire`** (advisory): grep's `-L` (files without a match) and rg's `-L` (`--follow`, symlinks) collide with no parse error either way. Found by a systematic diff of every `--help` flag letter shared between a classic tool and its weir-suggested replacement, run against all 10 tracked pairs rather than waiting for a live incident the way `rg-r-misfire` and `rg-h-is-help` were both found. 98 letters collide across the 10 pairs; this is the one that cleared the bar for a new rule. The rest are either in read-only tools (wrong output, never a wrong file) or self-defending (the muscle-memory value fails to parse instead of silently misbehaving). Advisory, not block: unlike `-r`/`-h`, nothing in the command text separates a real `--follow` intent from the grep-muscle-memory mistake, and rg's real files-without-match flag has no short form to substitute in.
- **macOS coverage.** `release.yml` gained a `macos-check` job, gated on the same `v*` tag `goreleaser` already builds `darwin/amd64`+`arm64` binaries from — the binaries existed before anything ever ran on the platform they target. Runs `go vet`/`test -race`/`build`/the install smoke test on `macos-latest`, plus a step dumping real `grep`/`sed`/`find`/`ps`/`awk` `--help`/`man` text to the CI log. Also added `workflow_dispatch` so it can run as a standalone smoke check before a tag exists — `release`'s own job gets `if: startsWith(github.ref, 'refs/tags/')` so a manual dispatch run can never reach goreleaser's publish step. Deliberately not on the `ci.yml` push/PR path — `macos-latest` bills at roughly 10x the Linux per-minute rate on a private repo.
- **macOS audit, closed against real Darwin.** Started against FreeBSD's own man pages as the closest reachable proxy without a Mac, then confirmed for real by triggering `macos-check` manually before this release existed as a tag. `grep -m`/`-r`, `find -exec ... {} +`, and `sed`'s `-i extension` synopsis all match the proxy's findings exactly on the actual `macos-latest` runner. `ps --help` shows no long (`--`) options at all, and `awk` is BWK's minimal, POSIX-only "one true awk" — neither tool has a rule that parses its flags directly (`ps-grep-vs-pgrep`/`awk-awk` are both pipe-shape rules), so both check out with nothing to change. `grep-head-trim`, `find-exec-semi`, and the three `rg-*-misfire` rules need no changes on macOS.
- **`bsd-sed-i-mandatory-arg`** (advisory, `OS: "darwin"` only): BSD `sed -i` takes a mandatory argument glued to the flag where GNU's is optional, the same shape of landmine `sd-in-place-write` already covers for the other tool — `sed -i 's/x/y/' file` reads `'s/x/y/'` as the extension and `file` as the sed script, leaving no file operand and never touching the source. `Rule` gained an `OS` field to make this safe: empty fires everywhere, a `runtime.GOOS` value restricts it, checked against a package-level `goos` var (not a bare `runtime.GOOS` call) specifically so tests can force the darwin branch without a Mac. First platform-gated rule this engine has ever had.
- **`internal/probe`'s fd/bat lookup, confirmed already correct on macOS.** An earlier pass through this session claimed the binary-name table needed a macOS branch — asserted without reading `probe.go` first, and wrong. The catalog already stores canonical names and tries them before ever falling back to the Debian `fdfind`/`batcat` alias, so `exec.LookPath("fd")` resolves directly wherever Homebrew installs `fd` under its own name. No code change; two new tests (`TestCanonicalNameResolvesWithoutDebAlt`, `TestDebAltFallbackStillWorks`) lock in both lookup paths against a fully isolated `$PATH`.

### Fixed

- **`sd-in-place-write`, `sd-in-place-write-secret-file`, `sd-in-place-write-redaction`, `sd-in-place-write-empty`** could match across a newline. Go's `\s` includes `\n`, so a safe, documented 2-arg stdin-form `sd` invocation got blocked when the whitespace gap between positional arguments consumed the newline ending that statement and read the next, unrelated statement's first word as sd's FILE argument. New `argGap` (`[ \t]+`) fixes all four.
- The same shape, swept across the rest of `rules.go`, turned up in eighteen more rules. `grep-head-trim`, `ls-grep`, `grep-wc`, `find-exec-semi`, `sort-uniq`, `awk-awk` used an unbounded `[^|]*` gap that six newer sibling rules in the same file had already migrated away from. `which-vs-command-v`, `git-add-all`, both `rg-r-misfire` rules, `pkill-f-self-match`, `rg-h-is-help`, `rg-cap-l-misfire`, `rg-ignore-file-hides-target` (Pattern and Suppress), `grep-dash-pattern` (Pattern and Suppress), `pgrep-f-self-match` (both branches), and both `sd-replacement-shell-var` rules carried the identical bare-`\s` shape. `rg-r-misfire-bundled` is `Action: block` — a false match there stopped a real command from running, not just an unwanted advisory. All eighteen adversarial cases are now permanent regression fixtures in `ruleNegatives`.
- The first real `macos-check` run (`workflow_dispatch`, before this version had a tag) failed on its own test suite: a `ruleNegatives` fixture for `bsd-sed-i-mandatory-arg` asserted the rule must not fire, but never overrode `goos` — an implicit assumption that "ambient" means Linux, true on this dev host and `ci.yml`'s `ubuntu-latest`, false on `macos-check`'s actual Darwin runner. `TestRuleNegatives` now looks up each fixture's target rule and forces `goos` to a guaranteed-different platform for any OS-gated one, rather than trusting whatever host happens to run `go test`. Confirmed fixed on a second real `macos-check` run.

## v0.1.9 — 2026-09-05

Fourth sighting of `pipe-eats-exit-status`, reported from aipotluck.org: the advisory fired on exactly the shape it names — `git push ... 2>&1 | tail` from a background task — and the command ran anyway. The rejected push (a failing pre-push hook) then reported "completed (exit code 0)" to the harness, briefly trusted as proof the push had succeeded.

### Changed
- **`pipe-eats-exit-status` is now BLOCK, not advise.** Same escalation logic already used for the `sd-in-place-write-*` and `sd-replacement-shell-var-no-capture-group` rules: for a command whose whole point is a status nobody else will see directly — a background runner, a CI step, an agent harness's own "exit code 0" summary — the advisory text arrives in the same turn as the tool result, after the pipeline has already run and already reported the wrong status upstream. `stateChanging` keeps this narrowly scoped (git push/pull, go install, cargo install/publish, pip install, npm publish, terraform apply/destroy, kubectl apply, docker push, gh release, rsync, scp piped to tail/head/grep/rg) — measured at ~0.1% of calls — so the block bar (a rewrite that's always safe, never a false trap) holds: `set -o pipefail`, `PIPESTATUS`, or redirect-to-file are all always-correct fixes with no case where the piped-and-silently-wrong form was actually the intended behavior. `pipe-status-echo` (the sibling rule for an explicit `echo $?` read) stays advisory — it's not scoped to `stateChanging` and a deliberate check of a filter's own exit status is a real, if rare, thing to want.

## v0.1.8 — 2026-09-03

Second sighting of `sd-replacement-shell-var`, reported from a sibling session (aipotluck.org, 2026-09-02): the rule fired, printed its fix, and the corruption landed anyway. The command was editing TypeScript source, not a shell script — the result was syntactically valid, just wrong (two import paths silently lost their `$` prefix), with no runtime to fail fast and nobody re-reading the diff before it moved on.

### Fixed
- **`sd-replacement-shell-var` now catches a double-quoted, backslash-escaped `$NAME` directly.** The reported command only matched the existing pattern by coincidence — its double-quoted replacement happened to contain literal single quotes (from the TypeScript import syntax itself) that satisfied the rule's single-quoted branch. Checked independently: a clean double-quoted `"...\$NAME..."` with no stray quotes anywhere wasn't caught at all before this fix. The pattern now has an explicit branch for it, so the shape fires for the right reason instead of a lucky accident of the surrounding source text.

### Added
- **`sd-replacement-shell-var-no-capture-group`** (BLOCK): narrower than the base rule — fires only when the search pattern defines zero capture groups of any kind (named or plain), the state where `$NAME` cannot resolve under any reading. An advisory can be skimmed, and this failure gives no second chance to notice; the base rule stays advisory for the ambiguous cases (a real numbered or named group present).
- Base rule's `Fix` text now says the failure is silent and deferred — exit 0, no warning, and the result can look like valid source rather than a parse error — a cost the original text didn't name.

Measured post-fix: 39 fires / 0.021% of 188,432 corpus calls — comfortably inside the tripwire band, spans read clean.

## v0.1.7 — 2026-08-31

A cross-project 7-day token-cost audit (real API usage data from Claude Code transcripts, resend-weighted by how many turns each injected block sits in context before compaction) found weir's `SessionStart` hook was 13.9% of all hook+MCP spend account-wide and 85%+ of all hook cost specifically — bigger than every other maintained hook or MCP server combined except `linear`. The cause wasn't call frequency: `weir suggest` (`PreToolUse:Bash`) only costs anything on the rare turn a matching command appears, measured at 637 bytes per single-rule fire. `weir inject` (`SessionStart`) fires once, unconditionally, every session, and then gets resent at cache-read rates on every subsequent turn until compaction — a block injected once at t=0 and read back 100+ times compounds past what its single firing suggests.

### Changed
- **The silent-failure gotcha section is now terse, and capped.** Measured before this change: the gotcha section was 4,970 of the injection's 9,679 bytes — over half the entire SessionStart block, and the only section of the three (idioms, compositions, gotchas) with no byte budget. It had grown from 4 entries to 11 across recent field-report sessions with nothing watching its size.

  Ten of the eleven gotchas already have a matching `PreToolUse` rule in `rules.go` that explains the same failure in full at the moment the risky command is actually typed — verified against the rule table by name, not assumed. So `Line` now carries only the mechanism, one concrete failure shape, and the fix; the incident narrative and secondary examples that used to live here stay exactly as detailed as before in the matching rule's `Fix` text, which only gets paid for when it's earned. `command-not-found` (no matching rule — absence of a binary isn't regex-detectable) is the one exception and keeps its original length, since it's the only place that hazard is ever explained.

  A new `Rule string` field on the `gotcha` struct names that matching rule; `TestGotchaRulesExist` fails the build if one drifts. A new `GotchaBudgetChars = 3000` caps the section the same way `IdiomBudgetChars`/`CompositionBudgetChars` already cap theirs.

  Measured after: gotchas section 4,970 → 2,799 bytes (−43.7%), full `weir inject` output 9,679 → 7,469 bytes (**−22.8%**). No gotcha was dropped — all 11 still render at today's content size, well under the new cap.

  This is deliberately not a deletion of the SessionStart copy now that a rule exists for each. The `sd`-in-place gotcha proved once already that prose-only SessionStart warnings aren't sufficient on their own (it sat here alone for two releases and still didn't stop the loss it described, which is why every destructive gotcha now has a matching rule) — removing the priming layer entirely would undo that lesson. This change only stops paying the always-resent tax for detail that a second, cheaper, better-timed surface already delivers.

## v0.1.6 — 2026-08-07

Five field reports arrived between v0.1.5 and now, and one of them opened by measuring weir against itself: ten advisory fires in about thirty minutes, nine judged false positives, five of them the same shape. That report asked for its own new rule to be held until the noise was fixed, on the grounds that a tier which is mostly wrong teaches the reader to stop reading it — which is how a correct advisory sitting in that session's context at 9am failed to prevent the mistake it described at 4am.

So this lands in that order: the precision fixes first, then the eight new rules. Every rule here — new and pre-existing — was swept against this host's transcript corpus with `weir measure` before shipping, and two of the new ones were narrowed on what came back. Corpus is ~210,300 Bash calls since 2026-06-13 and still growing as sessions run; fire rates are a percentage of that.

Two of the new rules describe traps specific to how Claude Code runs Bash, which is `bash -c '<the whole command>'`. That single fact is what makes `pgrep -f` match the shell asking the question.

### Fixed
- **`sd-in-place-write` and its three block escalations no longer read a pipe as a positional argument.** All four counted `sd PAT REP FILE` using `[^\s'"]\S*` for a bare argument, and `\S*` matches `|`, `>` and `<` happily. So the second pipe in `curl -sL URL | sd '<[^>]+>' '' | head -40` was consumed as an argument, `head` landed in the file-operand slot, and the rule fired — on the stdout form that weir's own block rules recommend as the safe alternative. The redirect form `... | sd PAT REP > out.html` failed the same way. Positional arguments are now a shared `shWord` fragment that excludes shell metacharacters outright.
- **`\bsd\b` no longer matches inside an identifier.** `rg -n "sd-in-place-write" -A18 internal/suggest/rules.go` fired the rule, because `-` is a non-word character and `sd` is two letters. The four sd rules now anchor to a command position (statement start, or after a newline, `;`, `&&`, `||`, or a pipe) the way `uuoc` and `which-vs-command-v` already did. The report that named this bug had it fire while the report was being written; reproducing it here fired it again, on the command that went looking.

  Measured effect of the two fixes together: `sd-in-place-write` drops from 2,041 fires to 1,043 — 998 of its fires, 49%, were false. The remainder are real three-argument in-place writes.

  This also closes the open follow-up from v0.1.5, which weighed promoting the base rule to block on the grounds that block-action rules get the `isInsideShellString` guard and advisory rules do not, so blocking might *reduce* false positives. Anchoring to a command position fixes the quoted-string matches directly, at the tier the rule belongs at. The base rule stays advisory: legitimate persistence is a real use case and 1,043 corpus fires are mostly it.
- **`ps-grep-vs-pgrep` no longer recommends the trap.** Its Fix text ended "For killing: `pkill -f PATTERN`", which under Claude Code kills the shell issuing it (see `pkill-f-self-match` below). Now points at capturing the PID. This is the second time a Fix string has recommended something another rule blocks; the first was `uuoc` suggesting `sd PAT REP FILE`, fixed in v0.1.5.
- **The manifest prints the name that runs.** `- fd (prefer over find) -> /usr/bin/fdfind` put the invocable name in the column that looks like provenance, so the line reads as "use `fd`" while only `fdfind` exists on PATH. It now renders `- fdfind (prefer over find; upstream name fd, which is NOT on PATH — type \`fdfind\`)`, and the rename is applied to the idiom and composition blocks underneath, which were teaching ten `fd …` pipelines that exit 127 on Debian and Ubuntu.

  The failure this came from is the quietest one on file: a session ran `fd -HI -e xlsx … 2>/dev/null | head`, got nothing, and twice reported that files did not exist in a directory they were at depth 1 of. A search that cannot run and a search that finds nothing produce the same empty stdout, and `2>/dev/null` — which every search idiom carries, because searches are noisy — removes the only difference.

### Added
- **`sd-in-place-write-empty`** (BLOCK): the base `sd PAT REP FILE` shape where the replacement is the empty string. `sd PAT '' FILE` does not substitute, it deletes — and persisting a deletion is what "strip this out so I can read the rest" becomes when the file operand is present. Same shape-reveals-intent argument as `sd-in-place-write-redaction`, one step stronger. Suppresses on `-p` / `--preview`; the stdin form is unaffected. 178 fires across the corpus (0.085%).

  Reported by aipotluckorg-231245 after `sd '^---[\s\S]*?---' '' "$f"` in a `for` loop over eleven tracked `.mdoc` files — intended to strip frontmatter before a word count, actually blanked all eleven. Every count came back `0`, which is the only reason anyone noticed. Recovered with `git checkout --`; nothing reached a commit.

  On the tier question v0.1.5's note raised: this one had a PreToolUse rule and it was the wrong tier. `sd-in-place-write` fired, correctly, with the right text. For a destructive write the advisory arrives in the same message as the tool result, which is to say after the loop has run to completion. The v0.1.3 SessionStart prose was loaded in that session too. Both were read; neither could act. Hence the block.
- **`sponge-eats-failed-pipeline`** (advise): any `| sponge`. 11 fires, 0.005%.

  `cmd | sponge FILE` truncates FILE to zero whenever cmd fails, because sponge faithfully writes whatever the pipeline produced and a failed command produces nothing. Exit 0, no warning, original gone. Measured rather than assumed: `printf 'alpha\nbeta\ngamma\n' > m.md` then `false | sponge m.md` takes a 17-byte file to 0 bytes and reports success.

  What makes it worse than the general case is *why* people reach for sponge. It exists because `cmd < FILE > FILE` truncates before cmd reads, so it gets used precisely when the target and the source are the same file — which is exactly when an empty write has nothing to recover from. `sd` and `awk` fail without emptying their target.

  No `Suppress`. Whether the left side can fail is not knowable from the command text, so this advises on every piped sponge and accepts that some were safe.
- **`grep-dash-pattern`** (advise): a grep whose QUOTED operand starts with `-`, with no `--` or `-e` earlier. Suppressed by either. 10 fires, 0.005%.

  `grep -vxF "- [Leave it]" FILE` parses the pattern as an option bundle and exits 2 with a usage error, matching nothing. Markdown list items, diff lines, arrows, and flag names all start with `-`. Gated on the operand being quoted, because an unquoted leading dash is indistinguishable from a flag and the quotes are what signal the author meant it as data. `cmdPos` keeps `git log --grep "-fix"` out, where a bare `\bgrep\b` would match inside the long flag.

  The reported case is loud-ish, exit 2 with a message, and it earns a rule anyway because the message goes to stderr while stdout stays empty — the shapes that swallow stderr are the same ones that make an empty result look like an answer. Sweeping the corpus then turned up something worse than the reported case. All ten fires are real, and one shape does not error at all:

  ```
  grep -E '->'        -> rc=2, ugrep: invalid option ->
  grep -E '--- FAIL'  -> rc=2, grep: unrecognized option
  grep -E '-v'        -> rc=1, NO MESSAGE
  ```

  A pattern that happens to BE a valid flag is consumed as one. `-v` becomes `--invert-match`, so grep searches for nothing, inverts, and exits 1 — which reads as "no matches" and is the quietest form of the bug. `-i`, `-c`, `-l`, `-o` and `-w` behave the same way. That case is now the headline of the fix text; it was not in the report and I would not have found it without the sweep.

  Both rules come from one incident, 2026-08-07, reported by documents-66700 and routed by the user: `grep -vxF "$LINE" "$M" | sponge "$M"`, run to filter a single line out of an auto-memory index, left the file at 0 bytes. The dash-pattern made the left side fail and the sponge made the failure destroy the input. Either alone is survivable; the pair is not. On this host `grep` is ugrep 7.5.0, whose "invalid option" text reads as a different kind of failure than GNU grep's, though both reject the pattern the same way.
- **`pgrep-f-self-match`** (advise): `pgrep -f`/`--full` co-occurring with `until`, `while`, or `kill`. 321 fires, 0.153%.

  `pgrep -f` matches full command lines, and the pattern text is sitting in the issuing shell's own `/proc/<pid>/cmdline` — so `until ! pgrep -f foo; do sleep 10; done` matches itself and never exits. Verified here directly: `bash -c 'pgrep -af MARKER'`, on a marker string no running process was named after, returned two rows — the inner shell and Claude Code's outer wrapper.

  Gated on the loop-or-kill pairing rather than on `-f` alone, because a one-shot `pgrep -f foo` used to look at what is running costs one extra line of output and nothing else. Reported 2026-08-05 by a session that found two of its predecessors' wait loops still running with `etime` in days, having outlived the job, the session that started them, and every session since.
- **`pkill-f-self-match`** (advise): `pkill -f`/`--full`, ungated. 894 fires, 0.425%.

  Ungated because there is no safe one-shot form. Verified the same way and less gently: `bash -c 'pkill -f MARKER; echo survived'` — on a marker matching no real process — produced no output and exit 144. The shell was killed by its own `pkill` before the `echo` ran. Every corpus fire is a server teardown of the shape `pkill -f "uvicorn backend.app.main"`, all of which have been killing their own shell.
- **`sd-replacement-shell-var`** (advise): a single-quoted sd replacement containing `$NAME` or `${NAME}`. 32 fires, 0.015%.

  sd's replacement string has its own `$` grammar — `$NAME` is a named capture-group reference, and an unmatched reference expands to EMPTY rather than erroring. `sd 'BIN=".*"' 'BIN="${TWIP_BIN:-$HERE/target/release/twip}"' run.sh` writes `BIN=""` and exits 0.

  The sed reflex is not merely absent here, it is inverted: single-quoting is exactly what makes `$FOO` literal in sed, because the quotes stop the shell and sed has no `$` grammar of its own. In sd the quotes stop the shell and then sd interpolates anyway. Someone who has never used sed reads the man page and gets this right.

  Orthogonal to the four in-place rules, which all discriminate on the file operand or on the 3-argument shape: this one fires on the pipe form too, because the replacement is destroyed there just as thoroughly. Suppressed when a named group (`(?P<name>` / `(?<name>`) appears — that is the feature working as designed. Numeric refs and the `$$` escape never trip it. Reported 2026-07-28 with a measured table against sd 1.0.0.
- **`rg-h-is-help`** (advise): an `h`-bearing rg flag bundle of two or more letters, or `-h` followed by a pattern or path. 54 fires, 0.026%.

  In grep, `-h` is `--no-filename` and `grep -oh PAT f1 f2` is the standard "just the matches" idiom. In rg, `-h` is `--help` and `--no-filename` is `-I` or `-N`. So `rg -oh 'https?://[^ ]+' a.md b.md` prints the usage text on stdout with exit 0 and never reads the pattern or the files. Piped through `sort -u | head`, which is what an extraction command does, the result is a tidy sorted list of flag descriptions and the author's email address, and it reads as "the files contained these strings." Bare `rg -h` and `rg --help` stay silent. Reported 2026-07-29.
- **`rg-ignore-file-hides-target`** (advise): rg or fd naming a dot-DIRECTORY in a path argument, with no `--no-ignore` / `-u` / `-I`. 128 fires, 0.061%.

  rg honours `.gitignore` whether or not a git operation is anywhere in view, so a deny-by-default ignore file makes an entire tree return zero matches with exit 1 — indistinguishable from "the string is not there." Nothing in the command mentions git and nothing in the output mentions the ignore file.

  `--hidden` does not fix it, which is what costs the time: rg descends into a hidden directory fine when you name it as an explicit path argument, so the dotted path misdirects toward the wrong flag. The decisive flag is `--no-ignore`.

  Gated on the path argument rather than on the absence of a flag, on the reporter's own measurement against 39,827 tool calls: "rg/fd without `--hidden`" fires on 11% of calls at 6.4% precision against a 5.1% base rate, which is chance dressed as a signal; adding "and the command names a dot-path" moves precision to 28.5% and cuts volume 34x. Reported 2026-08-06 by a session that searched `~/.claude/projects` for hook markers, got zero rows, and came one sentence short of reporting the hooks dead. With `-uu` the same search returns 20+ files; one marker occurs 159 times.

  Final count 128 fires, 0.061%. Narrowed twice on the corpus spans. A dot-path is only the trap when it is a DIRECTORY — ignore rules apply while descending, so `rg -n "env" frontend/.gitignore` and `rg -oN 'phc_…' frontend/.env.production.local` read their file and were false positives, two of the first sweep's six samples. The argument must now end in a component with no dot in it, which handles `.env.production.local` and `.github/workflows/ci.yml` by shape; the extensionless dot-files that no shape can tell from a directory (`.gitignore`, `.npmrc`, `.bashrc`, and paths under `.git/hooks/`) are a named suppression list. That took the rule from 662 fires to 128 — 81% of what it was catching was a file it had no business flagging.
- **`pipe-status-echo`** (advise): a pipeline ending in a filter, followed by `echo … $?`. 902 fires, 0.429%. Suppressed by `pipefail` or `PIPESTATUS`.

  The sharp half of the pipeline-status trap, and the one worth interrupting: the author is *deliberately* printing the status of the thing they care about, and getting the filter's. `gitleaks detect --redact 2>&1 | tail -25; echo "gitleaks_exit=$?"` reports tail's status. `go test ./... | rg -v '^ok '` then `echo $?` reports rg's — and rg exits 1 when it filtered everything away, so a fully-green suite reports failure.
- **`pipe-eats-exit-status`** (advise): a publish or install command piped into `tail`/`head`/`grep`/`rg`. Suppressed by `pipefail` or `PIPESTATUS`.

  A pipeline exits with its last stage's status, so `git push origin develop 2>&1 | tail -5` exits 0 on a rejected push. The error text is right there on stdout, so it does not feel hidden — it gets hidden one layer up, by anything reporting the status instead of showing the text: a background-task runner, a `set -e` script, a CI step, `&&` chaining. `2>&1` makes it worse rather than better, because the error becomes more lines for `tail -5` to discard.

  Narrowed before shipping, and this is the entry to read if you are adding a rule of your own. The first draft carried the build and test verbs too — `make`, `go build`, `go test`, `npm run`. It fired 15,571 times, 7.4% of every Bash call in the corpus, noisier than any rule weir has ever shipped, and 6 of 6 sampled fires were the same deliberate shape: `make check 2>&1 | tail -15`. That is not the trap. Someone trimming a check target is reading the tail; the eaten status costs them nothing because the output is in front of them. The trap needs a reader that consumes the status instead of the text AND a failure that leaves no other trace, which is why the shipped list is publish and install verbs only. A failed `go install` leaves the previously installed binary in place: no missing file, no broken build, and the next command runs happily against the old version.

  Cut to those verbs it fires 4,167 times, 1.98% — still the second-noisiest rule here, and shipped anyway because the matched spans are unambiguous: `git push 2>&1 | tail`, `git push --force origin v0.7.0 2>&1 | tail`, `pip install -e . -q 2>&1 | tail`, `git pull --rebase 2>&1 | tail`. The volume is not a regex artifact. The corpus contains 2,458 `git push` commands and 1,389 of them are in a statement that also trims output, so the habit really is that common — which is the argument for the rule rather than against it. `grep-head-trim` has shipped at 4.75% since v0.1.0 on the same reasoning.

### Changed
- **The rg gitignore gotcha leads with the ignore half.** It previously opened on hidden files and offered `.env` as the canonical case, so it read as a rule about dotfiles — and the natural narrower fix for a dotfile problem is `--hidden`, which does not help. The decisive flag is `--no-ignore`. The entry now says so, and names `~/.claude` rather than `.env`, for a reason that turned out to be measurable rather than stylistic: on a scratch tree with a deny-by-default `*` gitignore, `rg NEEDLE .env` returns the match and `rg NEEDLE sub` returns nothing. Ignore rules apply while DESCENDING; an explicitly-named file argument is read regardless. So a named `.env` was never the trap, and a tree is.
- Six new SessionStart gotcha entries: rg's `-h`, sd's replacement `$` grammar, sponge's empty commit, the dash-pattern parse, the pipeline exit-status rule, and the pgrep/pkill self-match. Three of those are shell-level rather than tool-specific, so `gotcha.Tool` now accepts an empty string meaning "always surface."
- A fifth entry has no rule and could not have one: command-not-found is the quietest failure a search has, because a search that cannot run and a search that finds nothing produce the same empty stdout, and `2>/dev/null` removes the difference. The entry says not to convert an empty search result into "the file does not exist" without checking the binary first.
- The gotcha table gained a second bar for admission, recorded in the source: an entry describing a *destructive* habit must also have a PreToolUse rule. Prose delivered once at SessionStart cannot interrupt at the moment of danger.

### Internal
- Test harness gained `ruleNegatives` — fixtures asserting that a given command must NOT fire a given rule, while remaining free to fire others. The old table could only say "nothing at all fires", which cannot express the case precision work turns on: `sd '(x)' 'got:$1' file.txt` is a true positive for the in-place rule and a false positive for the capture-group rule, and both facts need stating.
- `TestEveryRuleHasAPositive` fails the build on a rule with no fixture.

## v0.1.5 — 2026-07-25

### Added
- Three new suggest rules for the `sd` in-place trap — the second silent-destructive incident reported this week (rg-r-misfire being the first). Reported by cope-1184525 after `sd '=.*' '=<set>' .env` overwrote a live `ANTHROPIC_API_KEY` with the literal string `<set>` (file went 232 → 129 bytes, recovered only because they kept a separate key .txt out-of-band).
  - **`sd-in-place-write`** (advise): fires on any `sd PAT REP FILE` shape with no `-p`/`--preview`. Legitimate persistence is a real use case, so advisory rather than block.
  - **`sd-in-place-write-secret-file`** (BLOCK): base shape + file operand matches `.env` / `.pem` / `credential*` / `.key` / `.p12` / `.pfx`. Overwriting these is close to unrecoverable in practice.
  - **`sd-in-place-write-redaction`** (BLOCK): base shape + replacement contains `<set>` / `<redacted>` / `***` / `REDACTED`. A user typing a redaction almost never wants it persisted — the replacement's shape reveals the "for display" intent.
- All three suppress on `-p` / `--preview`. Stdin form (`cat FILE | sd PAT REP`) has only two positional args and naturally doesn't match.

### Fixed
- Removed `sd` from the `uuoc` rule's target alternation. The uuoc rewrite for `cat FILE | sd PAT REP` (safe stdin form) → `sd PAT REP FILE` is the exact destructive in-place shape the new sd rules block. Two rules would fight. sd is the only tool on the uuoc list whose "TOOL FILE" form differs destructively from the "cat FILE | TOOL" form; sed's `-i`-less default is stdout, so sed stays.

### Notes
- Cope's diagnosis on why the v0.1.3 sd gotcha didn't stop this: prose in the SessionStart inject block is documentation, not a guard. On the same session, `ls-pipe-wc-l` and `grep-head-trim` blocked style nits while the destructive command sailed through. A gotcha that only lives in the session-start digest can't interrupt at the moment of danger — that's the PreToolUse rule's job. Bar for adding a gotcha entry now includes: does this also need a PreToolUse rule?

## v0.1.4 — 2026-07-23

### Fixed
- Block-mode rules now suppress matches that land inside a heredoc body, in addition to the existing single/double-quoted suppression. Motivating case (reported by aipotluckorg-3288452): `which-vs-command-v` fired on a `git commit -F - <<'EOF' ... EOF` whose commit-message prose contained the English word "which" three times, refusing a productive commit. The v0.1.1 walker only tracked `'` and `"`, so heredoc bodies were a blind spot. Walker now recognizes `<<[-]?['"]?DELIM['"]?` openers and terminator lines (dedent-aware for `<<-`); `<<<` (herestring) correctly does NOT open a body. Documented gaps unchanged: `$'...'`, backticks, nested heredocs, and heredoc openers inside quoted strings.

### Changed
- `rg-r-misfire` split into two rules:
  - **`rg-r-misfire-bundled`** (BLOCK, new): the bundled form `-r[nliwcv]` has virtually no legitimate use — a real single-letter replacement is written `-r n` (separated) or `--replace=n`. Bundled `-r[letter]` is nearly always the `grep -rn` muscle-memory trap that silently rewrites stdout. Blocking outright.
  - **`rg-r-misfire`** (advise, kept): separated `-r X` where X is a single letter, `''`, or `""`. Legitimate but rare, so advisory rather than block. Now also catches `-r ''` and `-r ""` (empty replacement — silently strips matches from output), reported by aipotluckorg-3288452 as a same-class variant.

### Added
- New gotchas section entries, contributed by mars-1868431 via dispatch:
  - **rg**: respects `.gitignore` and hides hidden files by default — silently under-matches vs `grep -r` when the target lives in an ignored path. `.env` is the classic tripwire. Use `rg -uu` (or `--no-ignore --hidden`).
  - **fd**: same class — hides gitignored and hidden files by default (unlike `find`). Use `fd -HI`.

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
