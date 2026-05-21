package probe

import (
	"encoding/json"
	"testing"
)

// TestRunIncludesTrackedTools — the catalog tools should each appear in
// either Present or Absent (we don't assert which, since that depends on
// host PATH). Exception: tools that are the alias-target half of an
// aliasPairs entry can be dedup'd when both binaries share an inode
// (eza ships an exa compat symlink).
func TestRunIncludesTrackedTools(t *testing.T) {
	m := Run()
	if m.Version != SchemaVersion {
		t.Errorf("Version = %d; want %d", m.Version, SchemaVersion)
	}
	seen := map[string]bool{}
	for _, e := range m.Present {
		seen[e.Name] = true
	}
	for _, e := range m.Absent {
		seen[e.Name] = true
	}
	// Build the set of legitimately-dedupable alias-targets.
	dedupable := map[string]bool{}
	for _, pair := range aliasPairs {
		dedupable[pair.alias] = true
	}
	for _, t2 := range catalog {
		if !seen[t2.name] && !dedupable[t2.name] {
			t.Errorf("catalog tool %q absent from both Present and Absent lists", t2.name)
		}
	}
}

// TestMarshalSchema — marshalled output must conform to the v2 envelope
// shape: weir_probe.{version, present[], absent[]} with replaces and pkg
// rendered as JSON null when empty (not as the string "").
func TestMarshalSchema(t *testing.T) {
	m := Manifest{
		Version: 2,
		Present: []Entry{
			{Name: "jq", Replaces: "", Kind: "file", Path: "/usr/bin/jq"},
			{Name: "rg", Replaces: "grep", Kind: "file", Path: "/usr/bin/rg"},
		},
		Absent: []Entry{
			{Name: "watchexec", Replaces: "", Pkg: ""},
			{Name: "bat", Replaces: "cat", Pkg: "bat"},
		},
	}
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("marshal output not valid JSON: %v", err)
	}
	wp, ok := got["weir_probe"].(map[string]any)
	if !ok {
		t.Fatal("output missing weir_probe envelope")
	}
	if v, _ := wp["version"].(float64); int(v) != 2 {
		t.Errorf("version != 2: %v", wp["version"])
	}
	present := wp["present"].([]any)
	if len(present) != 2 {
		t.Errorf("present length = %d; want 2", len(present))
	}
	// jq is additive -> replaces must be literal null
	jq := present[0].(map[string]any)
	if jq["replaces"] != nil {
		t.Errorf("jq.replaces = %v; want null", jq["replaces"])
	}
	if jq["name"] != "jq" {
		t.Errorf("jq.name = %v; want jq", jq["name"])
	}
	// rg replaces grep -> replaces must be the string "grep"
	rg := present[1].(map[string]any)
	if rg["replaces"] != "grep" {
		t.Errorf("rg.replaces = %v; want grep", rg["replaces"])
	}
	// absent entries: watchexec has no pkg (null); bat has pkg "bat"
	absent := wp["absent"].([]any)
	we := absent[0].(map[string]any)
	if we["pkg"] != nil {
		t.Errorf("watchexec.pkg = %v; want null", we["pkg"])
	}
	bat := absent[1].(map[string]any)
	if bat["pkg"] != "bat" {
		t.Errorf("bat.pkg = %v; want bat", bat["pkg"])
	}
}

// TestAbsentAptPkgs returns sorted, unique, non-null pkg names.
func TestAbsentAptPkgs(t *testing.T) {
	m := Manifest{
		Absent: []Entry{
			{Name: "watchexec", Pkg: ""}, // skip
			{Name: "rg", Pkg: "ripgrep"},
			{Name: "fd", Pkg: "fd-find"},
			{Name: "bat", Pkg: "bat"},
			{Name: "dup", Pkg: "bat"}, // duplicate pkg, should dedupe
		},
	}
	got := m.AbsentAptPkgs()
	want := []string{"bat", "fd-find", "ripgrep"}
	if len(got) != len(want) {
		t.Fatalf("len = %d; want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}

// TestDedupeSymlinkAliases — when eza and exa exist at the SAME inode
// (eza package ships exa compat symlink), exa should be dropped.
// We construct synthetic Entry rows here; the real os.SameFile check
// runs against actual paths, so this test exercises the wiring not
// the kernel call. A separate integration test could test the inode
// comparison.
func TestDedupeSymlinkAliasesNoOpWhenOnlyOne(t *testing.T) {
	in := []Entry{
		{Name: "eza", Path: "/usr/bin/eza", Kind: "file"},
		{Name: "jq", Path: "/usr/bin/jq", Kind: "file"},
	}
	out := dedupeSymlinkAliases(in)
	if len(out) != 2 {
		t.Errorf("expected no dedup when only one of the pair is present; got %d", len(out))
	}
}
