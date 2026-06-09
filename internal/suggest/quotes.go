package suggest

// isInsideShellQuotes reports whether byte position pos in cmd is inside a
// single- or double-quoted shell string. Used to suppress block-mode rules
// whose match landed inside quoted prose — most commonly a commit message
// body passed as `git commit -m "..."` containing the literal text "which"
// or "cat FILE | tool".
//
// Approximate: tracks single and double quote state with backslash-escape
// handling inside double quotes (matching bash semantics — single quotes
// don't process escapes per POSIX). Does NOT model $'...', heredoc bodies
// as separate scopes, or backticks. Pragmatic effect: if the matched offset
// is wrapped by an unclosed " or ', suppression fires.
func isInsideShellQuotes(cmd string, pos int) bool {
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
	return inSingle || inDouble
}
