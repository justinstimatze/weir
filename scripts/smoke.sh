#!/usr/bin/env bash
# weir install-cycle smoke test.
#
# Builds the binary, points install at a temp settings.json that contains
# unrelated state (sentinel key + a UserPromptSubmit hook from a hypothetical
# other tool), and verifies:
#   1. status on empty file reports "not installed"
#   2. install adds weir's two hooks, leaves unrelated state intact
#   3. status reports both hooks registered
#   4. install (re-run) is idempotent
#   5. uninstall removes only weir-owned hooks
#   6. unrelated key + unrelated hook still present at the end
#   7. weir review (block) refuses `which X` (advisory rules still emit text)
#
# Exits non-zero on any check failure. CI invokes this on every push/PR.
set -euo pipefail

WEIR_BIN=${WEIR_BIN:-}
if [[ -z "$WEIR_BIN" ]]; then
  WEIR_BIN=$(mktemp -t weir-smoke.XXXXXX)
  trap 'rm -f "$WEIR_BIN"' EXIT
  echo "smoke: building weir into $WEIR_BIN"
  go build -ldflags="-s -w -X main.version=smoke" -o "$WEIR_BIN" .
fi

TMP_SETTINGS=$(mktemp -t weir-smoke-settings.XXXXXX.json)
trap 'rm -f "$TMP_SETTINGS" "$TMP_SETTINGS".weir-bak-*' EXIT
cat > "$TMP_SETTINGS" <<'JSON'
{
  "existing_unrelated_key": "keep me",
  "hooks": {
    "UserPromptSubmit": [
      {"matcher": "", "hooks": [{"type": "command", "command": "/other/tool.sh"}]}
    ]
  }
}
JSON

export CLAUDE_SETTINGS=$TMP_SETTINGS

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

# 1. status on never-installed
out=$("$WEIR_BIN" status 2>&1 || true)
echo "$out" | grep -q "0 weir-owned hook" || fail "step 1: expected 0 weir-owned hooks; got: $out"
pass "step 1: status reports not installed"

# 2. install
"$WEIR_BIN" install >/dev/null
n=$(jq '[.hooks // {} | to_entries[] | .value[]?.hooks[]? | select(.command? // "" | contains("'"$WEIR_BIN"'"))] | length' "$TMP_SETTINGS")
[[ "$n" == "2" ]] || fail "step 2: expected 2 weir hooks after install; got $n"
pass "step 2: install added both hooks"

# unrelated state preserved
[[ "$(jq -r .existing_unrelated_key "$TMP_SETTINGS")" == "keep me" ]] || fail "step 2a: unrelated key was clobbered"
[[ "$(jq -r '.hooks.UserPromptSubmit[0].hooks[0].command' "$TMP_SETTINGS")" == "/other/tool.sh" ]] || fail "step 2b: unrelated UserPromptSubmit hook was clobbered"
pass "step 2a/b: unrelated key + hook preserved"

# 3. status reports both
out=$("$WEIR_BIN" status 2>&1)
echo "$out" | grep -q "SessionStart -> $WEIR_BIN inject" || fail "step 3a: SessionStart not listed"
echo "$out" | grep -q "PreToolUse -> $WEIR_BIN suggest" || fail "step 3b: PreToolUse not listed"
pass "step 3: status lists both registered hooks"

# 4. idempotent install
out=$("$WEIR_BIN" install 2>&1)
echo "$out" | grep -q "already registered" || fail "step 4: re-install should report already-registered"
n=$(jq '[.hooks // {} | to_entries[] | .value[]?.hooks[]? | select(.command? // "" | contains("'"$WEIR_BIN"'"))] | length' "$TMP_SETTINGS")
[[ "$n" == "2" ]] || fail "step 4: count after re-install should still be 2; got $n"
pass "step 4: install is idempotent"

# 5. uninstall
"$WEIR_BIN" uninstall >/dev/null
n=$(jq '[.hooks // {} | to_entries[] | .value[]?.hooks[]? | select(.command? // "" | contains("'"$WEIR_BIN"'"))] | length' "$TMP_SETTINGS")
[[ "$n" == "0" ]] || fail "step 5: expected 0 weir hooks after uninstall; got $n"
pass "step 5: uninstall removed both"

# 6. unrelated state STILL preserved post-uninstall
[[ "$(jq -r .existing_unrelated_key "$TMP_SETTINGS")" == "keep me" ]] || fail "step 6: uninstall ate the unrelated key"
[[ "$(jq -r '.hooks.UserPromptSubmit[0].hooks[0].command' "$TMP_SETTINGS")" == "/other/tool.sh" ]] || fail "step 6: uninstall ate the unrelated UserPromptSubmit hook"
pass "step 6: unrelated state intact after uninstall"

# 7. review subcommand: block path emits BLOCKED text
out=$("$WEIR_BIN" review "which python3" 2>&1)
echo "$out" | grep -q "BLOCKED in production" || fail "step 7a: 'which python3' should be blocked"
out=$("$WEIR_BIN" review "command -v python3" 2>&1 || true)
echo "$out" | grep -q "clean" || fail "step 7b: 'command -v python3' should be clean"
pass "step 7: review correctly classifies block vs clean"

echo
echo "smoke: all 7 checks passed"
