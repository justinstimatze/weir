#!/usr/bin/env python3
"""weir pass-A antipattern hunt.

Re-streams the same JSONL transcripts that `mine_pipes.py` reads, looking
for ten a-priori-suspect antipatterns that the v0 rule set doesn't yet
cover. Output is structured the same way as the existing baseline:
counts + 5-sample-per-pattern excerpts.

These are CANDIDATE rules — surfaced for review before being promoted
into `bin/suggest.py`.
"""
import json
import os
import re
import sys
import time
from collections import Counter, defaultdict

# Reuse the streaming generator from the existing mining script.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from mine_pipes import iter_bash_commands  # noqa: E402

OUTPUT = "/tmp/pipe_extra_report.json"

# --- candidate antipattern regexes (cheap, false-positive-tolerant) -----

# NOTE: All pipe-shape regexes use `[^|\n;&]` (NOT `[^|]`) for inter-token
# gaps so they can't span shell-statement separators (newline, `;`, `&&`).
# This was a real FP class in the first pass — multi-line commands like
# `... | head -20\necho '---'\n... | tail -10` were matching head|tail-range
# even though the two stages are independent statements.
PATTERNS = {
    # tail's f/F flag has to come immediately after `tail` (as its first arg
    # block); spans like `tail -8\n...\n... | grep "FAIL"` previously
    # false-matched because `-...full...` inside an echoed string contained `f`.
    "tail_f_grep_no_linebuf": {
        "re": re.compile(r"\btail\s+-(?:\S*[fF])\S*[^|\n;&]*\|\s*(?:grep|sed|awk)\b"),
        "linebuf_skip": re.compile(r"--line-buffered|\bstdbuf\b"),
        "fix": "`tail -f X | grep` buffers stdout in 4 KiB blocks — live tail won't appear until the buffer fills. Use `grep --line-buffered` (or `stdbuf -oL grep`) so matches flush per-line.",
    },
    "for_in_ls": {
        "re": re.compile(r"\bfor\s+\w+\s+in\s+\$\(\s*ls\b"),
        "fix": "`for f in $(ls DIR)` word-splits on whitespace and globs — breaks on filenames with spaces, tabs, newlines, or glob chars. Use `for f in DIR/*; do` (with `shopt -s nullglob` if the dir may be empty) or `find DIR -maxdepth 1 -print0 | while IFS= read -r -d '' f`.",
    },
    "for_in_find": {
        "re": re.compile(r"\bfor\s+\w+\s+in\s+\$\(\s*find\b"),
        "fix": "`for f in $(find …)` has the same word-splitting hazard as `for f in $(ls …)`. Use `find … -print0 | while IFS= read -r -d '' f`, or `find … -exec CMD {} +` if the loop body is one command.",
    },
    "find_xargs_no_null": {
        # find ... | xargs ... on the same statement, no -print0 / -0 in that statement
        "re": re.compile(r"\bfind\b[^|\n;&]*\|\s*xargs\b[^\n;&]*"),
        "null_skip": re.compile(r"-print0|\bxargs\b[^\n;&]*\s-0\b"),
        "fix": "`find … | xargs CMD` splits on whitespace — filenames with spaces or newlines get mangled. Use `find … -print0 | xargs -0 CMD`, or `find … -exec CMD {} +`.",
    },
    "useless_echo_subst": {
        # echo $(...) or echo `...` at the start of a statement (boundary: ^, ;, &&, ||, or newline)
        "re": re.compile(r"(?:^|[;&\n]\s*|&&\s*|\|\|\s*)echo\s+\"?(?:\$\(|`)"),
        "fix": "`echo $(CMD)` just runs CMD and re-prints its stdout — the wrapping `echo` is no-op and discards CMD's exit code. Drop the echo.",
    },
    "which_instead_of_command_v": {
        # require `which` at start of a statement to avoid matching it inside
        # quoted strings or English prose passed as a tool argument.
        "re": re.compile(r"(?:^|[;&\n]\s*|&&\s*|\|\|\s*|\|\s*)which\s+[A-Za-z_][\w.-]*"),
        "fix": "`which` is non-POSIX with inconsistent cross-distro behavior — can't see shell functions/aliases, exit codes vary. `command -v CMD` is POSIX, sees functions/aliases, exits non-zero cleanly on miss.",
    },
    "ls_pipe_wc_l": {
        "re": re.compile(r"\bls\b[^|\n;&]*\|\s*wc\s+-l\b"),
        "fix": "`ls | wc -l` overcounts when filenames contain newlines. For a directory entry count: `find DIR -mindepth 1 -maxdepth 1 -printf '.\\n' | wc -l`, or with `shopt -s nullglob dotglob`: `arr=(DIR/*); echo ${#arr[@]}`.",
    },
    "redir_order_inverted": {
        # `... 2>&1 ... >file` inside a single statement, no pipe between
        "re": re.compile(r"\b2>&1\b[^|\n;&]*?\s>(?!\&)\S"),
        "fix": "`CMD 2>&1 >FILE` does NOT send stderr to FILE — it dup's stderr to original stdout, THEN redirects stdout to FILE. Stderr still goes to the terminal. Correct order: `CMD >FILE 2>&1` (or `CMD &>FILE` in bash).",
    },
    "kill_pgrep": {
        "re": re.compile(r"\bkill\b[^|;&\n]*\$\(\s*pgrep\b|\bkill\b[^|;&\n]*`pgrep\b"),
        "fix": "`kill $(pgrep -f X)` has a TOCTOU race and fails noisily if pgrep returns empty (kill with no args). Use `pkill -f X` — atomic, handles empty matches cleanly.",
    },
    "head_pipe_tail_range": {
        "re": re.compile(r"\bhead\b[^|\n;&]*?-n?\s*\d+[^|\n;&]*\|\s*tail\b[^|\n;&]*?-n?\s*\d+"),
        "fix": "`head -n N FILE | tail -n M` to extract a line range reads to line N before discarding the prefix. `sed -n 'START,ENDp' FILE` or `awk 'NR>=START && NR<=END' FILE` reads only what's needed and exits at END.",
    },
}

SAMPLE_CAP = 5


def main():
    t0 = time.time()
    counts = Counter()
    samples = defaultdict(list)
    per_project = defaultdict(Counter)
    n_total = 0

    SKIP_KEYS = ("linebuf_skip", "null_skip")
    for proj, _mb, cmd in iter_bash_commands():
        n_total += 1
        for name, spec in PATTERNS.items():
            if not spec["re"].search(cmd):
                continue
            # If an antidote pattern is also present, suppress (e.g. tail -f | grep
            # IS used responsibly when --line-buffered or stdbuf is also on the line).
            skip = next((spec[k] for k in SKIP_KEYS if k in spec), None)
            if skip and skip.search(cmd):
                continue
            counts[name] += 1
            per_project[name][proj] += 1
            if len(samples[name]) < SAMPLE_CAP:
                samples[name].append(cmd[:240])

    report = {
        "meta": {
            "elapsed_seconds": round(time.time() - t0, 1),
            "bash_total": n_total,
            "note": "Candidate antipatterns hunted by data/mine_extra.py — NOT yet promoted to bin/suggest.py rules. Regexes are cheap heuristics; verify samples before promoting.",
        },
        "candidates": {
            name: {
                "count": counts[name],
                "rate_per_1000": round(1000.0 * counts[name] / max(1, n_total), 2),
                "fix": PATTERNS[name]["fix"],
                "top_projects": per_project[name].most_common(5),
                "samples": samples[name],
            }
            for name in sorted(PATTERNS, key=lambda k: -counts[k])
        },
    }

    with open(OUTPUT, "w") as f:
        json.dump(report, f, indent=2)

    # also emit a stderr summary
    sys.stderr.write(f"done in {report['meta']['elapsed_seconds']}s; bash_total={n_total}\n")
    sys.stderr.write(f"counts: {dict(counts.most_common())}\n")


if __name__ == "__main__":
    main()
