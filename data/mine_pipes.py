#!/usr/bin/env python3
"""Mine Claude Code session transcripts for shell pipeline patterns."""
import json
import os
import re
import sys
import glob
import time
from collections import Counter, defaultdict
from datetime import datetime, timezone

PROJECTS_GLOB = os.path.expanduser("~/.claude/projects/*/*.jsonl")
SUBAGENT_GLOB = os.path.expanduser("~/.claude/projects/*/subagents/*.jsonl")

OUTPUT = "/tmp/pipe_report.json"


def naive_pipe_split(cmd: str):
    """Split a command on top-level pipes, ignoring pipes inside quoted strings.
    Heuristic only; ignores nested escapes."""
    parts = []
    buf = []
    in_s = False  # single quote
    in_d = False  # double quote
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
        elif ch == "|" and not in_s and not in_d:
            # check for ||
            if i + 1 < n and cmd[i + 1] == "|":
                buf.append("||")
                i += 2
                continue
            parts.append("".join(buf))
            buf = []
        else:
            buf.append(ch)
        i += 1
    parts.append("".join(buf))
    return parts


def first_binary(stage: str) -> str:
    s = stage.strip()
    # strip leading parens/braces/dollar/!
    s = s.lstrip("(){}$! \t")
    # also strip common shell prefixes like "sudo", "time", "env VAR=x", "command"
    # actually keep things simple — first whitespace token
    if not s:
        return ""
    # strip leading subshell artifacts
    while s.startswith(("(", "{", "$")):
        s = s[1:].lstrip()
    # handle redirections at the very start (rare)
    m = re.match(r"([A-Za-z0-9_./+\-]+)", s)
    if not m:
        return s.split()[0] if s.split() else ""
    tok = m.group(1)
    # if it's a var-assignment (FOO=bar cmd), look ahead
    if "=" in tok and not tok.startswith("="):
        rest = s[m.end():].lstrip()
        m2 = re.match(r"([A-Za-z0-9_./+\-]+)", rest)
        if m2:
            return m2.group(1)
    # if it's a wrapper like sudo/time/env/command/exec/nohup, look at next
    wrappers = {"sudo", "time", "env", "command", "exec", "nohup", "stdbuf",
                "ionice", "nice", "timeout", "xargs"}
    if tok in wrappers:
        rest = s[m.end():].lstrip()
        # skip flags
        while rest.startswith("-"):
            sp = rest.find(" ")
            if sp == -1:
                break
            rest = rest[sp + 1:].lstrip()
        m2 = re.match(r"([A-Za-z0-9_./+\-]+)", rest)
        if m2:
            return m2.group(1)
    # strip path prefix
    if "/" in tok:
        tok = tok.rsplit("/", 1)[-1]
    return tok


# ---- antipattern regexes (cheap, false-positive-tolerant) ----
RE_CAT_GREP   = re.compile(r"\bcat\s+[^\|;&<>]+\|\s*grep\b")
RE_CAT_HEAD   = re.compile(r"\bcat\s+[^\|;&<>]+\|\s*head\b")
RE_CAT_TAIL   = re.compile(r"\bcat\s+[^\|;&<>]+\|\s*tail\b")
RE_CAT_WC     = re.compile(r"\bcat\s+[^\|;&<>]+\|\s*wc\b")
RE_CAT_JQ     = re.compile(r"\bcat\s+[^\|;&<>]+\|\s*jq\b")
RE_GREP_WC    = re.compile(r"\bgrep\b[^|]*\|\s*wc\s+-l\b")
RE_GREP_HEAD  = re.compile(r"\bgrep\b[^|]*\|\s*head\b")
RE_SORT_UNIQ  = re.compile(r"\bsort\b[^|]*\|\s*uniq\b(?!\s*-c)")
RE_SORT_UNIQ_C = re.compile(r"\bsort\b[^|]*\|\s*uniq\s+-c\b")
RE_FIND_XARGS_GREP = re.compile(r"\bfind\b[^|]*\|\s*xargs\b[^|]*\bgrep\b")
RE_FIND_EXEC_SEMI = re.compile(r"\bfind\b[^|]*-exec\b[^|]*\\;")
RE_FIND_EXEC_PLUS = re.compile(r"\bfind\b[^|]*-exec\b[^|]*\+")
RE_LS_GREP    = re.compile(r"\bls\b[^|]*\|\s*grep\b")
RE_AWK_AWK    = re.compile(r"\bawk\b[^|]*\|[^|]*\bawk\b")
RE_SED_SED    = re.compile(r"\bsed\b[^|]*\|[^|]*\bsed\b")
RE_REDIR_TEE  = re.compile(r"2>&1\s*\|\s*tee\b")

# jq filter capture: jq '<filter>' or jq "<filter>" or jq <unquoted>
RE_JQ_FILTER = re.compile(r"\bjq\s+(?:-[a-zA-Z]+\s+)*('([^']*)'|\"((?:[^\"\\]|\\.)*)\"|([^\s|]+))")
# jq with trailing file arg (after the filter)
RE_JQ_FILE = re.compile(r"\bjq\b[^|]*\.(json|jsonl|ndjson)\b")

MODERN_TOOLS = {
    "rg", "fd", "fdfind", "bat", "sd", "mlr", "eza", "exa", "jq", "yq",
    "dasel", "gron", "teip", "parallel", "hyperfine", "up", "pv",
    "sponge", "pee", "ifne", "chronic", "vipe", "choose", "xsv", "qsv",
    "delta", "btm", "duf", "dust", "procs", "zoxide", "tldr",
}
CLASSIC_FOR = {
    "rg": "grep", "fd": "find", "fdfind": "find", "bat": "cat",
    "sd": "sed", "mlr": "awk", "eza": "ls", "exa": "ls",
}


def project_name_from_path(path: str) -> str:
    # e.g. /home/.../projects/-home-user-Documents-myproject/foo.jsonl
    parts = path.split(os.sep)
    try:
        idx = parts.index("projects")
        return parts[idx + 1]
    except (ValueError, IndexError):
        return "unknown"


def month_bucket(ts_iso: str) -> str:
    try:
        # handle 'Z' suffix
        if ts_iso.endswith("Z"):
            ts_iso = ts_iso[:-1] + "+00:00"
        dt = datetime.fromisoformat(ts_iso)
        return dt.strftime("%Y-%m")
    except Exception:
        return "unknown"


def iter_bash_commands():
    """Yield (project, month, command) tuples streaming through JSONL files."""
    files = sorted(set(glob.glob(PROJECTS_GLOB) + glob.glob(SUBAGENT_GLOB)))
    for path in files:
        proj = project_name_from_path(path)
        try:
            with open(path, "r", encoding="utf-8", errors="replace") as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        d = json.loads(line)
                    except Exception:
                        continue
                    if d.get("type") != "assistant":
                        continue
                    msg = d.get("message")
                    if not isinstance(msg, dict):
                        continue
                    content = msg.get("content")
                    if not isinstance(content, list):
                        continue
                    ts = d.get("timestamp", "")
                    mb = month_bucket(ts) if ts else "unknown"
                    for c in content:
                        if not isinstance(c, dict):
                            continue
                        if c.get("type") != "tool_use":
                            continue
                        if c.get("name") != "Bash":
                            continue
                        inp = c.get("input") or {}
                        cmd = inp.get("command")
                        if not isinstance(cmd, str) or not cmd.strip():
                            continue
                        yield proj, mb, cmd
        except Exception as e:
            sys.stderr.write(f"err reading {path}: {e}\n")


def main():
    t0 = time.time()
    total = 0
    piped = 0
    stage_dist = Counter()
    binary_counts = Counter()
    binary_first_stage = Counter()  # first binary of each pipeline (anchor)
    template_2 = Counter()
    template_3 = Counter()

    antipatterns = Counter()
    antipattern_samples = defaultdict(list)

    jq_total = 0
    jq_from_file = 0
    jq_from_pipe = 0
    jq_piped_out = 0
    jq_next_stage = Counter()
    jq_filters = Counter()

    per_project_bash = Counter()
    per_project_binary = defaultdict(Counter)
    per_project_piped = Counter()

    month_counts = Counter()

    modern_counts = Counter()
    classic_counts = Counter()

    sample_caps = {}

    def maybe_sample(key, cmd, cap=5):
        if antipattern_samples[key] is None:
            return
        if len(antipattern_samples[key]) < cap:
            # truncate samples for safety
            antipattern_samples[key].append(cmd[:200])

    n_processed = 0
    for proj, mb, cmd in iter_bash_commands():
        n_processed += 1
        total += 1
        per_project_bash[proj] += 1
        month_counts[mb] += 1

        stages = naive_pipe_split(cmd)
        n_stages = len(stages)
        if n_stages == 1:
            stage_dist["1"] += 1
        elif n_stages == 2:
            stage_dist["2"] += 1
        elif n_stages == 3:
            stage_dist["3"] += 1
        else:
            stage_dist["4+"] += 1

        if n_stages > 1:
            piped += 1
            per_project_piped[proj] += 1

        bins = [first_binary(s) for s in stages]
        bins = [b for b in bins if b]

        for b in bins:
            binary_counts[b] += 1
            per_project_binary[proj][b] += 1
            if b in MODERN_TOOLS:
                modern_counts[b] += 1
            if b in {"grep", "find", "cat", "sed", "awk", "ls"}:
                classic_counts[b] += 1

        if bins:
            binary_first_stage[bins[0]] += 1

        if len(bins) >= 2:
            template_2["→".join(bins[:2])] += 1
        if len(bins) >= 3:
            template_3["→".join(bins[:3])] += 1

        # antipatterns
        if RE_CAT_GREP.search(cmd):
            antipatterns["cat_file_grep_UUOC"] += 1
            maybe_sample("cat_file_grep_UUOC", cmd)
        if RE_CAT_HEAD.search(cmd):
            antipatterns["cat_file_head_UUOC"] += 1
            maybe_sample("cat_file_head_UUOC", cmd)
        if RE_CAT_TAIL.search(cmd):
            antipatterns["cat_file_tail_UUOC"] += 1
        if RE_CAT_WC.search(cmd):
            antipatterns["cat_file_wc_UUOC"] += 1
            maybe_sample("cat_file_wc_UUOC", cmd)
        if RE_CAT_JQ.search(cmd):
            antipatterns["cat_file_jq"] += 1
            maybe_sample("cat_file_jq", cmd)
        if RE_GREP_WC.search(cmd):
            antipatterns["grep_pipe_wc_l"] += 1
            maybe_sample("grep_pipe_wc_l", cmd)
        if RE_GREP_HEAD.search(cmd):
            antipatterns["grep_pipe_head"] += 1
        if RE_SORT_UNIQ.search(cmd):
            antipatterns["sort_pipe_uniq_dedupe"] += 1
            maybe_sample("sort_pipe_uniq_dedupe", cmd)
        if RE_SORT_UNIQ_C.search(cmd):
            antipatterns["sort_pipe_uniq_c"] += 1
        if RE_FIND_XARGS_GREP.search(cmd):
            antipatterns["find_xargs_grep"] += 1
            maybe_sample("find_xargs_grep", cmd)
        if RE_FIND_EXEC_SEMI.search(cmd):
            antipatterns["find_exec_semicolon"] += 1
        if RE_FIND_EXEC_PLUS.search(cmd):
            antipatterns["find_exec_plus"] += 1
        if RE_LS_GREP.search(cmd):
            antipatterns["ls_pipe_grep"] += 1
            maybe_sample("ls_pipe_grep", cmd)
        if RE_AWK_AWK.search(cmd):
            antipatterns["awk_pipe_awk"] += 1
        if RE_SED_SED.search(cmd):
            antipatterns["sed_pipe_sed"] += 1
        if RE_REDIR_TEE.search(cmd):
            antipatterns["redir_pipe_tee"] += 1

        # jq analysis
        if "jq" in bins or re.search(r"\bjq\b", cmd):
            # only count if jq is actually a binary in any stage
            jq_in_stage_idxs = [i for i, b in enumerate(bins) if b == "jq"]
            for idx in jq_in_stage_idxs:
                jq_total += 1
                # determine input mode for THIS jq stage
                if idx == 0:
                    # first stage — input is either file arg or stdin via redirection
                    stage = stages[idx]
                    if RE_JQ_FILE.search(stage):
                        jq_from_file += 1
                    elif "<" in stage:
                        jq_from_file += 1  # redirected from file
                    else:
                        # jq with no file and no pipe-from — likely from stdin via heredoc or just argstring;
                        # bucket as 'other' but treat as file-ish
                        jq_from_file += 1
                else:
                    jq_from_pipe += 1
                # piped onward?
                if idx + 1 < len(bins):
                    jq_piped_out += 1
                    jq_next_stage[bins[idx + 1]] += 1
                # extract first filter
                m = RE_JQ_FILTER.search(stages[idx])
                if m:
                    flt = m.group(2) or m.group(3) or m.group(4) or ""
                    if flt:
                        jq_filters[flt[:40]] += 1

    # finalize per-project top binaries
    top_projects = per_project_bash.most_common(12)
    per_project_summary = []
    for proj, cnt in top_projects:
        top_bins = per_project_binary[proj].most_common(10)
        per_project_summary.append({
            "project": proj,
            "bash_total": cnt,
            "piped": per_project_piped[proj],
            "piped_pct": round(100.0 * per_project_piped[proj] / max(1, cnt), 1),
            "top_binaries": top_bins,
        })

    # time-series — last 6 months
    months_sorted = sorted(m for m in month_counts.keys() if m != "unknown")
    last6 = months_sorted[-6:] if len(months_sorted) >= 6 else months_sorted
    ts = [{"month": m, "bash_calls": month_counts[m]} for m in months_sorted]

    # modern vs classic
    mv_classic = {
        "rg_vs_grep":      (binary_counts.get("rg", 0),      binary_counts.get("grep", 0)),
        "fd_vs_find":      (binary_counts.get("fd", 0) + binary_counts.get("fdfind", 0),
                            binary_counts.get("find", 0)),
        "bat_vs_cat":      (binary_counts.get("bat", 0),     binary_counts.get("cat", 0)),
        "sd_vs_sed":       (binary_counts.get("sd", 0),      binary_counts.get("sed", 0)),
        "mlr_vs_awk":      (binary_counts.get("mlr", 0),     binary_counts.get("awk", 0)),
        "eza_exa_vs_ls":   (binary_counts.get("eza", 0) + binary_counts.get("exa", 0),
                            binary_counts.get("ls", 0)),
    }
    other_modern = {t: binary_counts.get(t, 0) for t in MODERN_TOOLS}

    report = {
        "meta": {
            "elapsed_seconds": round(time.time() - t0, 1),
            "files_glob": [PROJECTS_GLOB, SUBAGENT_GLOB],
            "heuristic_note": (
                "Pipeline splitting: naive top-level | scan, skipping pipes inside "
                "'...' and \"...\" with backslash-escape awareness. ||, command substitutions, "
                "and process substitutions are not specially handled. First-binary parsing "
                "strips leading (/{/$/!, looks past var=val and wrappers like sudo/time/env/xargs."
            ),
        },
        "totals": {
            "bash_calls": total,
            "piped_calls": piped,
            "piped_pct": round(100.0 * piped / max(1, total), 2),
            "stage_distribution": dict(stage_dist),
        },
        "top_binaries": binary_counts.most_common(40),
        "modern_vs_classic": mv_classic,
        "modern_tool_counts": {k: v for k, v in sorted(other_modern.items(), key=lambda x: -x[1]) if v > 0},
        "antipatterns": dict(antipatterns.most_common()),
        "antipattern_samples": {k: v for k, v in antipattern_samples.items()},
        "top_template_2": template_2.most_common(30),
        "top_template_3": template_3.most_common(30),
        "jq": {
            "total_invocations": jq_total,
            "from_file_or_stdin_direct": jq_from_file,
            "from_pipe": jq_from_pipe,
            "piped_to_next_stage": jq_piped_out,
            "top_next_stage": jq_next_stage.most_common(10),
            "top_filter_prefixes": jq_filters.most_common(10),
        },
        "per_project_top": per_project_summary,
        "time_series_all": ts,
        "time_series_last6": last6,
    }

    with open(OUTPUT, "w") as f:
        json.dump(report, f, indent=2)
    sys.stderr.write(f"done in {report['meta']['elapsed_seconds']}s; total bash={total}\n")


if __name__ == "__main__":
    main()
