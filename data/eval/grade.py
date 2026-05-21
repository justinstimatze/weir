#!/usr/bin/env python3
"""weir synthetic eval — grader.

Reads cached API responses (one per model × prompt × condition × sample),
extracts bash commands, runs each through `weir suggest`, records rule
hits + first-token binaries. Output: graded.json keyed by model so the
reporter can do per-model breakdowns.
"""
import hashlib
import json
import os
import re
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).parent
CACHE_DIR = HERE / "cache"
PROMPTS_PATH = HERE / "prompts.jsonl"
OUT_PATH = HERE / "graded.json"
WEIR = os.path.expanduser("~/go/bin/weir")


def load_env(path: Path):
    if not path.exists():
        return
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if "=" in line:
            k, _, v = line.partition("=")
            os.environ.setdefault(k.strip(), v.strip().strip('"').strip("'"))


load_env(HERE / ".env")

MODELS = [
    "claude-opus-4-7",
    "claude-sonnet-4-6",
    "claude-haiku-4-5-20251001",
]
N_SAMPLES = 3

RE_FENCED = re.compile(r"```(?:bash|sh|shell)?\s*\n(.+?)\n```", re.DOTALL)
RE_INLINE_BACKTICK = re.compile(r"`([^`\n]{4,})`")
RE_LOOKS_LIKE_BASH = re.compile(r"^(?:[A-Z_]+=|sudo |cd |ls |cat |grep |find |rg |fd |bat |sd |mlr |jq |awk |sed |head |tail |wc |sort |uniq |ps |pgrep |which |command |go |npm |python|pip|git |make |bash |sh |tmux |fzf |entr |hyperfine |parallel |curl |wget |ssh |scp )")


def cache_key(model, system, user, sample_idx):
    h = hashlib.sha256()
    h.update(model.encode())
    h.update(b"\x00")
    h.update(system.encode())
    h.update(b"\x00")
    h.update(user.encode())
    h.update(b"\x00")
    h.update(str(sample_idx).encode())
    return h.hexdigest()[:16]


def load_prompts():
    out = []
    with open(PROMPTS_PATH) as f:
        for line in f:
            line = line.strip()
            if line:
                out.append(json.loads(line))
    return out


def get_treatment_system_prompt():
    out = subprocess.check_output([WEIR, "inject"], text=True)
    return json.loads(out)["hookSpecificOutput"]["additionalContext"]


def load_cached(model, system, user, sample_idx):
    key = cache_key(model, system, user, sample_idx)
    path = CACHE_DIR / f"{key}.json"
    if not path.exists():
        return None
    return json.loads(path.read_text())["text"]


def extract_bash(text):
    commands = []
    seen = set()
    for m in RE_FENCED.finditer(text):
        block = m.group(1).strip()
        for line in block.split("\n"):
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            if line not in seen:
                seen.add(line)
                commands.append(line)
    for m in RE_INLINE_BACKTICK.finditer(text):
        cmd = m.group(1).strip()
        if not RE_LOOKS_LIKE_BASH.match(cmd):
            continue
        if cmd not in seen:
            seen.add(cmd)
            commands.append(cmd)
    return commands


def first_token(cmd):
    s = cmd.lstrip("(){}$! \t")
    parts = s.split()
    if not parts:
        return ""
    tok = parts[0]
    while "=" in tok and not tok.startswith("=") and len(parts) > 1:
        parts = parts[1:]
        tok = parts[0]
    if "/" in tok:
        tok = tok.rsplit("/", 1)[-1]
    return tok


def grade_command(cmd):
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
    try:
        p = subprocess.run(
            [WEIR, "suggest"],
            input=payload,
            capture_output=True,
            text=True,
            timeout=10,
        )
    except Exception:
        return []
    if not p.stdout.strip():
        return []
    try:
        out = json.loads(p.stdout)
    except Exception:
        return []
    hso = out.get("hookSpecificOutput", {})
    text = hso.get("additionalContext", "") + hso.get("permissionDecisionReason", "")
    BANNER = {"weir-suggest", "weir"}
    return [r for r in re.findall(r"\[([a-z][-a-z]+)\]", text) if r not in BANNER]


def main():
    if not CACHE_DIR.exists() or not any(CACHE_DIR.glob("*.json")):
        sys.stderr.write("error: cache empty — run run_eval.py first\n")
        sys.exit(1)
    if not os.path.exists(WEIR):
        sys.stderr.write(f"error: {WEIR} not found; run 'go install ./cmd/weir' from repo root\n")
        sys.exit(1)

    prompts = load_prompts()
    treatment_system = get_treatment_system_prompt()

    samples = []
    missing = 0
    for model in MODELS:
        for p in prompts:
            for condition, system in [("control", ""), ("treatment", treatment_system)]:
                for i in range(N_SAMPLES):
                    text = load_cached(model, system, p["prompt"], i)
                    if text is None:
                        missing += 1
                        continue
                    bash_blocks = extract_bash(text)
                    rule_hits = []
                    first_binaries = []
                    for cmd in bash_blocks:
                        rule_hits.extend(grade_command(cmd))
                        fb = first_token(cmd)
                        if fb:
                            first_binaries.append(fb)
                    samples.append({
                        "model": model,
                        "id": p["id"],
                        "category": p["category"],
                        "condition": condition,
                        "sample_idx": i,
                        "bash_blocks": bash_blocks,
                        "rule_hits": rule_hits,
                        "first_binaries": first_binaries,
                    })

    out = {
        "models": MODELS,
        "n_samples_per_condition": N_SAMPLES,
        "treatment_chars": len(treatment_system),
        "samples": samples,
        "missing_cache_entries": missing,
    }
    OUT_PATH.write_text(json.dumps(out, indent=2))
    sys.stderr.write(f"weir grade: {len(samples)} graded samples ({missing} missing from cache) -> {OUT_PATH}\n")
    sys.stderr.write(f"weir grade: next — python3 report.py\n")


if __name__ == "__main__":
    main()
