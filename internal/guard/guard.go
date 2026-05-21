// Package guard wraps hook entry points so a panic inside weir never
// reaches Claude Code's hook subprocess as a non-zero exit. The fail-open
// contract — "weir cannot break your session" — is enforced here.
//
// Use:
//
//	func CmdInject(args []string, in io.Reader, out io.Writer) int {
//	    return guard.Hook("inject", func() int {
//	        ... actual work ...
//	    })
//	}
//
// On panic: writes a one-line marker to stderr (visible to the user if
// they tail the hook log) and returns 0 so Claude Code treats the hook
// as a successful no-op. Real errors are still surfaced via the function's
// own return value; only true panics are swallowed.
package guard

import (
	"fmt"
	"os"
	"runtime/debug"
)

// Hook runs fn, recovers from any panic, returns fn's exit code on
// success or 0 on panic. name identifies the hook in the panic stderr line.
func Hook(name string, fn func() int) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "weir/%s: panic recovered (fail-open): %v\n%s\n",
				name, r, debug.Stack())
			code = 0
		}
	}()
	return fn()
}
