// Package measure implements `weir measure`: stream the current
// transcript corpus, count what the rule set fires on, and diff against
// a baseline snapshot. The 2026-05-20 baseline ships embedded.
//
// Use cases:
//   - "Did installing weir actually change my bash habits?" (re-run weekly,
//     watch rule-hit counts shrink while modern-tool counts grow)
//   - "Did a new rule fire as much as I expected after promotion?"
//   - "Is grep|head trending toward zero?"
package measure

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/justinstimatze/weir/internal/suggest"
)

//go:embed baseline.json
var rawBaseline []byte

// Baseline is the subset of mine_pipes.py's report we compare against.
type Baseline struct {
	Meta struct {
		ElapsedSeconds float64 `json:"elapsed_seconds"`
	} `json:"meta"`
	Totals struct {
		BashCalls  int     `json:"bash_calls"`
		PipedCalls int     `json:"piped_calls"`
		PipedPct   float64 `json:"piped_pct"`
	} `json:"totals"`
	TopBinaries     [][]any          `json:"top_binaries"` // [["grep",4575], ...]
	ModernVsClassic map[string][]int `json:"modern_vs_classic"`
	Antipatterns    map[string]int   `json:"antipatterns"`
}

// Current is the equivalent counters computed by streaming the corpus
// right now and running suggest.Match() on each Bash command.
type Current struct {
	BashCalls  int            `json:"bash_calls"`
	PipedCalls int            `json:"piped_calls"`
	PipedPct   float64        `json:"piped_pct"`
	Binaries   map[string]int `json:"binaries"`  // first-token of each command
	RuleHits   map[string]int `json:"rule_hits"` // suggest rule name -> count
	StreamedAt string         `json:"streamed_at,omitempty"`
}

// Diff is what the human report renders from.
type Diff struct {
	Baseline Baseline `json:"baseline"`
	Current  Current  `json:"current"`
	// Modern-vs-classic rendered: tool -> {classic_now, classic_baseline, modern_now}
	ModernNow map[string]int `json:"modern_now"`
}

// --- JSONL streaming ----------------------------------------------------

type assistantEnvelope struct {
	Type    string `json:"type"`
	Message struct {
		Content []json.RawMessage `json:"content"`
	} `json:"message"`
}

type toolUse struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Input struct {
		Command string `json:"command"`
	} `json:"input"`
}

// streamBash iterates every Bash command across all of the user's session
// transcripts in ~/.claude/projects/. Yields one command at a time.
func streamBash(yield func(string)) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	patterns := []string{
		filepath.Join(home, ".claude", "projects", "*", "*.jsonl"),
		filepath.Join(home, ".claude", "projects", "*", "subagents", "*.jsonl"),
	}
	seen := map[string]bool{}
	var paths []string
	for _, pat := range patterns {
		m, _ := filepath.Glob(pat)
		for _, p := range m {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	sort.Strings(paths)

	for _, p := range paths {
		if err := streamFile(p, yield); err != nil {
			// continue on per-file errors — partial measurement is better than none
			fmt.Fprintf(os.Stderr, "weir measure: %s: %v\n", p, err)
		}
	}
	return nil
}

func streamFile(path string, yield func(string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	// transcripts can have very long lines; default 64KB isn't enough
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var env assistantEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		if env.Type != "assistant" {
			continue
		}
		for _, raw := range env.Message.Content {
			var tu toolUse
			if err := json.Unmarshal(raw, &tu); err != nil {
				continue
			}
			if tu.Type != "tool_use" || tu.Name != "Bash" {
				continue
			}
			if strings.TrimSpace(tu.Input.Command) == "" {
				continue
			}
			yield(tu.Input.Command)
		}
	}
	return scanner.Err()
}

// --- binary extraction --------------------------------------------------

var reBinaryToken = regexp.MustCompile(`[A-Za-z0-9_./+\-]+`)

var binaryWrappers = map[string]bool{
	"sudo": true, "time": true, "env": true, "command": true,
	"exec": true, "nohup": true, "stdbuf": true, "ionice": true,
	"nice": true, "timeout": true, "xargs": true,
}

// firstBinary returns the first "real" binary name from a single stage
// of a pipeline. Mirrors mine_pipes.py's first_binary heuristic so binary
// counts diff cleanly against the baseline.
func firstBinary(stage string) string {
	s := strings.TrimSpace(stage)
	for len(s) > 0 && (s[0] == '(' || s[0] == '{' || s[0] == '$' || s[0] == '!') {
		s = strings.TrimLeft(s[1:], " \t")
	}
	if s == "" {
		return ""
	}
	m := reBinaryToken.FindString(s)
	if m == "" {
		return ""
	}
	// VAR=value cmd
	if strings.Contains(m, "=") && !strings.HasPrefix(m, "=") {
		rest := strings.TrimLeft(s[len(m):], " \t")
		next := reBinaryToken.FindString(rest)
		if next != "" {
			m = next
		}
	}
	if binaryWrappers[m] {
		rest := strings.TrimLeft(s[len(m):], " \t")
		// skip flags
		for strings.HasPrefix(rest, "-") {
			sp := strings.IndexAny(rest, " \t")
			if sp == -1 {
				break
			}
			rest = strings.TrimLeft(rest[sp+1:], " \t")
		}
		next := reBinaryToken.FindString(rest)
		if next != "" {
			m = next
		}
	}
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

// pipeSplit splits a command on top-level pipes, ignoring pipes inside
// quoted strings. Equivalent to mine_pipes.py's naive_pipe_split.
func pipeSplit(cmd string) []string {
	var parts []string
	var buf strings.Builder
	inS, inD := false, false
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		if ch == '\\' && i+1 < len(cmd) {
			buf.WriteByte(ch)
			buf.WriteByte(cmd[i+1])
			i++
			continue
		}
		switch {
		case ch == '\'' && !inD:
			inS = !inS
			buf.WriteByte(ch)
		case ch == '"' && !inS:
			inD = !inD
			buf.WriteByte(ch)
		case ch == '|' && !inS && !inD:
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				buf.WriteString("||")
				i++
				continue
			}
			parts = append(parts, buf.String())
			buf.Reset()
		default:
			buf.WriteByte(ch)
		}
	}
	parts = append(parts, buf.String())
	return parts
}

// Run streams the corpus and computes Current.
func Run() Current {
	c := Current{
		Binaries: map[string]int{},
		RuleHits: map[string]int{},
	}
	// errors from streamBash are best-effort; partial measurement is better
	// than none, and per-file errors already go to stderr inside.
	_ = streamBash(func(cmd string) {
		c.BashCalls++
		stages := pipeSplit(cmd)
		if len(stages) > 1 {
			c.PipedCalls++
		}
		for _, st := range stages {
			b := firstBinary(st)
			if b != "" {
				c.Binaries[b]++
			}
		}
		for _, r := range suggest.Match(cmd) {
			c.RuleHits[r.Name]++
		}
	})
	if c.BashCalls > 0 {
		c.PipedPct = 100.0 * float64(c.PipedCalls) / float64(c.BashCalls)
	}
	return c
}

// --- baseline + diff ----------------------------------------------------

func loadBaseline(path string) (*Baseline, error) {
	var data []byte
	var err error
	if path == "" || path == "embedded" {
		data = rawBaseline
	} else {
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// --- rendering ----------------------------------------------------------

func renderHuman(w io.Writer, base *Baseline, curr Current) {
	bashDelta := curr.BashCalls - base.Totals.BashCalls
	pipedDelta := curr.PipedPct - base.Totals.PipedPct
	fmt.Fprintln(w, "weir measure — current corpus vs embedded 2026-05-20 baseline")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  bash calls:    %d  (baseline: %d;  Δ %+d)\n", curr.BashCalls, base.Totals.BashCalls, bashDelta)
	fmt.Fprintf(w, "  piped pct:     %.1f%%  (baseline: %.1f%%;  Δ %+.1fpp)\n", curr.PipedPct, base.Totals.PipedPct, pipedDelta)
	fmt.Fprintln(w)

	// modern vs classic
	fmt.Fprintln(w, "modern-vs-classic (modern count delta is the load-bearing signal):")
	keys := make([]string, 0, len(base.ModernVsClassic))
	for k := range base.ModernVsClassic {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		bp := base.ModernVsClassic[k]
		if len(bp) != 2 {
			continue
		}
		// k is like "rg_vs_grep" -> modern=rg, classic=grep
		parts := strings.Split(k, "_vs_")
		if len(parts) != 2 {
			continue
		}
		modName, classicName := parts[0], parts[1]
		modNow := curr.Binaries[modName]
		// handle aliases: fd_vs_find counts fd+fdfind, eza_exa_vs_ls counts eza+exa
		switch modName {
		case "fd":
			modNow += curr.Binaries["fdfind"]
		case "eza_exa":
			modNow = curr.Binaries["eza"] + curr.Binaries["exa"]
		}
		classicNow := curr.Binaries[classicName]
		fmt.Fprintf(w, "  %-18s  modern %d -> %d (Δ %+d) | classic %d -> %d (Δ %+d)\n",
			k, bp[0], modNow, modNow-bp[0], bp[1], classicNow, classicNow-bp[1])
	}
	fmt.Fprintln(w)

	// antipatterns: baseline uses mine_pipes.py names; current uses suggest rule names.
	// They're related but not identical. Show both.
	fmt.Fprintln(w, "antipatterns (lower is better):")
	fmt.Fprintln(w, "  baseline categories (from mine_pipes.py @ 2026-05-20):")
	apKeys := make([]string, 0, len(base.Antipatterns))
	for k := range base.Antipatterns {
		apKeys = append(apKeys, k)
	}
	sort.Slice(apKeys, func(i, j int) bool { return base.Antipatterns[apKeys[i]] > base.Antipatterns[apKeys[j]] })
	for _, k := range apKeys {
		fmt.Fprintf(w, "    %-30s baseline=%d\n", k, base.Antipatterns[k])
	}

	fmt.Fprintln(w, "  current suggest.go rule hits (live rule set):")
	rhKeys := make([]string, 0, len(curr.RuleHits))
	for k := range curr.RuleHits {
		rhKeys = append(rhKeys, k)
	}
	sort.Slice(rhKeys, func(i, j int) bool { return curr.RuleHits[rhKeys[i]] > curr.RuleHits[rhKeys[j]] })
	for _, k := range rhKeys {
		fmt.Fprintf(w, "    %-30s current=%d\n", k, curr.RuleHits[k])
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Note: the categories aren't 1:1 (baseline's mine_pipes.py has slightly different regexes than suggest.go's rules). Watch direction of change over time, not absolute equality.")
}

// --- subcommand entry ---------------------------------------------------

// CmdMeasure is the entry point for `weir measure`.
func CmdMeasure(args []string, _ io.Reader, stdout io.Writer) int {
	fs := flag.NewFlagSet("measure", flag.ContinueOnError)
	baselinePath := fs.String("baseline", "", "baseline JSON path (default: embedded 2026-05-20)")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON instead of human text")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base, err := loadBaseline(*baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "weir measure: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "weir measure: streaming transcripts...")
	curr := Run()
	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(Diff{Baseline: *base, Current: curr})
	} else {
		renderHuman(stdout, base, curr)
	}
	return 0
}
