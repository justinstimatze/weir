package suggest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Positive cases: each Cmd MUST match the named Rule (it may also match others).
// Corpus is mirrored from the historical Python prototype; kept here because
// every fixture is a real-world command we've observed (or a tight FP variant).
var positives = []struct {
	Cmd  string
	Want string
}{
	{"grep -i error /tmp/log | head -10", "grep-head-trim"},
	{"ls /tmp | grep .log", "ls-grep"},
	{"grep -i error /tmp/log | wc -l", "grep-wc"},
	{"ps aux | grep transcribe | grep -v grep | wc -l", "grep-wc"},
	{"cat /etc/hosts | grep localhost", "uuoc"},
	{"cat benchmark/results/summary.md | head -200", "uuoc"},
	{`find . -name '*.py' -exec wc -l {} \;`, "find-exec-semi"},
	{"sort file.txt | uniq", "sort-uniq"},
	{"awk '{print $1}' file | awk '{print $2}'", "awk-awk"},
	{"python3 deploy/eval.py 2>/tmp/eval-stderr2.log; echo '---'; cat /tmp/eval-stderr2.log | grep -i error | head -10", "uuoc"},
	{"which python3", "which-vs-command-v"},
	{"which python3 && python3 -V", "which-vs-command-v"},
	{"ls /tmp/foo && which convert", "which-vs-command-v"},
	{"ls /home/x/captions | wc -l", "ls-pipe-wc-l"},
	{"find . -name '*.go' | xargs grep -l 'foo'", "find-xargs-no-null"},
	// Promoted from pass B:
	{"head -n 100 file.log | tail -n 10", "head-tail-range"},
	{"head -100 file.log | tail -10", "head-tail-range"},
	{"head -n 1000 /var/log/app.log | tail -n 50", "head-tail-range"},
	{"ps aux | grep transcribe", "ps-grep-vs-pgrep"},
	{"ps -ef | grep python", "ps-grep-vs-pgrep"},
	{"ps aux | grep -E 'foo|bar' | grep -v grep", "ps-grep-vs-pgrep"},
	// git staging guard — broad forms that sweep in untracked files.
	{"git add -A", "git-add-all"},
	{"git add --all", "git-add-all"},
	{"git add .", "git-add-all"},
	{"git add ./", "git-add-all"},
	{"git add -A && git commit -m x", "git-add-all"},
	{"git add -v .", "git-add-all"},
	// rg -r misfire — silent data corruption from grep -rn muscle memory.
	// v0.1.4 split: bundled `-r[nliwcv]` blocks (nearly zero legit use);
	// separated `-r X` (single-letter, '', or "") advises (legit but rare).
	{"rg -rn CRON_SECRET /tmp/t.txt", "rg-r-misfire-bundled"},
	{"rg -rn PATTERN .", "rg-r-misfire-bundled"},
	{"rg -rl PATTERN /tmp", "rg-r-misfire-bundled"},
	{"rg -ri PATTERN src/", "rg-r-misfire-bundled"},
	{"rg -r n /tmp/t.txt", "rg-r-misfire"},
	{"rg -n 'foo' file -r ''", "rg-r-misfire"}, // aipotluck's variant
	{`rg -n 'foo' file -r ""`, "rg-r-misfire"}, // same trap, double-quoted
	// sd in-place trap — v0.1.5, reported by cope-1184525 after
	// `sd '=.*' '=<set>' .env` destroyed a live ANTHROPIC_API_KEY.
	// Base advisory: any 3-arg sd on a file, no -p/--preview.
	{"sd 'foo' 'bar' file.txt", "sd-in-place-write"},
	{"sd '=.*' '=whatever' /tmp/x.conf", "sd-in-place-write"},
	{`sd "foo" "bar" myfile`, "sd-in-place-write"},
	{"sd 'x' 'y' path/to/file", "sd-in-place-write"},
	// Secret-file escalation: block on .env / .pem / .key / credentials.
	{"sd '=.*' '=<set>' .env", "sd-in-place-write-secret-file"}, // cope's exact command
	{"sd 'foo' 'bar' /etc/ssl/server.pem", "sd-in-place-write-secret-file"},
	{"sd 'x' 'y' ~/.ssh/id_rsa.key", "sd-in-place-write-secret-file"},
	{"sd 'x' 'y' /path/to/credentials.json", "sd-in-place-write-secret-file"},
	{"sd 'x' 'y' cert.p12", "sd-in-place-write-secret-file"},
	// Redaction-shaped replacement: block regardless of file name.
	{"sd 'apikey=.*' 'apikey=<redacted>' cfg.yaml", "sd-in-place-write-redaction"},
	{"sd 'pw=.*' 'pw=***' notes.md", "sd-in-place-write-redaction"},
	{"sd 'secret=.*' 'secret=REDACTED' log.txt", "sd-in-place-write-redaction"},
	// Empty replacement: block regardless of file name. `sd PAT '' FILE` deletes.
	{`sd '^---[\s\S]*?---' '' "$f"`, "sd-in-place-write-empty"}, // the aipotluck command, verbatim
	{"sd 'foo' '' notes.md", "sd-in-place-write-empty"},
	{`sd "<think>.*</think>" "" transcript.txt`, "sd-in-place-write-empty"},
	{"sd '^#.*$' '' src/main.go", "sd-in-place-write-empty"},
	// sd's replacement has its own $ grammar — v0.1.7, reported from twip
	// after `'BIN="${TWIP_BIN:-$HERE/…}"'` wrote `BIN=""` with exit 0.
	// Fires on BOTH positional forms; the pipe form corrupts just as badly.
	{`sd 'BIN=".*"' 'BIN="${TWIP_BIN:-$HERE/target/release/twip}"' run.sh`, "sd-replacement-shell-var"},
	{`printf 'x\n' | sd 'x' 'a$HERE/b'`, "sd-replacement-shell-var"},
	{`sd 'foo' 'user-$USERNAME' notes.md`, "sd-replacement-shell-var"},
	{`cat f | sd '(\w+)' 'got:$name'`, "sd-replacement-shell-var"},
	// Second sighting, v0.1.8 — reported from a sibling session
	// (aipotluck.org): the rule fired via the single-quoted branch by
	// coincidence (a double-quoted TS import string's own literal
	// './$mod' quotes satisfied it), was advisory, and the corruption
	// landed anyway in source, not a shell script — syntactically valid
	// TypeScript with a silently truncated import path, no runtime to
	// fail fast. The double-quoted branch below makes this fire for the
	// actual reason instead of a lucky accident of the source text.
	{`sd "import \{ A, B \} from './\\\$mod';" "import { B } from './\$mod';" file.ts`, "sd-replacement-shell-var"},
	// Clean double-quoted, backslash-escaped $, with no stray single
	// quotes anywhere — this is the case the coincidence above was
	// masking. Before the double-quoted branch existed, this did not
	// fire at all: shWord's '"[^"]*"' word never contains a bare `'`,
	// so nothing satisfied the single-quoted branch's `'...'` requirement.
	{`sd 'x' "a\$HERE/b" file.txt`, "sd-replacement-shell-var"},
	// No capture group anywhere in the pattern ('x' matches literal text,
	// zero groups) — the narrower escalation should ALSO fire on this
	// exact command, on top of the base advisory above.
	{`sd 'x' "a\$HERE/b" file.txt`, "sd-replacement-shell-var-no-capture-group"},
	// Same shape, but the pipe form — escalation isn't gated on a file
	// operand any more than the base rule is.
	{`printf 'x\n' | sd 'x' 'a$HERE/b'`, "sd-replacement-shell-var-no-capture-group"},
	// pgrep -f matches the bash -c shell running the command, so a wait
	// loop never exits — v0.1.7, reported from lexicon with two orphaned
	// loops days old. Gated on the loop/kill pairing.
	{`until ! pgrep -f "scripts/probe-absence.py" >/dev/null; do sleep 10; done`, "pgrep-f-self-match"},
	{`while pgrep -f adjudicate-spans >/dev/null; do sleep 20; done`, "pgrep-f-self-match"},
	{`pgrep -af "until ! pgrep" | rg -o "^[0-9]+" | while read p; do kill $p; done`, "pgrep-f-self-match"},
	{`pgrep --full myjob | xargs kill`, "pgrep-f-self-match"},
	// pkill -f is ungated: there is no safe one-shot form.
	{`pkill -f fetch-zim-article`, "pkill-f-self-match"},
	{`pkill -9 -f 'python worker.py'`, "pkill-f-self-match"},
	// rg -h is --help — v0.1.7. Bundle containing h, or -h before a pattern.
	{`rg -oh 'https?://[^ ]+' a.md b.md`, "rg-h-is-help"},
	{`rg -nh PATTERN src/`, "rg-h-is-help"},
	{`rg -h 'https?://' notes.md`, "rg-h-is-help"},
	// rg -L is --follow, not grep's --files-without-match. Found by the
	// flag-overlap boundary sweep (data/flag_overlap.py), not an incident.
	{`rg -L 'TODO' src/`, "rg-cap-l-misfire"},
	{`rg -inL PATTERN dir/`, "rg-cap-l-misfire"},
	// rg/fd honour .gitignore with no git in view — v0.1.7. Gated on a
	// dot-path argument (28.5% precision vs 6.4% for the flag-absence gate).
	{`rg -c 'EPISTEMIC CHECK' ~/.claude/projects/`, "rg-ignore-file-hides-target"},
	{`rg 'marker' "$HOME/.claude/projects"`, "rg-ignore-file-hides-target"},
	{`fd -e jsonl . ~/.claude/projects`, "rg-ignore-file-hides-target"},
	{`rg TODO .config/nvim/lua`, "rg-ignore-file-hides-target"},
	// A pipeline reports its LAST stage's status — v0.1.7, three sightings.
	// Scoped to publish/install verbs; see the stateChanging comment for
	// why the build and test verbs were measured out of the list.
	{"git push origin develop 2>&1 | tail -5", "pipe-eats-exit-status"},
	{"go install github.com/justinstimatze/defn@main 2>&1 | tail -10", "pipe-eats-exit-status"},
	{"pip install -r requirements.txt 2>&1 | tail -20", "pipe-eats-exit-status"},
	{"rsync -a src/ host:/dest 2>&1 | grep -i error", "pipe-eats-exit-status"},
	// The sharper variant: an EXPLICIT `echo $?` that reads the filter's status.
	{"go test ./... -count=1 | rg -v '^ok '\necho \"GOTEST_EXIT=$?\"", "pipe-status-echo"},
	{"go install ./... 2>&1 | tail -10\necho \"EXIT=$?\"", "pipe-status-echo"},
	{"cmd | wc -l; echo $?", "pipe-status-echo"},
	// sponge commits an empty stream when the left side fails — v0.1.7,
	// reported by documents-66700 after this exact command destroyed an
	// auto-memory index. Verified: `false | sponge m.md` -> 0 bytes, exit 0.
	{`grep -vxF "$LINE" "$M" | sponge "$M"`, "sponge-eats-failed-pipeline"},
	{`jq 'del(.foo)' cfg.json | sponge cfg.json`, "sponge-eats-failed-pipeline"},
	{`sort -u list.txt | sponge list.txt`, "sponge-eats-failed-pipeline"},
	// A quoted pattern starting with `-` parses as an option bundle.
	{`grep -vxF "- [Leave it]" MEMORY.md`, "grep-dash-pattern"},
	{`rg foo x | grep "--color"`, "grep-dash-pattern"},
	// All four of these were measured against ugrep 7.5.0 and GNU grep.
	// The last is the quiet one: `-v` is a VALID flag, so grep inverts the
	// match rather than erroring — exit 1, no message, reads as no matches.
	{`cargo test 2>&1 | grep -nE '--- FAIL|^FAIL'`, "grep-dash-pattern"},
	{`go test ./... | grep -E "->"`, "grep-dash-pattern"},
	{`grep "- id:" catalog.yaml`, "grep-dash-pattern"},
	{`grep -E '-v' notes.md`, "grep-dash-pattern"},
	// A leading-dash FILE operand fails identically and takes the same fix.
	{`grep -ao 'tokens[0-9]*' "-home-x-Documents-buddy/abc.jsonl"`, "grep-dash-pattern"},
}

// Negative cases: each MUST match NO rules.
var negatives = []string{
	"grep -m 10 -i error /tmp/log",
	"ls /tmp/*.log",
	"grep -c -i error /tmp/log",
	"grep -i error /tmp/log",
	`find . -name '*.py' -exec wc -l {} +`,
	"sort file.txt | uniq -c",
	"sort file.txt | uniq -d",
	"awk '{print}' file",
	"cat /etc/hosts",
	"cat /etc/hosts > /tmp/copy",
	"echo hello",
	"git log --oneline | head -20",
	// git staging guard negatives — explicit paths and tracked-only must NOT block.
	"git add server.py",
	"git add foo.py bar.py",
	"git add src/ docs/",
	"git add ./foo",
	"git add -u",
	"git add -p",
	"command -v python3",
	"bmg describe -intent 'assess which mode the lens used'",
	`echo 'which is best' > /tmp/x`,
	`find DIR -mindepth 1 -maxdepth 1 -printf '.\n' | wc -l`,
	`find . -name '*.go' -print0 | xargs -0 grep -l 'foo'`,
	`find . -name '*.go' -exec grep -l 'foo' {} +`,
	// head-tail negatives — should NOT fire across statements or without -n on both
	`cmd | head ; echo --- ; cmd | tail -10`,                  // two separate statements
	`rg -n foo src 2>&1 | head -20; rg -n bar src | tail -25`, // separate, semicolon
	// Trimming a build or test target is a READ, not the status trap:
	// the output is right there and the author is looking at it. This was
	// 15,571 of the 15,571-fire first draft.
	`make check 2>&1 | tail -15`,
	`go build ./... 2>&1 | head -20; go test ./... | tail -25`,
	`npm run build 2>&1 | grep -E 'error'`,
	`head file.txt | tail`, // no numeric args on either
	// ps-grep negatives
	`pgrep -af python`,  // already using pgrep
	`ps aux | head -10`, // no grep
	`ps -p 1234`,        // ps without pipe
	// uuoc negatives — surfaced by the eval (mlr's `cat` verb was being
	// false-matched as coreutils cat; would have blocked legitimate mlr usage)
	`mlr --c2p cat /tmp/sales.csv | less -S`,
	`mlr --c2p cat /tmp/sales.csv | bat`,
	`awk cat /tmp/x.txt`, // hypothetical, but exercises the boundary
	// rg -r misfire negatives — the trap only fires on bare single-letter
	// replacements that match a common rg short flag; ordinary rg usage
	// and legitimate replacements must NOT match.
	`rg -n PATTERN file`,           // no -r
	`rg PATTERN file`,              // no flags at all
	`rg -r 'foo bar' file`,         // legit multi-char replacement, quoted
	`rg -r foo file`,               // legit multi-char replacement, unquoted
	`rg -r 'n' file`,               // legit single-letter replacement, quoted (advisory can't inspect quotes; pattern excludes because next char after -r space is ')
	`rg -e PATTERN -r replacement`, // legit long replacement
	// v0.1.4 heredoc suppression — block-mode rules must NOT fire on
	// prose sitting inside a heredoc body passed via `git commit -F -`.
	"git commit -F - <<'EOF'\nfixed the bug which affected auth\nwhich the fixture asserted\nEOF",
	"git commit -F - <<EOF\ncat the config file to see the value\nEOF",
	"git commit -F - <<-EOF\n\twhich fires the retry\nEOF", // dedented form
	// sd in-place negatives — safe shapes must NOT fire any of the four rules.
	`sd -p '=.*' '=<set>' .env`,         // preview — the safe form for cope's case
	`sd --preview 'foo' 'bar' file.txt`, // long-form preview
	`cat file.txt | sd 'foo' 'bar'`,     // stdin form, writes stdout
	`echo hi | sd 'x' 'y'`,              // stdin form, 2 args only
	`sd 'foo' 'bar'`,                    // 2 args, reads stdin
	`sd --help`,                         // help flag
	// Empty-replacement negatives. The escalation must leave the two safe ways
	// to delete-for-display alone, or it just teaches people to work around it.
	`cat page.mdoc | sd '^---[\s\S]*?---' ''`, // the fix for the command that caused it
	`sd -p 'foo' '' notes.md`,                 // preview an in-place deletion
	`sd 'foo' ''`,                             // 2 args, reads stdin
	// v0.1.6 FP fixes. The first three are the shape the field named as
	// five of nine false positives in one session: the pipe form, which is
	// the remedy the sd block rules recommend. The last two are `\bsd\b`
	// matching inside an identifier — the second fired while its own
	// report was being written.
	`curl -sL https://example.com | sd '<[^>]+>' '' | head -40`,
	`curl -sL https://example.com | sd '<[^>]+>' '' > out.html`,
	`cat notes.md | sd 'foo' 'bar' | head -20`,
	`rg -n "sd-in-place-write" -A18 internal/suggest/rules.go`,
	`WEIR_SAMPLE_RULE=sd-in-place-write go test ./internal/measure -v`,
	// pgrep/pkill negatives. A one-shot `pgrep -f` LOOK costs one extra
	// line of output; only the loop/kill pairing is fatal. Name matching
	// (no -f) cannot self-match at all.
	`pgrep -af python`,
	`pgrep -f 'python worker.py'`,
	`until ! pgrep myjob; do sleep 5; done`,
	`pkill myjob`,
	`pkill -HUP nginx`,
	// rg -h negatives. A deliberate help lookup must stay silent, and so
	// must the flags that only look like the trap.
	`rg -h`,
	`rg --help`,
	`rg --hidden PATTERN src/`,
	`rg -H PATTERN src/`, // -H is --with-filename, not the trap
	`rg -h | head -40`,
	`rg -h 2>&1`,
	// rg-ignore negatives. The rule gates on a dot-DIRECTORY in a path, so
	// relative paths and quoted dot-patterns stay silent, and naming an
	// unrestricted flag suppresses it.
	`rg PATTERN ./src`,
	`rg PATTERN ../lib`,
	`rg '\.env' .`,
	// Glob exclusions name a dot-directory in order to SKIP it. The first
	// corpus sweep found these were most of the rule's false positives.
	`rg -il -g '!**/.venv/**' -g '!**/.git/**' stripe src`,
	`fd -t f -E '.git' -E '.venv' . src`,
	// An explicitly-named dot FILE is read regardless of ignore rules —
	// rg only applies them while descending. Verified on a scratch tree
	// with a deny-by-default gitignore: `rg NEEDLE .env` returns the
	// match, `rg NEEDLE sub` returns nothing.
	`rg -n "env" frontend/.gitignore`,
	`rg -oN 'phc_[A-Za-z0-9]+' frontend/.env.production.local`,
	`rg -n 'node' .github/workflows/ci.yml`,
	// Extensionless files under .git are the residual case no shape can
	// separate from a directory — `pre-commit` looks exactly like `hooks`.
	`rg -n "calque check --exclude" .git/hooks/pre-commit`,
	`rg -n 'url' .git/config`,
	`rg -uu 'marker' ~/.claude/projects`,
	`rg --no-ignore 'marker' ~/.claude`,
	`fd -HI . ~/.claude/projects`,
	// pipeline-status negatives. Read-only commands lose nothing by
	// discarding a status, and both guards suppress.
	`ls -la | tail -5`,
	`set -o pipefail; git push origin main 2>&1 | tail -5`,
	`go test ./... | tail -20; echo "${PIPESTATUS[0]}"`,
	// grep-dash-pattern negatives. `--` and `-e` both say the author knows
	// the operand is data, and an UNQUOTED leading dash is a flag.
	`grep -vxF -- "- [Leave it]" MEMORY.md`,
	`grep -e "-v" notes.md`,
	`grep -vxF "plain text" MEMORY.md`,
	// cmdPos keeps `\bgrep\b` out of a long flag that ends in it.
	`git log --grep "-fix" --oneline`,
	// sponge without a pipe writes nothing and cannot truncate.
	`sponge --help`,
}

// Rule-scoped negatives: the command may legitimately fire OTHER rules,
// but must not fire Rule. The plain `negatives` table above can only say
// "nothing at all fires", which cannot express the case that matters most
// for precision work — a command that is a true positive for one rule and
// a false positive for its neighbour. `sd 'x' 'got:$1' file.txt` really is
// an in-place write and really is not a bad capture-group reference.
var ruleNegatives = []struct {
	Cmd  string
	Rule string
}{
	// Double-quoted and bare `$NAME` are expanded by the SHELL before sd
	// sees them — the documented way to interpolate. `$$` is sd's
	// literal-dollar escape, `$1` is a numeric group ref, and a defined
	// named group is the legitimate use of the feature.
	{`sd 'x' "a$HERE/b" file.txt`, "sd-replacement-shell-var"},
	{`sd 'x' 'a$$HERE/b' file.txt`, "sd-replacement-shell-var"},
	{`sd '(x)' 'got:$1' file.txt`, "sd-replacement-shell-var"},
	{`sd '(x)' 'got:${1}' file.txt`, "sd-replacement-shell-var"},
	{`sd '(?P<thing>x)' 'got:$thing' file.txt`, "sd-replacement-shell-var"},
	{`sd '(?<thing>x)' 'got:$thing' file.txt`, "sd-replacement-shell-var"},
	// Escalation is narrower than the base rule: a PLAIN unnamed group's
	// mere presence suppresses it too, even though `$name` doesn't
	// actually address that group by number — the base rule (positive
	// fixture above, line 84) stays advisory for this shape on purpose.
	{`cat f | sd '(\w+)' 'got:$name'`, "sd-replacement-shell-var-no-capture-group"},
	// Named/numbered-group cases that suppress or exempt the base rule
	// must not somehow trip the escalation either.
	{`sd '(x)' 'got:$1' file.txt`, "sd-replacement-shell-var-no-capture-group"},
	{`sd '(?P<thing>x)' 'got:$thing' file.txt`, "sd-replacement-shell-var-no-capture-group"},
	// uuoc owns this one; the pipeline rules must stay out of it because
	// `cat` changes no state and losing its status costs nothing.
	{`cat access.log | rg ERROR`, "pipe-eats-exit-status"},
	{`cat access.log | rg ERROR`, "pipe-status-echo"},
	// A dot-path with an unrestricted flag present is handled, but the
	// in-place sd rules must not read a search path as a file operand.
	{`rg 'marker' ~/.claude/projects`, "sd-in-place-write"},
}

func names(rs []Rule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func TestPositives(t *testing.T) {
	for _, p := range positives {
		got := names(Match(p.Cmd))
		if !contains(got, p.Want) {
			t.Errorf("positive: %q\n  expected %q in matches; got %v", p.Cmd, p.Want, got)
		}
	}
}

func TestNegatives(t *testing.T) {
	for _, n := range negatives {
		got := names(Match(n))
		if len(got) > 0 {
			t.Errorf("negative: %q\n  expected no matches; got %v", n, got)
		}
	}
}

func TestRuleNegatives(t *testing.T) {
	for _, n := range ruleNegatives {
		got := names(Match(n.Cmd))
		if contains(got, n.Rule) {
			t.Errorf("rule-negative: %q\n  expected %q NOT to fire; got %v", n.Cmd, n.Rule, got)
		}
	}
}

// TestEveryRuleHasAPositive keeps the corpus honest: a rule with no
// fixture is a rule nobody has checked fires at all, and the sd family
// showed how quietly a pattern can rot into matching the wrong thing.
func TestEveryRuleHasAPositive(t *testing.T) {
	covered := make(map[string]bool, len(positives))
	for _, p := range positives {
		covered[p.Want] = true
	}
	for _, r := range Rules {
		if !covered[r.Name] {
			t.Errorf("rule %q has no positive fixture", r.Name)
		}
	}
}

// TestRunBlockingPath: a blocked rule (which-vs-command-v) emits deny.
func TestRunBlockingPath(t *testing.T) {
	in := bytes.NewBufferString(`{"tool_name":"Bash","tool_input":{"command":"which python3"}}`)
	var out bytes.Buffer
	if code := Run(nil, in, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if out.Len() == 0 {
		t.Fatal("expected hook output, got empty")
	}
	var got blockOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if got.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("expected deny; got %q", got.HookSpecificOutput.PermissionDecision)
	}
	if !strings.Contains(got.HookSpecificOutput.PermissionDecisionReason, "which-vs-command-v") {
		t.Errorf("expected rule name in reason; got: %q", got.HookSpecificOutput.PermissionDecisionReason)
	}
}

// TestRunAdvisoryPath: a non-blocked rule (head-tail-range) emits additionalContext.
func TestRunAdvisoryPath(t *testing.T) {
	in := bytes.NewBufferString(`{"tool_name":"Bash","tool_input":{"command":"head -n 100 file | tail -n 10"}}`)
	var out bytes.Buffer
	if code := Run(nil, in, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if out.Len() == 0 {
		t.Fatal("expected hook output, got empty")
	}
	var got adviseOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if got.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("wrong hookEventName: %q", got.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(got.HookSpecificOutput.AdditionalContext, "head-tail-range") {
		t.Errorf("expected head-tail-range in context; got: %q", got.HookSpecificOutput.AdditionalContext)
	}
}

// Non-Bash tool calls should produce no output.
func TestRunSkipsNonBash(t *testing.T) {
	in := bytes.NewBufferString(`{"tool_name":"Read","tool_input":{"command":"which python3"}}`)
	var out bytes.Buffer
	Run(nil, in, &out)
	if out.Len() != 0 {
		t.Errorf("expected no output for non-Bash tool; got %q", out.String())
	}
}

// Malformed input should fail-open silently.
func TestRunFailOpen(t *testing.T) {
	in := bytes.NewBufferString(`not json at all`)
	var out bytes.Buffer
	if code := Run(nil, in, &out); code != 0 {
		t.Errorf("expected exit 0 on bad input; got %d", code)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output on bad input; got %q", out.String())
	}
}
