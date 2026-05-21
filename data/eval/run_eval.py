#!/usr/bin/env python3
"""weir synthetic eval — API runner.

Multi-model. Parallel across (model, prompt, condition, sample_idx)
combinations via a ThreadPoolExecutor. Cached responses are reused;
only un-cached combos hit the API.

Cache key includes the model name, so adding a model doesn't invalidate
existing cache for the other models.
"""
import argparse
import concurrent.futures
import hashlib
import json
import os
import subprocess
import sys
import time
from pathlib import Path

HERE = Path(__file__).parent
PROMPTS_PATH = HERE / "prompts.jsonl"
CACHE_DIR = HERE / "cache"


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

try:
    import anthropic
except ImportError:
    sys.stderr.write("error: pip install anthropic\n")
    sys.exit(1)

MODELS = [
    "claude-opus-4-7",
    "claude-sonnet-4-6",
    "claude-haiku-4-5-20251001",
]
N_SAMPLES = 3
MAX_TOKENS = 1024
PARALLEL_WORKERS = 8


def load_prompts(filter_substring=None):
    out = []
    with open(PROMPTS_PATH) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            p = json.loads(line)
            blob = (p.get("category", "") + " " + p.get("id", "") + " " + p.get("prompt", "")).lower()
            if filter_substring and filter_substring.lower() not in blob:
                continue
            out.append(p)
    return out


def get_treatment_system_prompt():
    weir = os.path.expanduser("~/go/bin/weir")
    if not os.path.exists(weir):
        sys.stderr.write(f"error: {weir} not found; run 'go install ./cmd/weir' from the repo root first\n")
        sys.exit(1)
    out = subprocess.check_output([weir, "inject"], text=True)
    return json.loads(out)["hookSpecificOutput"]["additionalContext"]


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


def cached_call(client, model, system, user, sample_idx):
    CACHE_DIR.mkdir(exist_ok=True)
    key = cache_key(model, system, user, sample_idx)
    path = CACHE_DIR / f"{key}.json"
    if path.exists():
        return "cached", json.loads(path.read_text())["text"]
    kwargs = dict(
        model=model,
        max_tokens=MAX_TOKENS,
        messages=[{"role": "user", "content": user}],
    )
    if system:
        kwargs["system"] = [{"type": "text", "text": system}]
    try:
        msg = client.messages.create(**kwargs)
    except Exception as e:
        return f"err:{e}", ""
    text_parts = []
    for block in msg.content:
        if getattr(block, "type", None) == "text":
            text_parts.append(block.text)
    text = "\n".join(text_parts)
    path.write_text(json.dumps({
        "text": text,
        "model": model,
        "input_tokens": msg.usage.input_tokens,
        "output_tokens": msg.usage.output_tokens,
    }, indent=2))
    return "api", text


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--filter", help="only run prompts whose id/category/text contains this substring")
    ap.add_argument("--samples", type=int, default=N_SAMPLES)
    ap.add_argument("--models", nargs="+", default=MODELS, help="model IDs to run (default: opus, sonnet, haiku)")
    ap.add_argument("--workers", type=int, default=PARALLEL_WORKERS)
    args = ap.parse_args()

    if not os.environ.get("ANTHROPIC_API_KEY"):
        sys.stderr.write("error: ANTHROPIC_API_KEY not set\n")
        sys.exit(1)

    client = anthropic.Anthropic()
    prompts = load_prompts(args.filter)
    treatment_system = get_treatment_system_prompt()

    # Build the full work plan
    tasks = []
    for model in args.models:
        for p in prompts:
            for condition, system in [("control", ""), ("treatment", treatment_system)]:
                for i in range(args.samples):
                    tasks.append((model, p, condition, system, i))

    sys.stderr.write(f"weir eval: {len(args.models)} models × {len(prompts)} prompts × 2 conditions × {args.samples} samples = {len(tasks)} total\n")
    sys.stderr.write(f"weir eval: treatment system prompt is {len(treatment_system)} chars (~{len(treatment_system)//4} tokens)\n")
    sys.stderr.write(f"weir eval: parallel workers = {args.workers}\n\n")

    t0 = time.time()
    api_calls = 0
    cache_hits = 0
    errors = 0
    done = 0

    def work(task):
        model, p, condition, system, i = task
        status, _ = cached_call(client, model, system, p["prompt"], i)
        return model, p["id"], condition, i, status

    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as ex:
        futures = [ex.submit(work, t) for t in tasks]
        for fut in concurrent.futures.as_completed(futures):
            model, pid, cond, i, status = fut.result()
            done += 1
            if status == "cached":
                cache_hits += 1
            elif status == "api":
                api_calls += 1
            else:
                errors += 1
            if done % 25 == 0 or done == len(tasks):
                sys.stderr.write(f"  [{done}/{len(tasks)}] cache_hits={cache_hits} api={api_calls} err={errors}  (last: {model.split('-')[1]:<6} {pid:<10} {cond:<10} #{i} {status[:20]})\n")

    sys.stderr.write(f"\nweir eval: done in {time.time() - t0:.1f}s; {cache_hits} cache hits, {api_calls} API calls, {errors} errors\n")
    sys.stderr.write(f"weir eval: cache at {CACHE_DIR}\n")
    sys.stderr.write(f"weir eval: next step — python3 grade.py\n")


if __name__ == "__main__":
    main()
