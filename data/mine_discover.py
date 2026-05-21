#!/usr/bin/env python3
"""weir pass-B discovery — frequency-rank normalized command shapes to
surface antipattern candidates that pass A's a-priori list missed.

Three rankings emitted:
  1. pipeline_chains:    sequences of pipe-stage binaries (e.g. "grep|head",
                         "find|xargs|grep"). Re-derives the existing
                         top_template_N counts but excludes shapes already
                         covered by suggest.py's rules.
  2. single_stage_shapes: for non-piped commands, bucket by (binary,
                         normalized-flags). E.g. `for f in $(ls)` clusters
                         with all other `for f in $(ls X)` invocations.
  3. normalized_full:    full-command normalization (paths -> PATH,
                         strings -> STR, numbers -> N, vars -> VAR). Best
                         signal for structural inefficiencies independent
                         of arguments.

For each top entry: count + 3 sample real commands. Output goes to
/tmp/pipe_discover_report.json; this script also writes a stderr summary.

The intent is human eyeballing, not auto-promotion. The companion script
mine_extra.py is for HUNTING named patterns; this one is for FINDING
unnamed patterns.
"""
import json
import os
import re
import sys
import time
from collections import Counter, defaultdict
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from mine_pipes import iter_bash_commands, naive_pipe_split, first_binary  # noqa: E402


# Split a command on top-level statement separators (`;`, `&&`, `||`,
# `\n`), respecting single/double-quoted regions like naive_pipe_split.
# This was the key fix for pass B v0: without it, multi-statement commands
# trivially false-match chains like `head|tail` when they're really
# `cmd | head ; ...; cmd | tail` — two independent pipelines.
def naive_statement_split(cmd: str):
    parts = []
    buf = []
    in_s = False
    in_d = False
    i = 0
    n = len(cmd)
    while i < n:
        ch = cmd[i]
        if ch == "\\" and i + 1 < n:
            buf.append(ch)
            buf.append(cmd[i + 1])
            i += 2
            continue
        if ch == "'" and not in_d:
            in_s = not in_s
            buf.append(ch)
        elif ch == '"' and not in_s:
            in_d = not in_d
            buf.append(ch)
        elif not in_s and not in_d:
            # `&&` and `||` are two-char separators; check before `;` / `\n`
            if ch == "&" and i + 1 < n and cmd[i + 1] == "&":
                parts.append("".join(buf))
                buf = []
                i += 2
                continue
            if ch == "|" and i + 1 < n and cmd[i + 1] == "|":
                parts.append("".join(buf))
                buf = []
                i += 2
                continue
            if ch == ";" or ch == "\n":
                parts.append("".join(buf))
                buf = []
                i += 1
                continue
            buf.append(ch)
        else:
            buf.append(ch)
        i += 1
    parts.append("".join(buf))
    return [p for p in parts if p.strip()]

OUTPUT = "/tmp/pipe_discover_report.json"
SAMPLE_CAP = 3
TOP_N = 100

# Already-covered pipeline shapes (the 7+3 rules in bin/suggest.py).
# Skip these when surfacing pipeline_chain candidates so the eyeball
# pass doesn't re-discover what's already a rule.
COVERED_CHAINS = {
    "grep|head",
    "ls|grep",
    "grep|wc",
    "cat|grep", "cat|head", "cat|tail", "cat|sed", "cat|awk",
    "cat|jq", "cat|less", "cat|more", "cat|wc", "cat|sort", "cat|uniq",
    "cat|sd", "cat|mlr", "cat|bat", "cat|rg", "cat|fd", "cat|fzf",
    "find|xargs",   # covered by find-xargs-no-null
    "sort|uniq",
    "awk|awk",
}


# --- normalization helpers ----------------------------------------------

# Quoted strings (single or double)
RE_STRING = re.compile(r"""(?:'[^']*'|"(?:[^"\\]|\\.)*")""")
# Absolute or home-relative paths
RE_PATH = re.compile(r"""(?:~|\.)?/[A-Za-z0-9_./@+%-]+""")
# Numbers (standalone)
RE_NUMBER = re.compile(r"\b\d+\b")
# Shell var/expansion
RE_VAR = re.compile(r"\$\w+|\$\{[^}]+\}|\$\([^)]+\)|`[^`]+`")
# Flags — keep as-is for behavior but normalize trailing arg
# (we don't normalize flags themselves)


def normalize(cmd: str) -> str:
    """Aggressive normalization: collapse argument variance, keep structure.
    Conservative on flags / structural keywords / operators."""
    out = cmd
    out = RE_VAR.sub("VAR", out)
    out = RE_STRING.sub("STR", out)
    out = RE_PATH.sub("PATH", out)
    out = RE_NUMBER.sub("N", out)
    # collapse multiple spaces
    out = re.sub(r"\s+", " ", out).strip()
    return out


def flag_shape(stage: str) -> str:
    """For single-stage bucketing: <binary> <SORTED-FLAGS>. Drops args entirely.
    e.g. `grep -i -n foo file` -> 'grep -i -n'.
    """
    b = first_binary(stage)
    if not b:
        return ""
    # tokenize, keep tokens starting with '-' (but not '--' alone or numbers like -5)
    flags = []
    for tok in stage.split():
        if tok.startswith("-") and len(tok) > 1 and not tok[1].isdigit():
            # split combined short flags into individuals? keep combined for honesty
            flags.append(tok)
    flags.sort()
    return f"{b} {' '.join(flags)}".rstrip()


def main():
    t0 = time.time()
    chain_counts = Counter()
    chain_samples = defaultdict(list)

    single_counts = Counter()
    single_samples = defaultdict(list)

    norm_counts = Counter()
    norm_samples = defaultdict(list)

    n_total = 0
    n_skipped_covered = 0

    for _proj, _mb, cmd in iter_bash_commands():
        n_total += 1

        # Decompose into statements first, then analyze each statement's pipeline.
        # This is the v1 fix: without per-statement decomposition, chains like
        # `head|tail` from `cmd | head ; cmd | tail` (two independent statements
        # joined by `;`) get wrongly counted as a single pipeline.
        for stmt in naive_statement_split(cmd):
            stages = naive_pipe_split(stmt)
            if len(stages) >= 2:
                bins = [first_binary(s) for s in stages]
                bins = [b for b in bins if b]
                if len(bins) >= 2:
                    for i in range(len(bins) - 1):
                        pair = f"{bins[i]}|{bins[i + 1]}"
                        if pair in COVERED_CHAINS:
                            n_skipped_covered += 1
                            continue
                        chain_counts[pair] += 1
                        if len(chain_samples[pair]) < SAMPLE_CAP:
                            chain_samples[pair].append(stmt[:240])
                    if len(bins) >= 3:
                        full = "|".join(bins)
                        chain_counts[full] += 1
                        if len(chain_samples[full]) < SAMPLE_CAP:
                            chain_samples[full].append(stmt[:240])
            else:
                shape = flag_shape(stmt.strip())
                if shape:
                    single_counts[shape] += 1
                    if len(single_samples[shape]) < SAMPLE_CAP:
                        single_samples[shape].append(stmt[:240])

        # Full normalized form (truncate to keep memory bounded)
        n = normalize(cmd)
        if 5 < len(n) < 200:  # skip tiny noise and huge multi-line dumps
            norm_counts[n] += 1
            if len(norm_samples[n]) < SAMPLE_CAP:
                norm_samples[n].append(cmd[:240])

    def topify(counts, samples):
        return [
            {"shape": k, "count": v, "samples": samples[k]}
            for k, v in counts.most_common(TOP_N)
        ]

    report = {
        "meta": {
            "elapsed_seconds": round(time.time() - t0, 1),
            "bash_total": n_total,
            "covered_chain_skips": n_skipped_covered,
            "top_n": TOP_N,
            "note": "Discovery output — for human eyeballing, not auto-promotion. pipeline_chains excludes already-covered shapes. single_stage_shapes buckets by (binary, sorted-flags). normalized_full collapses paths/strings/numbers/vars.",
        },
        "pipeline_chains": topify(chain_counts, chain_samples),
        "single_stage_shapes": topify(single_counts, single_samples),
        "normalized_full": topify(norm_counts, norm_samples),
    }

    Path(OUTPUT).write_text(json.dumps(report, indent=2))
    sys.stderr.write(
        f"done in {report['meta']['elapsed_seconds']}s; "
        f"bash_total={n_total}; "
        f"top chains shown: {len(report['pipeline_chains'])}; "
        f"top single shapes: {len(report['single_stage_shapes'])}; "
        f"top normalized: {len(report['normalized_full'])}\n"
    )


if __name__ == "__main__":
    main()
