package suggest

import "regexp"

// Rewrites for block rules whose fix is mechanical. When every block hit in a
// command has one, and the session runs with permissions bypassed, the hook
// applies the rewrite and lets the command run instead of refusing it, which
// saves the model a retry (Justin's 2026-10-08 ceremony cuts, item 4). Each
// rewrite skips matches inside a quoted string or heredoc body, the same
// guard Match applies, so prose that mentions `cat x | grep` is never edited.

var (
	// `cat FILE | TOOL` -> `< FILE TOOL`. The redirect form keeps TOOL reading
	// stdin, so output is byte-identical: `wc -l < f` prints no filename, where
	// the `wc -l f` form the advice names would add one.
	uuocRewrite = regexp.MustCompile(`(` + cmdPos + `)cat[ \t]+([^-\s|;&<>'"]\S*)[ \t]*\|[ \t]*(grep|head|tail|sed|awk|jq|less|more|wc|sort|uniq|mlr|bat|rg|fd|fzf)\b`)
	// `which NAME` -> `command -v NAME`, only with no flags in between: `which
	// -a` has no `command -v` equivalent.
	whichRewrite = regexp.MustCompile(`(` + cmdPos + `)which[ \t]+([A-Za-z_][\w.-]*)`)
	// `rg -rn` -> `rg -n`: the bundled -r is grep's recursion habit, and rg
	// recurses by default, so dropping it is the whole fix.
	rgBundledRewrite = regexp.MustCompile(`(\brg\b[^|\n;&]*` + argGap + `)-r([nliwcv])\b`)
)

var rewrites = map[string]func(string) (string, bool){
	"uuoc":                 func(c string) (string, bool) { return rewriteAll(c, uuocRewrite, "${1}< ${2} ${3}") },
	"which-vs-command-v":   func(c string) (string, bool) { return rewriteAll(c, whichRewrite, "${1}command -v ${2}") },
	"rg-r-misfire-bundled": func(c string) (string, bool) { return rewriteAll(c, rgBundledRewrite, "${1}-${2}") },
}

// rewriteAll replaces every match outside a shell string. It reports false
// when nothing outside a string matched, so the caller falls back to a block.
func rewriteAll(cmd string, re *regexp.Regexp, tmpl string) (string, bool) {
	locs := re.FindAllStringSubmatchIndex(cmd, -1)
	out := make([]byte, 0, len(cmd))
	last, changed := 0, false
	for _, loc := range locs {
		if isInsideShellString(cmd, loc[0]) {
			continue
		}
		out = append(out, cmd[last:loc[0]]...)
		out = re.ExpandString(out, tmpl, cmd, loc)
		last = loc[1]
		changed = true
	}
	if !changed {
		return cmd, false
	}
	return string(append(out, cmd[last:]...)), true
}

// applyRewrites rewrites cmd for every block rule hit, or reports false if
// any hit has no rewrite or its rewrite did not apply.
func applyRewrites(cmd string, blocks []Rule) (string, bool) {
	for _, r := range blocks {
		f, ok := rewrites[r.Name]
		if !ok {
			return "", false
		}
		next, ok := f(cmd)
		if !ok {
			return "", false
		}
		cmd = next
	}
	return cmd, true
}
