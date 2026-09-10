package rulehistory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempCacheDir points os.UserCacheDir() at a fresh temp dir for the
// duration of the test, so tests never touch the real ~/.cache/weir/.
func withTempCacheDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// writeTranscript writes a minimal transcript JSONL file with one Bash
// tool_use line per entry in cmds, plus a non-Bash noise line that must be
// ignored without error.
func writeTranscript(t *testing.T, dir string, cmds []string) string {
	t.Helper()
	path := filepath.Join(dir, "session.jsonl")
	var b strings.Builder
	for _, cmd := range cmds {
		line := map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": cmd}},
				},
			},
		}
		data, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	b.WriteString(`{"type":"user","cwd":"/tmp/irrelevant"}` + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadColdStartBelowEvidenceBar(t *testing.T) {
	withTempCacheDir(t)
	transcript := writeTranscript(t, t.TempDir(), []string{"ls -la", "git status"})

	st := Load(transcript, "")
	if st.Evidence {
		t.Error("expected Evidence=false with only 2 bash calls")
	}
	if st.TotalBashCalls != 2 {
		t.Errorf("expected TotalBashCalls=2, got %d", st.TotalBashCalls)
	}
}

func TestLoadTracksRuleFiresPastEvidenceBar(t *testing.T) {
	withTempCacheDir(t)
	cmds := make([]string, 0, MinEvidenceBashCalls+1)
	for i := 0; i < MinEvidenceBashCalls; i++ {
		cmds = append(cmds, "git status")
	}
	cmds = append(cmds, "grep foo file.txt | wc -l") // fires grep-wc, rules.go:93
	transcript := writeTranscript(t, t.TempDir(), cmds)

	st := Load(transcript, "")
	if !st.Evidence {
		t.Fatalf("expected Evidence=true with %d calls", st.TotalBashCalls)
	}
	if st.Fired["grep-wc"] != 1 {
		t.Errorf("expected grep-wc fired once, got %d (fired=%v)", st.Fired["grep-wc"], st.Fired)
	}
}

func TestLoadWritesCacheFile(t *testing.T) {
	withTempCacheDir(t)
	transcript := writeTranscript(t, t.TempDir(), []string{"git status"})

	Load(transcript, "")

	cachePath, err := cacheFilePath(filepath.Base(filepath.Dir(transcript)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Errorf("expected cache file at %s: %v", cachePath, err)
	}
}

func TestLoadRescansOnGrowth(t *testing.T) {
	withTempCacheDir(t)
	transcript := writeTranscript(t, t.TempDir(), []string{"git status"})

	st1 := Load(transcript, "")
	if st1.TotalBashCalls != 1 {
		t.Fatalf("expected 1 call before growth, got %d", st1.TotalBashCalls)
	}

	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{
				{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "git log"}},
			},
		},
	})
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	f.Close()

	st2 := Load(transcript, "")
	if st2.TotalBashCalls != 2 {
		t.Errorf("expected 2 calls after growth, got %d", st2.TotalBashCalls)
	}
}

// TestLoadSkipsRescanWhenSizeUnchanged tampers the cached count directly and
// confirms it survives a second Load — proving an unchanged file is skipped
// rather than rescanned (a black-box count check alone couldn't distinguish
// "skipped" from "rescanned and got the same answer").
func TestLoadSkipsRescanWhenSizeUnchanged(t *testing.T) {
	withTempCacheDir(t)
	transcript := writeTranscript(t, t.TempDir(), []string{"grep foo file | wc -l"})

	Load(transcript, "")

	cachePath, err := cacheFilePath(filepath.Base(filepath.Dir(transcript)))
	if err != nil {
		t.Fatal(err)
	}
	cf := readCache(cachePath)
	fs := cf.Files[transcript]
	fs.Fired["grep-wc"] = 999
	cf.Files[transcript] = fs
	if err := writeCacheAtomic(cachePath, cf); err != nil {
		t.Fatal(err)
	}

	st := Load(transcript, "")
	if st.Fired["grep-wc"] != 999 {
		t.Errorf("expected tampered value 999 to survive an unchanged-size file, got %d", st.Fired["grep-wc"])
	}
}

func TestLoadRescansOnRuleSetChange(t *testing.T) {
	withTempCacheDir(t)
	transcript := writeTranscript(t, t.TempDir(), []string{"grep foo file | wc -l"})

	Load(transcript, "")

	cachePath, err := cacheFilePath(filepath.Base(filepath.Dir(transcript)))
	if err != nil {
		t.Fatal(err)
	}
	cf := readCache(cachePath)
	cf.RuleSetKey = "stale-fingerprint"
	if err := writeCacheAtomic(cachePath, cf); err != nil {
		t.Fatal(err)
	}

	st := Load(transcript, "")
	if st.Fired["grep-wc"] != 1 {
		t.Errorf("expected a fresh rescan after rule-set-key mismatch, got %d", st.Fired["grep-wc"])
	}
}

func TestLoadCorruptCacheFailsOpen(t *testing.T) {
	withTempCacheDir(t)
	transcript := writeTranscript(t, t.TempDir(), []string{"grep foo file | wc -l"})

	cachePath, err := cacheFilePath(filepath.Base(filepath.Dir(transcript)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := Load(transcript, "")
	if st.Fired["grep-wc"] != 1 {
		t.Errorf("expected corrupt cache to fail open to a fresh scan, got fired=%v", st.Fired)
	}
}

func TestLoadUnresolvableIdentityFailsOpen(t *testing.T) {
	withTempCacheDir(t)
	st := Load("", "")
	if st.Evidence {
		t.Error("expected Evidence=false when project identity can't be resolved")
	}
	if st.Fired != nil {
		t.Errorf("expected nil Fired map, got %v", st.Fired)
	}
}

func TestSanitizeCWD(t *testing.T) {
	got := sanitizeCWD("/home/gas6amus/Documents/publicai/aipotluck.org")
	want := "-home-gas6amus-Documents-publicai-aipotluck-org"
	if got != want {
		t.Errorf("sanitizeCWD() = %q, want %q", got, want)
	}
}
