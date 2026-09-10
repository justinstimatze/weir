// Package rulehistory tracks, per Claude Code project, which
// internal/suggest rules have ever fired in that project's own Bash-command
// history. internal/inject uses it to mute a gotcha's SessionStart priming
// copy once a project's history shows the matching rule has never been
// relevant there — the live PreToolUse guard is a separate, unconditional
// layer and is never affected by anything in this package.
//
// This is the first persistent, cross-invocation, weir-owned cache weir has
// ever needed (everything else is either Claude Code's own settings.json or
// a build-time go:embed artifact). Every failure path here fails OPEN: Load
// never returns an error, and any problem (missing cache dir, corrupt JSON,
// unreadable transcript, unresolvable project identity) yields
// Status{Evidence: false}, which callers must treat as "show everything."
package rulehistory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/justinstimatze/weir/internal/suggest"
)

// MinEvidenceBashCalls is the minimum number of Bash calls a project's
// history must contain before any gotcha becomes eligible for muting. Its
// job is narrow: distinguish "this project has real history" from "this
// project has three Bash calls and a zero count on everything is
// meaningless." It is NOT a statistical-power threshold for "this rule is
// provably irrelevant here" — that can never be concluded from absence
// alone, especially for a rarely-used tool. Deliberately conservative: the
// cost of erring high is gotchas staying shown a bit longer than strictly
// necessary, the same safe-by-default posture as the cold-start case below.
const MinEvidenceBashCalls = 200

const schemaVersion = 1

// Status is what internal/inject needs to decide whether to mute a gotcha.
type Status struct {
	Evidence       bool           // project has cleared MinEvidenceBashCalls
	Fired          map[string]int // suggest rule name -> times fired, ever, in this project
	TotalBashCalls int
}

// cacheFile is the on-disk schema at <UserCacheDir>/weir/projects/<name>.json.
type cacheFile struct {
	SchemaVersion  int                  `json:"schema_version"`
	RuleSetKey     string               `json:"rule_set_key"` // suggest.RuleSetFingerprint()
	TotalBashCalls int                  `json:"total_bash_calls"`
	Fired          map[string]int       `json:"fired,omitempty"`
	Files          map[string]fileStats `json:"files,omitempty"` // transcript path -> its own contribution
	UpdatedAt      string               `json:"updated_at,omitempty"`
}

// fileStats is one transcript file's contribution, stored separately per
// file so a changed file's whole-file rescan replaces only its own
// contribution rather than requiring a subtract-then-add against a single
// global counter.
type fileStats struct {
	Size           int64          `json:"size"`
	TotalBashCalls int            `json:"total_bash_calls"`
	Fired          map[string]int `json:"fired,omitempty"`
}

// Load resolves project identity — preferring transcriptPath (the current
// transcript file's real path, read directly off SessionStart hook stdin;
// needs no reconstruction), falling back to cwd only if transcriptPath is
// unavailable — and returns the project's rule-fire history, updating the
// on-disk cache as needed. Never returns an error: any failure anywhere
// (missing cache dir, corrupt JSON, unreadable transcript, unresolvable
// project identity) yields Status{Evidence: false}, and callers must render
// every gotcha in response rather than treat a zero Status as "nothing fires
// here."
func Load(transcriptPath, cwd string) Status {
	projectDir := projectDirFor(transcriptPath, cwd)
	if projectDir == "" {
		return Status{}
	}

	cachePath, err := cacheFilePath(filepath.Base(projectDir))
	if err != nil {
		return Status{}
	}

	cf := readCache(cachePath)
	key := suggest.RuleSetFingerprint()
	if cf.RuleSetKey != key {
		// Rule set changed since this cache was written — a pattern edit
		// could make "never fired" data wrong in either direction, so
		// discard everything and force a full rescan of every file.
		cf = cacheFile{RuleSetKey: key, SchemaVersion: schemaVersion}
	}

	files, err := projectTranscripts(projectDir)
	if err != nil {
		return Status{}
	}

	changed := false
	seen := make(map[string]bool, len(files))
	for _, path := range files {
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if existing, ok := cf.Files[path]; ok && existing.Size == info.Size() {
			continue // unchanged since last scan — the common case
		}
		stats, err := scanFile(path)
		if err != nil {
			continue
		}
		stats.Size = info.Size()
		if cf.Files == nil {
			cf.Files = map[string]fileStats{}
		}
		cf.Files[path] = stats
		changed = true
	}
	// Drop entries for transcripts that no longer exist (rare — a manually
	// deleted or moved transcript file).
	for path := range cf.Files {
		if !seen[path] {
			delete(cf.Files, path)
			changed = true
		}
	}

	total := 0
	fired := map[string]int{}
	for _, fs := range cf.Files {
		total += fs.TotalBashCalls
		for name, n := range fs.Fired {
			fired[name] += n
		}
	}

	if changed {
		cf.TotalBashCalls = total
		cf.Fired = fired
		cf.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeCacheAtomic(cachePath, cf) // best-effort; a failed write just means the next Load rescans again
	}

	return Status{
		Evidence:       total >= MinEvidenceBashCalls,
		Fired:          fired,
		TotalBashCalls: total,
	}
}

// projectDirFor returns the ~/.claude/projects/<name> directory holding
// this project's transcripts, or "" if identity can't be resolved.
func projectDirFor(transcriptPath, cwd string) string {
	if transcriptPath != "" {
		return filepath.Dir(transcriptPath)
	}
	if cwd == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects", sanitizeCWD(cwd))
}

var reNonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// sanitizeCWD mirrors Claude Code's own transcript-directory naming, for use
// only when no transcriptPath is available to read the answer from
// directly (the preferred path in projectDirFor — this is the fallback).
// Confirmed empirically against this host's own ~/.claude/projects/
// entries: both '/' and '.' become '-'
// (".../publicai/aipotluck.org" -> "...-publicai-aipotluck-org"). Generalized
// to every non-alphanumeric character as the simplest rule consistent with
// both observations; unconfirmed for other punctuation, which is exactly
// why the transcriptPath path above is preferred whenever it's available.
func sanitizeCWD(cwd string) string {
	return reNonAlnum.ReplaceAllString(cwd, "-")
}

func cacheFilePath(basename string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "weir", "projects", basename+".json"), nil
}

func readCache(path string) cacheFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheFile{}
	}
	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return cacheFile{}
	}
	if cf.SchemaVersion != schemaVersion {
		return cacheFile{}
	}
	return cf
}

// writeCacheAtomic mirrors internal/install/install.go's writeAtomic:
// temp file in the same directory, then rename, so a reader never observes
// a half-written cache.
func writeCacheAtomic(path string, cf cacheFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".weir-rulehistory-*")
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

func projectTranscripts(dir string) ([]string, error) {
	patterns := []string{
		filepath.Join(dir, "*.jsonl"),
		filepath.Join(dir, "subagents", "*.jsonl"),
	}
	var paths []string
	for _, pat := range patterns {
		m, err := filepath.Glob(pat)
		if err != nil {
			return nil, err
		}
		paths = append(paths, m...)
	}
	return paths, nil
}
