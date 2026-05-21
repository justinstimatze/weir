// Package probe is weir's layer-1 capability discovery: which modern shell
// tools are reachable on the host's PATH, and which apt packages would
// supply the missing ones. Replaces probe.sh.
//
// On Debian/Ubuntu, fd and bat install as `fdfind` / `batcat`. The probe
// looks for the canonical name first, then falls back to the Debian alias
// while reporting the canonical name in the manifest (path field points
// at the actual on-disk binary).
//
// kind: this Go probe only sees PATH binaries — function / alias shims in
// the caller's interactive shell are not visible (same limitation the
// bash probe has when run as a child script). All present entries are
// kind="file".
package probe

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
)

// SchemaVersion is the major version of the manifest format. Bump on
// incompatible changes.
const SchemaVersion = 2

// Entry describes one tool, used for both `present` and `absent` lists.
// `Replaces` is the classic coreutils tool this stands in for, or empty
// (rendered as null in JSON) if additive.
type Entry struct {
	Name     string `json:"name"`
	Replaces string `json:"-"` // empty -> null in JSON; handled in MarshalJSON below
	Kind     string `json:"kind,omitempty"`
	Path     string `json:"path,omitempty"`
	Pkg      string `json:"-"` // empty -> null in JSON; only meaningful for absent
}

// presentJSON / absentJSON have explicit json.RawMessage Replaces/Pkg so
// we can emit literal null when the field is empty. (Go doesn't naturally
// distinguish "" from null in struct marshaling.)

type presentJSON struct {
	Name     string          `json:"name"`
	Replaces json.RawMessage `json:"replaces"`
	Kind     string          `json:"kind"`
	Path     string          `json:"path"`
}

type absentJSON struct {
	Name     string          `json:"name"`
	Replaces json.RawMessage `json:"replaces"`
	Pkg      json.RawMessage `json:"pkg"`
}

// Manifest is the full probe output.
type Manifest struct {
	Version int     `json:"version"`
	Present []Entry `json:"-"`
	Absent  []Entry `json:"-"`
}

type manifestJSON struct {
	Version int           `json:"version"`
	Present []presentJSON `json:"present"`
	Absent  []absentJSON  `json:"absent"`
}

type envelope struct {
	WeirProbe manifestJSON `json:"weir_probe"`
}

// tool describes one weir-tracked tool: its canonical name, the classic
// coreutils tool it replaces (empty = additive), the apt package that
// provides it (empty = not in stock apt), and the Debian-aliased binary
// to fall back to (empty = no alias).
type tool struct {
	name     string
	replaces string
	pkg      string
	debAlt   string
}

// catalog is the canonical weir tool table. Mirrors probe.sh's TOOLS +
// APT_PKG + DEB_ALT maps; the bash version was the spec.
var catalog = []tool{
	// replaces a coreutils tool
	{"rg", "grep", "ripgrep", ""},
	{"fd", "find", "fd-find", "fdfind"},
	{"bat", "cat", "bat", "batcat"},
	{"sd", "sed", "sd", ""},
	{"mlr", "awk", "miller", ""},
	{"eza", "ls", "eza", ""},
	{"exa", "ls", "", ""},
	{"dust", "du", "", ""},
	{"duf", "df", "duf", ""},
	{"procs", "ps", "procs", ""},
	{"bottom", "top", "", ""},
	{"btm", "top", "", ""},
	{"delta", "diff", "git-delta", ""},
	{"choose", "cut", "", ""},
	{"hexyl", "xxd", "hexyl", ""},
	// additive (no replaces)
	{"jq", "", "jq", ""},
	{"yq", "", "", ""},
	{"dasel", "", "", ""},
	{"gron", "", "", ""},
	{"sponge", "", "moreutils", ""},
	{"pv", "", "pv", ""},
	{"parallel", "", "parallel", ""},
	{"hyperfine", "", "hyperfine", ""},
	{"up", "", "", ""},
	{"teip", "", "", ""},
	{"xsv", "", "", ""},
	{"qsv", "", "", ""},
	{"fzf", "", "fzf", ""},
	{"watchexec", "", "", ""},
	{"entr", "", "entr", ""},
}

// Run scans the catalog against $PATH and returns a Manifest. Sorted by name.
func Run() Manifest {
	sorted := make([]tool, len(catalog))
	copy(sorted, catalog)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })

	m := Manifest{Version: SchemaVersion}
	for _, t := range sorted {
		path, _ := exec.LookPath(t.name)
		if path == "" && t.debAlt != "" {
			path, _ = exec.LookPath(t.debAlt)
		}
		if path != "" {
			m.Present = append(m.Present, Entry{
				Name:     t.name,
				Replaces: t.replaces,
				Kind:     "file",
				Path:     path,
			})
		} else {
			m.Absent = append(m.Absent, Entry{
				Name:     t.name,
				Replaces: t.replaces,
				Pkg:      t.pkg,
			})
		}
	}
	m.Present = dedupeSymlinkAliases(m.Present)
	return m
}

// aliasPairs lists known {primary, deprecated-or-compat} pairs. When BOTH
// are present and stat to the same (dev, inode), the alias is dropped from
// the manifest. Real-world case: the `eza` apt package ships an `exa` compat
// binary at the same inode, so reporting both is just noise.
var aliasPairs = []struct {
	primary string
	alias   string
}{
	{primary: "eza", alias: "exa"},
}

func dedupeSymlinkAliases(present []Entry) []Entry {
	byName := make(map[string]int, len(present))
	for i, e := range present {
		byName[e.Name] = i
	}
	dropIdx := make(map[int]bool)
	for _, pair := range aliasPairs {
		pi, pok := byName[pair.primary]
		ai, aok := byName[pair.alias]
		if !pok || !aok {
			continue
		}
		ps, perr := os.Stat(present[pi].Path)
		as, aerr := os.Stat(present[ai].Path)
		if perr != nil || aerr != nil {
			continue
		}
		if os.SameFile(ps, as) {
			dropIdx[ai] = true
		}
	}
	if len(dropIdx) == 0 {
		return present
	}
	out := make([]Entry, 0, len(present)-len(dropIdx))
	for i, e := range present {
		if !dropIdx[i] {
			out = append(out, e)
		}
	}
	return out
}

func toJSON(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("null")
	}
	b, _ := json.Marshal(s)
	return b
}

// Marshal emits the v2 JSON envelope.
func (m Manifest) Marshal() ([]byte, error) {
	mj := manifestJSON{Version: m.Version}
	for _, e := range m.Present {
		mj.Present = append(mj.Present, presentJSON{
			Name: e.Name, Replaces: toJSON(e.Replaces), Kind: e.Kind, Path: e.Path,
		})
	}
	for _, e := range m.Absent {
		mj.Absent = append(mj.Absent, absentJSON{
			Name: e.Name, Replaces: toJSON(e.Replaces), Pkg: toJSON(e.Pkg),
		})
	}
	return json.Marshal(envelope{WeirProbe: mj})
}

// AbsentAptPkgs returns the sorted, unique apt package names for absent
// tools that have a known apt-package mapping.
func (m Manifest) AbsentAptPkgs() []string {
	seen := map[string]struct{}{}
	for _, e := range m.Absent {
		if e.Pkg != "" {
			seen[e.Pkg] = struct{}{}
		}
	}
	pkgs := make([]string, 0, len(seen))
	for p := range seen {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	return pkgs
}

// CmdProbe is the entry point for `weir probe`. Emits JSON to stdout.
func CmdProbe(_ []string, _ io.Reader, stdout io.Writer) int {
	m := Run()
	b, err := m.Marshal()
	if err != nil {
		return 1
	}
	stdout.Write(b)
	stdout.Write([]byte("\n"))
	return 0
}

// PrintAptSuggestion runs the probe and prints a multi-line apt suggestion
// to w if there are absent-but-installable tools. Called by Status / Install
// to surface the gap to the user at every install moment.
func PrintAptSuggestion(w io.Writer) {
	m := Run()
	pkgs := m.AbsentAptPkgs()
	if len(pkgs) == 0 {
		return
	}
	absentN := 0
	for _, e := range m.Absent {
		if e.Pkg != "" {
			absentN++
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "weir: probe found %d modern tool(s) installed; %d more available from stock apt.\n",
		len(m.Present), absentN)
	fmt.Fprintln(w, "weir: to expand weir's manifest with these tools, run:")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  sudo apt install %s\n", joinSpace(pkgs))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "weir: not run automatically (sudo). Re-running 'weir install' is idempotent.")
}

func joinSpace(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}
