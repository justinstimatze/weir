package suggest

import "strings"

// isInsideShellString reports whether byte position pos in cmd sits inside a
// suppressed shell string context: a single- or double-quoted string, or the
// body of a heredoc. Used to keep block-mode rules from firing on prose
// passed as `git commit -m "..."` or `git commit -F - <<'EOF' ... EOF` —
// the exact class of false-block that trains people to disable the whole
// ruleset.
//
// Approximate. Tracks:
//   - Single quotes (POSIX: no escapes inside).
//   - Double quotes (bash: backslash escapes the next byte).
//   - Heredoc bodies opened by `<<[-]?['"]?DELIM['"]?` and closed by a line
//     whose content equals DELIM (dedented if the opener used `<<-`).
//
// Does NOT model: $'...', backticks, nested heredocs, or heredoc openers
// that themselves sit inside a quoted string. Effect of those gaps is
// over-suppression (a real antipattern slips past a block rule), not
// under-suppression (a legit command gets refused).
func isInsideShellString(cmd string, pos int) bool {
	inSingle := false
	inDouble := false
	for i := 0; i < pos && i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\\' && inDouble && i+1 < len(cmd):
			i++
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		}
	}
	if inSingle || inDouble {
		return true
	}
	if !strings.Contains(cmd, "<<") {
		return false
	}
	for _, span := range findHeredocs(cmd) {
		if pos >= span.start && pos < span.end {
			return true
		}
	}
	return false
}

// heredocSpan is a half-open byte range [start, end) covering a heredoc body
// (the lines AFTER the opener's newline and BEFORE the terminator line).
type heredocSpan struct {
	start, end int
}

// findHeredocs scans cmd once and returns every heredoc body as a byte range.
// Sequential (not nested): after each body, scanning resumes past the
// terminator line.
func findHeredocs(cmd string) []heredocSpan {
	var out []heredocSpan
	i := 0
	for i < len(cmd)-1 {
		if cmd[i] != '<' || cmd[i+1] != '<' {
			i++
			continue
		}
		// Skip herestring `<<<`.
		if i+2 < len(cmd) && cmd[i+2] == '<' {
			i += 3
			continue
		}
		j := i + 2
		dedent := false
		if j < len(cmd) && cmd[j] == '-' {
			dedent = true
			j++
		}
		for j < len(cmd) && (cmd[j] == ' ' || cmd[j] == '\t') {
			j++
		}
		var quote byte
		if j < len(cmd) && (cmd[j] == '\'' || cmd[j] == '"') {
			quote = cmd[j]
			j++
		}
		delimStart := j
		for j < len(cmd) {
			c := cmd[j]
			if quote != 0 {
				if c == quote {
					break
				}
			} else if !isDelimChar(c) {
				break
			}
			j++
		}
		delim := cmd[delimStart:j]
		if delim == "" {
			i++
			continue
		}
		if quote != 0 && j < len(cmd) {
			j++
		}
		// Scan to end of the opener line. Body span INCLUDES that
		// trailing \n and continues to (but not including) the
		// terminator line. Including the \n matters because block-mode
		// regexes like `which-vs-command-v` anchor on the `\n` before
		// the in-body word — FindStringIndex returns the anchor \n's
		// position, which must fall inside the suppression range.
		for j < len(cmd) && cmd[j] != '\n' {
			j++
		}
		if j >= len(cmd) {
			// No newline after opener — treat body as empty at EOF.
			out = append(out, heredocSpan{j, j})
			i = j
			continue
		}
		bodyStart := j
		j++
		bodyEnd := len(cmd)
		nextI := len(cmd)
		for k := j; k <= len(cmd); {
			lineEnd := k
			for lineEnd < len(cmd) && cmd[lineEnd] != '\n' {
				lineEnd++
			}
			line := cmd[k:lineEnd]
			if dedent {
				line = strings.TrimLeft(line, "\t")
			}
			if line == delim {
				bodyEnd = k
				if lineEnd < len(cmd) {
					nextI = lineEnd + 1
				} else {
					nextI = lineEnd
				}
				break
			}
			if lineEnd >= len(cmd) {
				nextI = lineEnd
				break
			}
			k = lineEnd + 1
		}
		out = append(out, heredocSpan{bodyStart, bodyEnd})
		i = nextI
	}
	return out
}

func isDelimChar(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
}
