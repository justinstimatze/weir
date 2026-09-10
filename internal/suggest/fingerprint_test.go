package suggest

import (
	"regexp"
	"testing"
)

// TestFingerprintStableWithNoChange — calling it twice against the same
// Rules must agree.
func TestFingerprintStableWithNoChange(t *testing.T) {
	a := RuleSetFingerprint()
	b := RuleSetFingerprint()
	if a != b {
		t.Errorf("fingerprint not stable: %q vs %q", a, b)
	}
}

// TestFingerprintChangesOnPatternEdit — editing a rule's Pattern (same Name)
// must change the fingerprint. This is the exact case the whole feature
// exists to catch: a rules.go edit that a version-string key would miss.
func TestFingerprintChangesOnPatternEdit(t *testing.T) {
	orig := Rules
	defer func() { Rules = orig }()

	before := RuleSetFingerprint()

	Rules = append([]Rule{}, orig...)
	Rules[0] = Rule{
		Name:    orig[0].Name,
		Pattern: regexp.MustCompile(orig[0].Pattern.String() + `X`),
		Fix:     orig[0].Fix,
	}
	after := RuleSetFingerprint()

	if before == after {
		t.Error("fingerprint did not change after editing a rule's Pattern")
	}
}

// TestFingerprintChangesOnRuleCountChange — adding or removing a rule must
// change the fingerprint.
func TestFingerprintChangesOnRuleCountChange(t *testing.T) {
	orig := Rules
	defer func() { Rules = orig }()

	before := RuleSetFingerprint()

	Rules = append(append([]Rule{}, orig...), Rule{
		Name:    "adhoc-test-only-rule",
		Pattern: regexp.MustCompile(`adhoc-test-only-pattern`),
	})
	after := RuleSetFingerprint()

	if before == after {
		t.Error("fingerprint did not change after adding a rule")
	}
}
