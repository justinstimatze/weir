package suggest

import (
	"crypto/sha256"
	"encoding/hex"
)

// RuleSetFingerprint identifies the current shape of Rules. Callers that
// cache anything keyed on "which rules exist and what they match" (e.g.
// internal/rulehistory) use this to detect a rules.go change and invalidate
// rather than trust stale data. Deliberately independent of main.version:
// version is a build-time ldflags injection that stays "dev" for every local
// `go build .`, so keying invalidation on it would mean a maintainer editing
// a rule locally never sees the cache invalidate until a release tag — the
// same drift go-cli-versioning already warns about elsewhere. A content
// fingerprint invalidates itself the moment rules.go actually changes, with
// no coordination with the release process.
//
// Pattern.String()/Suppress.String() round-trip the compiled regex source,
// so a pattern edit that doesn't touch Name changes the fingerprint too.
func RuleSetFingerprint() string {
	h := sha256.New()
	for _, r := range Rules {
		h.Write([]byte(r.Name))
		h.Write([]byte{0})
		h.Write([]byte(r.Pattern.String()))
		h.Write([]byte{0})
		if r.Suppress != nil {
			h.Write([]byte(r.Suppress.String()))
		}
		h.Write([]byte{0})
		h.Write([]byte(r.Action))
		h.Write([]byte{0})
		h.Write([]byte(r.OS))
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
