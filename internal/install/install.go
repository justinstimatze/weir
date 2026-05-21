// Package install registers / unregisters / inspects weir's hooks in
// Claude Code's settings.json. Replaces the bash+jq install.sh.
//
// Settings.json shape we care about:
//
//	{
//	  "hooks": {
//	    "SessionStart": [{"matcher":"","hooks":[{"type":"command","command":"..."}]}],
//	    "PreToolUse":   [{"matcher":"Bash","hooks":[{"type":"command","command":"..."}]}]
//	  },
//	  ... unrelated keys preserved as-is ...
//	}
//
// Unrelated keys / hooks (e.g. someone else's UserPromptSubmit hook) must
// survive both install and uninstall. We round-trip through map[string]any
// so unknown fields preserve their data (Go json key ordering inside maps
// is not preserved — the file still parses, but key order shifts; users
// can compare backups if that matters).
package install

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justinstimatze/weir/internal/probe"
)

// HookSpec describes one hook weir wants registered.
type HookSpec struct {
	Event   string // e.g. "SessionStart", "PreToolUse"
	Matcher string // tool-name filter for *ToolUse events; "" for others
	Command string // the command string Claude Code will exec
}

// Hooks returns the canonical list of hooks weir registers, given the
// absolute path of the weir binary. Bash hooks just `weir <sub>`.
func Hooks(weirBin string) []HookSpec {
	return []HookSpec{
		{Event: "SessionStart", Matcher: "", Command: weirBin + " inject"},
		{Event: "PreToolUse", Matcher: "Bash", Command: weirBin + " suggest"},
	}
}

// Options drives the install/uninstall/status flow.
type Options struct {
	SettingsPath string // defaults to ~/.claude/settings.json
	WeirBin      string // absolute path to weir binary; used in registered commands AND as the ownership prefix for uninstall
	Stdout       io.Writer
	Stderr       io.Writer
}

func (o *Options) settingsPath() (string, error) {
	if o.SettingsPath != "" {
		return o.SettingsPath, nil
	}
	if env := os.Getenv("CLAUDE_SETTINGS"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// loadSettings reads settings.json (creating an empty {} if missing),
// validates it's parseable, and returns the parsed map.
func loadSettings(path string) (map[string]any, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Empty starting state.
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w (refusing to touch invalid JSON)", path, err)
	}
	return settings, nil
}

// backup copies path to <path>.weir-bak-<timestamp> with nanosecond
// resolution (so back-to-back invocations don't overwrite each other).
func backup(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // nothing to back up
		}
		return "", err
	}
	bak := fmt.Sprintf("%s.weir-bak-%s", path, time.Now().Format("20060102-150405.000000000"))
	if err := os.WriteFile(bak, data, 0o644); err != nil {
		return "", err
	}
	return bak, nil
}

// writeAtomic serializes settings as pretty JSON and writes it via temp-file rename.
func writeAtomic(path string, settings map[string]any) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".weir-settings-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// hooksMap returns the .hooks subtree, creating it if absent.
func hooksMap(settings map[string]any) map[string]any {
	h, ok := settings["hooks"].(map[string]any)
	if !ok {
		h = map[string]any{}
		settings["hooks"] = h
	}
	return h
}

// eventList returns settings.hooks.<event> as a []any, creating it if absent.
func eventList(settings map[string]any, event string) []any {
	h := hooksMap(settings)
	list, _ := h[event].([]any)
	if list == nil {
		list = []any{}
		h[event] = list
	}
	return list
}

// hasEntry returns true if settings.hooks.<event> contains a hook command == cmd.
func hasEntry(settings map[string]any, event, cmd string) bool {
	h, ok := settings["hooks"].(map[string]any)
	if !ok {
		return false
	}
	list, _ := h[event].([]any)
	for _, entry := range list {
		e, _ := entry.(map[string]any)
		hooks, _ := e["hooks"].([]any)
		for _, hk := range hooks {
			hm, _ := hk.(map[string]any)
			if s, _ := hm["command"].(string); s == cmd {
				return true
			}
		}
	}
	return false
}

// countWeirEntries counts hook entries whose command begins with prefix —
// used to identify weir-owned commands across all event types for status / uninstall.
func countWeirEntries(settings map[string]any, prefix string) int {
	n := 0
	h, ok := settings["hooks"].(map[string]any)
	if !ok {
		return 0
	}
	for _, raw := range h {
		list, _ := raw.([]any)
		for _, entry := range list {
			e, _ := entry.(map[string]any)
			hooks, _ := e["hooks"].([]any)
			for _, hk := range hooks {
				hm, _ := hk.(map[string]any)
				if s, _ := hm["command"].(string); strings.HasPrefix(s, prefix) {
					n++
				}
			}
		}
	}
	return n
}

// insertHook appends a new {matcher, hooks:[{type:command, command:...}]} entry.
func insertHook(settings map[string]any, event, matcher, cmd string) {
	list := eventList(settings, event)
	entry := map[string]any{
		"matcher": matcher,
		"hooks":   []any{map[string]any{"type": "command", "command": cmd}},
	}
	hooksMap(settings)[event] = append(list, entry)
}

// stripPrefix removes all hook commands beginning with prefix; prunes any
// entry left with no inner hooks; drops any event array left empty; drops
// .hooks entirely if it becomes empty.
func stripPrefix(settings map[string]any, prefix string) {
	h, ok := settings["hooks"].(map[string]any)
	if !ok {
		return
	}
	for event, raw := range h {
		list, _ := raw.([]any)
		var newList []any
		for _, entry := range list {
			e, _ := entry.(map[string]any)
			hooks, _ := e["hooks"].([]any)
			var keepHooks []any
			for _, hk := range hooks {
				hm, _ := hk.(map[string]any)
				if s, _ := hm["command"].(string); !strings.HasPrefix(s, prefix) {
					keepHooks = append(keepHooks, hk)
				}
			}
			if len(keepHooks) > 0 {
				e["hooks"] = keepHooks
				newList = append(newList, e)
			}
		}
		if len(newList) == 0 {
			delete(h, event)
		} else {
			h[event] = newList
		}
	}
	if len(h) == 0 {
		delete(settings, "hooks")
	}
}

// --- subcommand entry points ---------------------------------------------

// Status prints which weir hooks are registered + the apt-install suggestion.
// Returns exit code (0 if weir is installed, 1 otherwise).
func Status(opts Options) int {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	path, err := opts.settingsPath()
	if err != nil {
		fmt.Fprintf(opts.Stderr, "weir: %v\n", err)
		return 1
	}
	settings, err := loadSettings(path)
	if err != nil {
		fmt.Fprintf(opts.Stderr, "weir: %v\n", err)
		return 1
	}
	n := countWeirEntries(settings, opts.WeirBin)
	fmt.Fprintf(opts.Stdout, "weir: %d weir-owned hook(s) registered in %s\n", n, path)
	for _, spec := range Hooks(opts.WeirBin) {
		if hasEntry(settings, spec.Event, spec.Command) {
			fmt.Fprintf(opts.Stdout, "  [installed] %s -> %s\n", spec.Event, spec.Command)
		} else {
			fmt.Fprintf(opts.Stdout, "  [missing]   %s -> %s\n", spec.Event, spec.Command)
		}
	}
	probe.PrintAptSuggestion(opts.Stdout)
	if n == 0 {
		return 1
	}
	return 0
}

// Install registers any of weir's hooks that aren't already present.
// Idempotent: re-running adds nothing. Always prints the apt suggestion.
func Install(opts Options) int {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	path, err := opts.settingsPath()
	if err != nil {
		fmt.Fprintf(opts.Stderr, "weir: %v\n", err)
		return 1
	}
	if _, err := os.Stat(opts.WeirBin); err != nil {
		fmt.Fprintf(opts.Stderr, "weir: binary not found at %s: %v\n", opts.WeirBin, err)
		return 1
	}
	settings, err := loadSettings(path)
	if err != nil {
		fmt.Fprintf(opts.Stderr, "weir: %v\n", err)
		return 1
	}
	added := 0
	for _, spec := range Hooks(opts.WeirBin) {
		if hasEntry(settings, spec.Event, spec.Command) {
			fmt.Fprintf(opts.Stdout, "weir: [skip] %s -> %s (already registered)\n", spec.Event, spec.Command)
			continue
		}
		if added == 0 {
			if bak, err := backup(path); err != nil {
				fmt.Fprintf(opts.Stderr, "weir: backup failed: %v\n", err)
				return 1
			} else if bak != "" {
				fmt.Fprintf(opts.Stderr, "weir: backed up %s -> %s\n", path, bak)
			}
		}
		insertHook(settings, spec.Event, spec.Matcher, spec.Command)
		fmt.Fprintf(opts.Stdout, "weir: [add]  %s -> %s\n", spec.Event, spec.Command)
		added++
	}
	if added > 0 {
		if err := writeAtomic(path, settings); err != nil {
			fmt.Fprintf(opts.Stderr, "weir: write failed: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprintln(opts.Stdout, "weir: nothing to add; all hooks already registered")
	}
	probe.PrintAptSuggestion(opts.Stdout)
	return 0
}

// Uninstall removes any hook command starting with opts.WeirBin (the weir
// binary path). Doesn't touch unrelated keys or unrelated hooks. Quiet on
// no-op (mirrors install.sh).
func Uninstall(opts Options) int {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	path, err := opts.settingsPath()
	if err != nil {
		fmt.Fprintf(opts.Stderr, "weir: %v\n", err)
		return 1
	}
	settings, err := loadSettings(path)
	if err != nil {
		fmt.Fprintf(opts.Stderr, "weir: %v\n", err)
		return 1
	}
	n := countWeirEntries(settings, opts.WeirBin)
	if n == 0 {
		fmt.Fprintln(opts.Stdout, "weir: no weir-owned hooks present; nothing to remove")
		return 0
	}
	bak, err := backup(path)
	if err != nil {
		fmt.Fprintf(opts.Stderr, "weir: backup failed: %v\n", err)
		return 1
	}
	if bak != "" {
		fmt.Fprintf(opts.Stderr, "weir: backed up %s -> %s\n", path, bak)
	}
	stripPrefix(settings, opts.WeirBin)
	if err := writeAtomic(path, settings); err != nil {
		fmt.Fprintf(opts.Stderr, "weir: write failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(opts.Stdout, "weir: removed %d weir-owned hook(s) from %s\n", n, path)
	return 0
}
