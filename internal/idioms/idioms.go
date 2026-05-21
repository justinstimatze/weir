// Package idioms loads weir's per-tool idiom corpus (built from
// tldr-pages by `weir build-idioms`) and exposes typed access.
//
// The JSON is embedded at build time via //go:embed so the binary is
// fully self-contained: no runtime file dependency, no path discovery.
package idioms

import (
	_ "embed"
	"encoding/json"
)

//go:embed idioms.json
var rawCorpus []byte

//go:embed composition.json
var rawComposition []byte

// Idiom is one (intent, command) pair from a tldr page.
type Idiom struct {
	Intent string `json:"intent"`
	Cmd    string `json:"cmd"`
}

// Composition is one cross-tool idiom: a goal-shaped pipeline plus the
// list of tools that must be present for the idiom to make sense.
type Composition struct {
	Intent string   `json:"intent"`
	Cmd    string   `json:"cmd"`
	Tools  []string `json:"tools"`
}

// Corpus is the parsed idioms.json envelope.
type Corpus struct {
	Meta struct {
		SourceRoot   string `json:"source_root"`
		SourcesUsed  int    `json:"sources_used"`
		PerToolCap   int    `json:"per_tool_cap"`
		ToolsCovered int    `json:"tools_covered"`
	} `json:"meta"`
	Idioms map[string][]Idiom `json:"idioms"`
	// Compositions are loaded from composition.json (hand-curated; not built by
	// build-idioms). Filtered at render time by tool-deps presence.
	Compositions []Composition `json:"-"`
}

type compositionEnvelope struct {
	Composition []Composition `json:"composition"`
}

var loaded *Corpus

// Load returns the embedded corpus, parsing on first access.
func Load() (*Corpus, error) {
	if loaded != nil {
		return loaded, nil
	}
	var c Corpus
	if err := json.Unmarshal(rawCorpus, &c); err != nil {
		return nil, err
	}
	var comp compositionEnvelope
	if err := json.Unmarshal(rawComposition, &comp); err == nil {
		c.Compositions = comp.Composition
	}
	loaded = &c
	return loaded, nil
}

// For returns up to n idioms for the named tool, or nil if the tool isn't in the corpus.
func (c *Corpus) For(tool string, n int) []Idiom {
	ids := c.Idioms[tool]
	if n >= 0 && n < len(ids) {
		return ids[:n]
	}
	return ids
}

// CompositionsFor returns the cross-tool idioms whose required tools are all
// in `present`. Empty `Tools` means "no tool deps" (the idiom uses only
// coreutils or built-ins).
func (c *Corpus) CompositionsFor(present map[string]bool) []Composition {
	var out []Composition
	for _, comp := range c.Compositions {
		ok := true
		for _, t := range comp.Tools {
			if !present[t] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, comp)
		}
	}
	return out
}
