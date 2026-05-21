// weir — one binary for the weir Claude Code addon.
// Subcommands:
//
//	weir suggest        - PreToolUse hook: lint Bash command, emit suggestion JSON
//	weir probe          - emit capability manifest JSON for installed modern shell tools
//	weir inject         - SessionStart hook: render manifest + idioms as additionalContext
//	weir build-idioms   - parse a tldr-pages tree into internal/idioms/idioms.json
//	weir install        - register weir's hooks in Claude Code's settings.json
//	weir uninstall      - remove weir's hooks
//	weir status         - report which hooks are registered + show apt suggestion
package main

import (
	"fmt"
	"os"

	"github.com/justinstimatze/weir/internal/idioms"
	"github.com/justinstimatze/weir/internal/inject"
	"github.com/justinstimatze/weir/internal/install"
	"github.com/justinstimatze/weir/internal/measure"
	"github.com/justinstimatze/weir/internal/probe"
	"github.com/justinstimatze/weir/internal/suggest"
)

// version is overridden at release-build time via goreleaser's
//
//	ldflags: -X main.version={{.Version}}
//
// Local `go build` leaves it as "dev".
var version = "dev"

const usageText = `usage: weir <subcommand> [args]

subcommands:
  suggest         PreToolUse hook (reads JSON on stdin, emits suggestion JSON)
  probe           emit capability manifest JSON
  inject          SessionStart hook (renders manifest + idioms)
  build-idioms    rebuild internal/idioms/idioms.json from a tldr-pages clone
  install         register weir's hooks in Claude Code settings.json
  uninstall       remove weir's hooks
  status          report registered hooks + apt suggestion
  measure         stream current transcript corpus and diff against the embedded baseline
  review CMD      interactive spot-check — show what weir would do with a given command
  version         print weir's version

env:
  CLAUDE_SETTINGS    path override (default: ~/.claude/settings.json)
  WEIR_SKIP          set non-empty to suppress inject output
  WEIR_SUGGEST_SKIP  set non-empty to suppress suggest output
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	sub, args := os.Args[1], os.Args[2:]

	// install / uninstall / status need to know weir's own path so they
	// can register `<this binary> inject` / `<this binary> suggest` as
	// the hook command, and so uninstall can identify weir-owned hooks
	// by command prefix.
	weirBin, err := os.Executable()
	if err != nil {
		// fall back to argv[0] if Executable fails (very unusual)
		weirBin = os.Args[0]
	}

	switch sub {
	case "suggest":
		os.Exit(suggest.Run(args, os.Stdin, os.Stdout))
	case "probe":
		os.Exit(probe.CmdProbe(args, os.Stdin, os.Stdout))
	case "inject":
		os.Exit(inject.CmdInject(args, os.Stdin, os.Stdout))
	case "build-idioms":
		os.Exit(idioms.CmdBuild(args, os.Stdin, os.Stdout))
	case "install":
		os.Exit(install.Install(install.Options{WeirBin: weirBin, Stdout: os.Stdout, Stderr: os.Stderr}))
	case "uninstall":
		os.Exit(install.Uninstall(install.Options{WeirBin: weirBin, Stdout: os.Stdout, Stderr: os.Stderr}))
	case "status":
		os.Exit(install.Status(install.Options{WeirBin: weirBin, Stdout: os.Stdout, Stderr: os.Stderr}))
	case "measure":
		os.Exit(measure.CmdMeasure(args, os.Stdin, os.Stdout))
	case "review":
		os.Exit(suggest.CmdReview(args, os.Stdin, os.Stdout))
	case "-h", "--help", "help":
		fmt.Print(usageText)
		os.Exit(0)
	case "-v", "--version", "version":
		fmt.Println(version)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "weir: unknown subcommand %q\n%s", sub, usageText)
		os.Exit(2)
	}
}
