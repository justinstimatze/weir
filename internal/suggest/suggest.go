// Package suggest is weir's layer-2 antipattern suggester. Run from a
// Claude Code PreToolUse hook with `weir suggest`; reads hook JSON on
// stdin, emits a hookSpecificOutput.additionalContext block if any rule
// matches the Bash command. Suggest-only (no blocking). Fail-open: any
// error -> silent exit 0; never break a Bash call.
//
// v0 limitations (documented, not bugs):
//   - Regex over the raw command string; shell quoting is NOT parsed for
//     advisory rules. A literal pipe inside a quoted pattern (e.g.
//     `grep "a|b" file`) can trigger any advisory rule that looks for `|`.
//     False-positive class accepted for v0 advisory rules (cheap: extra context).
//   - BLOCK rules suppress matches that land inside a single- or double-quoted
//     string (see quotes.go). This is a hard-stop rule class, so refusing a
//     productive `git commit -m "...which..."` is worth a small fix beyond the
//     pure-regex approach. Approximation: does not model $'...', heredoc bodies
//     as separate scopes, or backticks.
//   - Modern-tool nudges (rg over grep, fd over find) deliberately NOT emitted
//     here — those belong to the SessionStart manifest (`weir inject`).
package suggest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/justinstimatze/weir/internal/guard"
)

type hookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// adviseOutput is the JSON shape for advisory (non-blocking) suggestions:
// the model sees the additionalContext, the command still runs.
type adviseOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// blockOutput is the JSON shape for blocking suggestions: Claude Code
// refuses to run the tool and surfaces the reason to both model and user.
// Used only for rules whose rewrites are mechanically safe (uuoc, which-v-command-v).
type blockOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// Run is the entry point for `weir suggest`. Always returns 0 (fail-open).
// If the WEIR_SUGGEST_SKIP env var is set, exits silently without examining input.
// Any panic inside the body is recovered by guard.Hook so the PreToolUse hook
// never blocks Claude Code via crash.
func Run(args []string, stdin io.Reader, stdout io.Writer) int {
	return guard.Hook("suggest", func() int { return runInner(args, stdin, stdout) })
}

func runInner(_ []string, stdin io.Reader, stdout io.Writer) int {
	if os.Getenv("WEIR_SUGGEST_SKIP") != "" {
		return 0
	}
	var in hookInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return 0
	}
	if in.ToolName != "Bash" || in.ToolInput.Command == "" {
		return 0
	}
	hits := Match(in.ToolInput.Command)
	if len(hits) == 0 {
		return 0
	}

	// If ANY hit is a block-action rule, emit a deny response: the model
	// retries with the suggested rewrite. Aggregate block reasons; mention
	// advisory ones too so the model sees the full picture.
	var blocks, advises []Rule
	for _, r := range hits {
		if r.Action == "block" {
			blocks = append(blocks, r)
		} else {
			advises = append(advises, r)
		}
	}

	if len(blocks) > 0 {
		lines := []string{"[weir-suggest] blocking — antipattern with a mechanical-safe rewrite:"}
		for _, r := range blocks {
			lines = append(lines, fmt.Sprintf("- [%s] %s", r.Name, r.Fix))
		}
		if len(advises) > 0 {
			lines = append(lines, "Also (non-blocking, advisory):")
			for _, r := range advises {
				lines = append(lines, fmt.Sprintf("- [%s] %s", r.Name, r.Fix))
			}
		}
		lines = append(lines, "(To bypass this block for the rest of the session, set WEIR_SUGGEST_SKIP=1 in the env.)")

		var out blockOutput
		out.HookSpecificOutput.HookEventName = "PreToolUse"
		out.HookSpecificOutput.PermissionDecision = "deny"
		out.HookSpecificOutput.PermissionDecisionReason = strings.Join(lines, "\n")
		b, err := json.Marshal(out)
		if err != nil {
			return 0
		}
		_, _ = stdout.Write(b)
		_, _ = stdout.Write([]byte("\n"))
		return 0
	}

	// Advisory-only path.
	lines := make([]string, 0, len(hits)+2)
	lines = append(lines, "[weir-suggest] antipattern(s) in this Bash command:")
	for _, r := range advises {
		lines = append(lines, fmt.Sprintf("- [%s] %s", r.Name, r.Fix))
	}
	lines = append(lines, "(Suggestion only — the command will run as-is.)")

	var out adviseOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.AdditionalContext = strings.Join(lines, "\n")

	b, err := json.Marshal(out)
	if err != nil {
		return 0
	}
	_, _ = stdout.Write(b)
	_, _ = stdout.Write([]byte("\n"))
	return 0
}
