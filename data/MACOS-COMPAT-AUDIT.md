# macOS compatibility audit

Started 2026-09-08, no Mac available. Findings below come from two sources,
kept distinct:

- **Verified against a real man page** — fetched from
  `man.freebsd.org` (`manpath=macOS`), the closest primary source reachable
  without Darwin hardware. Apple's BSD userland (`grep`, `sed`, `find`,
  `ps`, `awk`) is historically forked from FreeBSD/NetBSD, so this is a
  reasonable proxy, not a guarantee of an exact match — the
  `macos-classic-tool-survey` CI job (`.github/workflows/ci.yml`) exists to
  replace this proxy with the real thing once it's run on an actual
  `macos-latest` runner.
- **Not yet checked** — named explicitly rather than silently skipped.

## Tool availability: settled

All 17 modern tools weir tracks have a Homebrew formula — checked against
the live `formulae.brew.sh` API, not assumed. `fd` and `bat` install under
their plain names on Homebrew (no caveat renaming them), unlike Debian's
`fdfind`/`batcat` — that rename exists only because Debian's own archive
had unrelated packages already using those names. `weir`'s `internal/probe`
binary-name table needs a macOS branch for these two; every other tracked
tool (`rg`, `sd`, `procs`, `delta`, `duf`, `hexyl`, `mlr`, `jq`, `fzf`,
`entr`, `hyperfine`, `parallel`, `pv`, `sponge`) already matches its Linux
name.

`weir` itself already cross-compiles for `darwin/amd64` and `darwin/arm64`
(`.goreleaser.yaml`) — that part was never broken, just never run.

## Rules verified as still correct on BSD tools

None of `rules.go`'s regexes actually parse GNU-specific flag syntax for
the *classic* tool in most cases — the rg/sd rules match `rg`/`sd`
themselves (identical cross-platform Rust binaries, no OS divergence
possible), and most of the rest (`grep-head-trim`, `ls-grep`, `uuoc`, etc.)
detect a pipe *shape*, not a flag. The real risk in that second group is
whether the **suggested rewrite** uses a flag that doesn't exist on BSD.
Checked the two that mattered most:

- **`grep -m` (max-count)**, the rewrite target of `grep-head-trim`.
  Confirmed present in BSD grep's own man page, same meaning
  (`--max-count=num`, "stop reading the file after num matches").
- **`grep -r`/`-R` (recursive)** and the general grep flag table that
  `rg-r-misfire`/`rg-h-is-help`/`rg-cap-l-misfire` depend on for the
  muscle-memory trap to exist in the first place. Confirmed BSD grep's
  `-r`/`-R`/`--recursive` means the same thing GNU grep's does — so the
  "grep muscle memory collides with rg's differently-typed flag" trap these
  three rules catch is the same trap on macOS, unchanged. Nothing to update.
- **`find -exec ... {} +`** (batched form), the rewrite target of
  `find-exec-semi`. Confirmed present in BSD find's own man page, verbatim
  same syntax and semantics as GNU find's.

## Found: a real BSD-specific trap, not yet a rule

**`sed -i` requires an argument on BSD/macOS, glued to the flag — a
different shape of the same class of danger `sd-in-place-write` already
covers, for the other tool.** Confirmed from the actual man page synopsis:
`-i extension`. GNU sed's `-i` takes an *optional* argument; BSD sed's
takes a *mandatory* one, and — this is the trap — it's read as the very
next character(s) glued to `-i`, not a separate flag. `sed -i '' 's/x/y/'
file` (empty-string extension, explicit) is the correct BSD form. `sed -i
's/x/y/' file` — the GNU-muscle-memory form, no separate extension —
gets `'s/x/y/'` consumed AS the extension argument, and `file` treated as
the sed *script*, with no file operand left at all. Reads clean, no error
in the common case, and the source file is never touched.

**This is not yet a `rules.go` entry, and shouldn't become one without a
decision first:** every existing rule fires unconditionally, regardless of
host OS — there is no `runtime.GOOS` check anywhere in `internal/suggest`
or `internal/probe` (checked directly, zero hits). A rule for this would
misfire on every Linux box (this one included), where `sed -i 's/x/y/'
file` is completely correct GNU usage. Writing it needs either an
OS-conditional rule (new engine capability, not a drop-in addition the way
`rg-cap-l-misfire` was) or Fix text that explicitly names itself as
macOS-only advice fired unconditionally — a real design choice, flagged
here rather than picked silently.

## Not yet checked

`ps` and `awk` — lower priority since no existing `rules.go` entry parses
either tool's flags directly (`ps-grep-vs-pgrep` and `awk-awk` are both
pipe-shape rules, tool-agnostic). BSD `ps`'s option syntax is known to
differ meaningfully from Linux's (traditionally no leading dash), but
nothing in the current rule set depends on that. Will get real coverage
once `macos-classic-tool-survey` runs against an actual `macos-latest`
runner — that requires a push, not done as part of this pass.
