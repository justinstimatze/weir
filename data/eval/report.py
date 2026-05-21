#!/usr/bin/env python3
"""weir synthetic eval — reporter.

Reads graded.json and emits per-model control-vs-treatment delta tables.
For each model: per-rule fire rate, modern-vs-classic binary counts,
per-category aggregate. Then a cross-model summary at the end.
"""
import json
import sys
from collections import Counter, defaultdict
from pathlib import Path

HERE = Path(__file__).parent
GRADED = HERE / "graded.json"

MODERN_TOOLS = {
    "rg", "fd", "fdfind", "bat", "batcat", "sd", "mlr", "eza", "exa", "jq",
    "parallel", "hyperfine", "fzf", "entr", "dust", "duf", "delta", "procs",
    "hexyl", "sponge", "pv",
}
CLASSIC_TOOLS = {
    "grep", "find", "cat", "sed", "awk", "ls", "ps", "which",
    "head", "tail", "wc",
}


def pct(num, denom):
    if denom == 0:
        return 0.0
    return 100.0 * num / denom


def report_model(model, samples):
    by_cond = defaultdict(list)
    for s in samples:
        by_cond[s["condition"]].append(s)
    n_control = len(by_cond.get("control", []))
    n_treatment = len(by_cond.get("treatment", []))

    print(f"\n{'='*70}")
    print(f"MODEL: {model}")
    print(f"  control samples: {n_control}; treatment samples: {n_treatment}")
    print(f"{'='*70}")

    # per-rule fire rate (samples with at least one hit)
    rules_seen = set()
    samples_with_hit = {"control": Counter(), "treatment": Counter()}
    for s in samples:
        cond = s["condition"]
        for r in set(s["rule_hits"]):
            samples_with_hit[cond][r] += 1
            rules_seen.add(r)

    if rules_seen:
        print(f"\n  per-rule fire RATE (samples-with-hit / total)")
        print(f"  {'rule':<24} {'control':>10} {'treatment':>10} {'delta':>10}")
        print(f"  {'-'*60}")
        for r in sorted(rules_seen, key=lambda x: -max(samples_with_hit['control'][x], samples_with_hit['treatment'][x])):
            c = pct(samples_with_hit["control"][r], n_control)
            t = pct(samples_with_hit["treatment"][r], n_treatment)
            print(f"  {r:<24} {c:>9.1f}%  {t:>9.1f}%  {t - c:>+9.1f}pp")
    else:
        print(f"\n  per-rule fire RATE: no rules fired in either condition")

    # first-binary counts
    bin_counts = {"control": Counter(), "treatment": Counter()}
    for s in samples:
        for b in s["first_binaries"]:
            bin_counts[s["condition"]][b] += 1
    interesting = (set(bin_counts["control"]) | set(bin_counts["treatment"])) & (MODERN_TOOLS | CLASSIC_TOOLS)
    if interesting:
        print(f"\n  first-binary counts (interesting tools only)")
        print(f"  {'binary':<14} {'control':>10} {'treatment':>10} {'delta':>10} {'tier':>10}")
        print(f"  {'-'*70}")
        for b in sorted(interesting, key=lambda x: -max(bin_counts['control'][x], bin_counts['treatment'][x])):
            c = bin_counts["control"][b]
            t = bin_counts["treatment"][b]
            tier = "modern" if b in MODERN_TOOLS else "classic"
            print(f"  {b:<14} {c:>10} {t:>10} {t - c:>+10} {tier:>10}")

    # modern/classic aggregate
    c_modern = sum(bin_counts["control"][b] for b in MODERN_TOOLS)
    c_classic = sum(bin_counts["control"][b] for b in CLASSIC_TOOLS)
    t_modern = sum(bin_counts["treatment"][b] for b in MODERN_TOOLS)
    t_classic = sum(bin_counts["treatment"][b] for b in CLASSIC_TOOLS)
    print(f"\n  AGGREGATE modern-vs-classic first-binary counts:")
    print(f"    control:   modern={c_modern:>4}  classic={c_classic:>4}  modern_share={pct(c_modern, c_modern+c_classic):.1f}%")
    print(f"    treatment: modern={t_modern:>4}  classic={t_classic:>4}  modern_share={pct(t_modern, t_modern+t_classic):.1f}%")
    print(f"    DELTA modern-share: {pct(t_modern, t_modern+t_classic) - pct(c_modern, c_modern+c_classic):+.1f}pp")

    return {
        "model": model,
        "control_modern_share": pct(c_modern, c_modern+c_classic),
        "treatment_modern_share": pct(t_modern, t_modern+t_classic),
        "control_rule_fires_total": sum(samples_with_hit["control"].values()),
        "treatment_rule_fires_total": sum(samples_with_hit["treatment"].values()),
    }


def main():
    if not GRADED.exists():
        sys.stderr.write("error: graded.json missing — run grade.py first\n")
        sys.exit(1)
    data = json.loads(GRADED.read_text())
    samples = data["samples"]
    models = data.get("models", [data.get("model", "unknown")])

    print(f"weir synthetic eval")
    print(f"  N samples / (model, prompt, condition): {data['n_samples_per_condition']}")
    print(f"  treatment system-prompt size: {data['treatment_chars']} chars (~{data['treatment_chars']//4} tokens)")
    print(f"  missing cache entries: {data.get('missing_cache_entries', 0)}")

    by_model = defaultdict(list)
    for s in samples:
        by_model[s["model"]].append(s)

    summaries = []
    for m in models:
        if m in by_model:
            summaries.append(report_model(m, by_model[m]))

    print(f"\n{'='*70}")
    print("CROSS-MODEL SUMMARY")
    print(f"{'='*70}")
    print(f"{'model':<32} {'control_mod%':>14} {'treatment_mod%':>16} {'delta':>10}")
    print(f"{'-'*72}")
    for s in summaries:
        print(f"{s['model']:<32} {s['control_modern_share']:>13.1f}% {s['treatment_modern_share']:>15.1f}% {s['treatment_modern_share'] - s['control_modern_share']:>+9.1f}pp")
    print()
    print(f"{'model':<32} {'control_fires':>14} {'treatment_fires':>16} {'delta':>10}")
    print(f"{'-'*72}")
    for s in summaries:
        print(f"{s['model']:<32} {s['control_rule_fires_total']:>14} {s['treatment_rule_fires_total']:>16} {s['treatment_rule_fires_total'] - s['control_rule_fires_total']:>+10}")
    print()
    print("Interpretation:")
    print("  - control_mod%: share of first-binary tokens that are modern tools, no weir injection.")
    print("  - treatment_mod%: same, with weir's inject text in the system prompt.")
    print("  - rule_fires: total samples in which at least one weir rule fires.")
    print("  - 'block'-action rules fire here only via advisory detection — production block-and-retry not simulated.")


if __name__ == "__main__":
    main()
