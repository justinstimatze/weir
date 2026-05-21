package suggest

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// CmdReview is the entry point for `weir review`. Takes a bash command
// from argv (joined) or stdin, runs it through the same Match() the
// PreToolUse hook uses, prints a human-readable verdict. No JSON wrapping,
// no fail-open silencing — this is for interactive spot-checking and
// rule-tuning during dogfooding.
//
// Usage:
//
//	weir review "ps aux | grep nginx"
//	echo "find . -name '*.go' | xargs grep TODO" | weir review
//	weir review --quiet "cmd"    # just print rule names, no verdict prose
func CmdReview(args []string, stdin io.Reader, stdout io.Writer) int {
	quiet := false
	var positional []string
	for _, a := range args {
		if a == "-q" || a == "--quiet" {
			quiet = true
			continue
		}
		positional = append(positional, a)
	}

	var cmd string
	if len(positional) > 0 {
		cmd = strings.Join(positional, " ")
	} else {
		// read from stdin
		b, err := io.ReadAll(bufio.NewReader(stdin))
		if err != nil || len(b) == 0 {
			fmt.Fprintln(os.Stderr, "weir review: no command given (argv or stdin)")
			fmt.Fprintln(os.Stderr, "usage: weir review \"<command>\"  OR  echo <cmd> | weir review")
			return 2
		}
		cmd = strings.TrimSpace(string(b))
	}

	if cmd == "" {
		fmt.Fprintln(os.Stderr, "weir review: empty command")
		return 2
	}

	hits := Match(cmd)
	if quiet {
		for _, r := range hits {
			fmt.Fprintln(stdout, r.Name)
		}
		if len(hits) == 0 {
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "command:  %s\n\n", cmd)
	if len(hits) == 0 {
		fmt.Fprintln(stdout, "verdict:  clean — no rules fire")
		return 1
	}

	var blocks, advises []Rule
	for _, r := range hits {
		if r.Action == "block" {
			blocks = append(blocks, r)
		} else {
			advises = append(advises, r)
		}
	}
	if len(blocks) > 0 {
		fmt.Fprintln(stdout, "verdict:  BLOCKED in production — PreToolUse would refuse this command")
	} else {
		fmt.Fprintln(stdout, "verdict:  advisory — command would still run; suggestion shown to model")
	}
	for _, r := range blocks {
		fmt.Fprintf(stdout, "  [block]    %s\n             %s\n", r.Name, r.Fix)
	}
	for _, r := range advises {
		fmt.Fprintf(stdout, "  [advise]   %s\n             %s\n", r.Name, r.Fix)
	}
	return 0
}
