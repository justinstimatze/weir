package idioms

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Tools weir tracks. Includes Debian-aliased filenames (fdfind, batcat) so
// we can map them back to canonical names in the output.
var trackedTools = []string{
	"rg", "fd", "bat", "sd", "mlr", "eza", "exa", "dust", "duf", "procs",
	"bottom", "btm", "delta", "choose", "hexyl",
	"jq", "yq", "dasel", "gron",
	"sponge", "pv", "parallel", "hyperfine",
	"up", "teip", "xsv", "qsv",
	"fzf", "watchexec", "entr",
	"fdfind", "batcat",
}

var alias = map[string]string{
	"fdfind": "fd",
	"batcat": "bat",
}

const perToolCap = 3

var (
	// {{[-F|--fixed-strings]}}  ->  -F  (first option)
	reFlagAlt = regexp.MustCompile(`\{\{\[(-[^|\]]+)\|[^\]]*\]\}\}`)
	// {{anything}} -- non-greedy and DOTALL-aware so inner `}` (e.g. inside
	// `{{echo '{"k":"v"}'}}`) doesn't break the match.
	rePlaceholder = regexp.MustCompile(`(?s)\{\{(.+?)\}\}`)
	// Placeholder content that's actually a shell command (echo, cat, etc.)
	// shouldn't be wrapped in <>; that's tldr's convention for "type this
	// literally to produce example input", not "fill this in with a value".
	reShellVerb = regexp.MustCompile(`^(echo|cat|printf|awk|sed|grep|sort|ls|find|head|tail|curl|wget|ssh)\s`)
)

func clean(cmd string) string {
	cmd = reFlagAlt.ReplaceAllString(cmd, "$1")
	cmd = rePlaceholder.ReplaceAllStringFunc(cmd, func(m string) string {
		inner := m[2 : len(m)-2]
		if reShellVerb.MatchString(inner) {
			return inner // literal command — drop the {{ }} wrapping entirely
		}
		return "<" + inner + ">"
	})
	cmd = strings.Trim(cmd, "` ")
	cmd = strings.TrimRight(cmd, " ")
	return cmd
}

// parsePage extracts (intent, cmd) pairs from a tldr markdown page.
func parsePage(text string) []Idiom {
	var pairs []Idiom
	var pending string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r ")
		switch {
		case strings.HasPrefix(line, "- "):
			intent := strings.TrimSpace(strings.TrimSuffix(line[2:], ":"))
			pending = intent
		case len(line) >= 2 && strings.HasPrefix(line, "`") && strings.HasSuffix(line, "`") && pending != "":
			cmd := clean(line)
			if cmd != "" {
				pairs = append(pairs, Idiom{Intent: pending, Cmd: cmd})
			}
			pending = ""
		}
	}
	return pairs
}

// CmdBuild is the entry point for `weir build-idioms`.
//
// Usage: weir build-idioms [--root PATH] [--output PATH]
//
//	--root    tldr-pages clone root (default /tmp/tldr/pages)
//	--output  path to write idioms.json (default internal/idioms/idioms.json
//	          relative to current dir — i.e. run from the repo root)
func CmdBuild(args []string, _ io.Reader, stdout io.Writer) int {
	fs := flag.NewFlagSet("build-idioms", flag.ContinueOnError)
	root := fs.String("root", "/tmp/tldr/pages", "tldr-pages clone root")
	output := fs.String("output", "internal/idioms/idioms.json", "output JSON path")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	type record struct {
		Meta struct {
			SourceRoot   string `json:"source_root"`
			SourcesUsed  int    `json:"sources_used"`
			PerToolCap   int    `json:"per_tool_cap"`
			ToolsCovered int    `json:"tools_covered"`
		} `json:"meta"`
		Idioms map[string][]Idiom `json:"idioms"`
	}

	out := record{Idioms: map[string][]Idiom{}}
	out.Meta.SourceRoot = *root
	out.Meta.PerToolCap = perToolCap

	dirs := []string{"common", "linux"}
	var sources []string
	for _, t := range trackedTools {
		canonical := t
		if a, ok := alias[t]; ok {
			canonical = a
		}
		if _, dup := out.Idioms[canonical]; dup {
			continue
		}
		var found string
		for _, d := range dirs {
			candidate := filepath.Join(*root, d, t+".md")
			if _, err := os.Stat(candidate); err == nil {
				found = candidate
				break
			}
		}
		if found == "" {
			continue
		}
		sources = append(sources, found)
		data, err := os.ReadFile(found)
		if err != nil {
			continue
		}
		pairs := parsePage(string(data))
		if n := len(pairs); n > 0 {
			if n > perToolCap {
				pairs = pairs[:perToolCap]
			}
			out.Idioms[canonical] = pairs
		}
	}
	out.Meta.SourcesUsed = len(sources)
	out.Meta.ToolsCovered = len(out.Idioms)

	// Sort keys for deterministic output
	sort.Strings(sources)

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "weir build-idioms: marshal: %v\n", err)
		return 1
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "weir build-idioms: mkdir: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*output, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "weir build-idioms: write: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "weir build-idioms: wrote %s (%d tools from %d tldr pages)\n",
		*output, len(out.Idioms), len(sources))
	fmt.Fprintln(stdout, "weir build-idioms: rebuild the weir binary (go install .) to pick up the new corpus via go:embed")
	return 0
}
