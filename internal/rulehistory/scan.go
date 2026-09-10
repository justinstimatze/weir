package rulehistory

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"

	"github.com/justinstimatze/weir/internal/suggest"
)

// assistantEnvelope and toolUse mirror internal/measure/measure.go's own
// JSONL envelope types. Duplicated rather than imported — measure's types
// are unexported and measure.streamFile yields commands, not counts — but
// this shape must be kept in sync with measure.go's if Claude Code's
// transcript format ever changes.
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

// scanFile reads path from byte 0 and counts every Bash command plus which
// suggest rules it fires. Always a full scan of the whole file — see the
// package doc comment on Load for why this deliberately isn't a
// byte-offset incremental resume.
func scanFile(path string) (fileStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileStats{}, err
	}
	defer f.Close()

	stats := fileStats{Fired: map[string]int{}}
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
			stats.TotalBashCalls++
			for _, r := range suggest.Match(tu.Input.Command) {
				stats.Fired[r.Name]++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fileStats{}, err
	}
	if len(stats.Fired) == 0 {
		stats.Fired = nil
	}
	return stats, nil
}
