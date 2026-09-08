#!/usr/bin/env python3
"""weir flag-overlap boundary scan.

Enumerates the collision surface between each classic tool weir tracks and
the modern replacement it suggests: every single-letter flag that exists in
both --help outputs, with each tool's own one-line description sitting next
to it. A collision is not automatically a danger -- `-i` means
case-insensitive in both grep and rg, harmless. The point of this script is
to stop relying on a FIELD_REPORT after real damage to find the dangerous
ones (rg -r/-h were both found that way) and instead walk the whole space
once, up front.

Pairs come from the "prefer over" mappings in weir's own SessionStart
capability manifest (internal/probe), not re-typed by hand here.

Output: JSON to stdout (one row per pair, one entry per colliding letter),
meant to be piped to a file and read by a human or a second pass that
classifies risk. This script does NOT judge danger -- it only enumerates
the boundary. See flag_overlap_report.json for a run against this host.
"""
import json
import re
import subprocess
import sys

# (classic binary, modern binary invoked on this host). Binary names match
# what's actually on $PATH here (fdfind not fd, batcat not bat) -- same
# distinction weir's own probe has to make.
PAIRS = [
    ("grep", "rg"),
    ("sed", "sd"),
    ("find", "fdfind"),
    ("ps", "procs"),
    ("diff", "delta"),
    ("df", "duf"),
    ("ls", "eza"),
    ("cat", "batcat"),
    ("xxd", "hexyl"),
    ("awk", "mlr"),
]

# Matches a short flag at the start of a help line, optionally followed by
# ", --long-name" or " ARG" etc. Captures the letter and the rest of the
# line as the description (best-effort -- help formats aren't uniform).
FLAG_LINE = re.compile(
    r"^\s{0,4}-([A-Za-z])(?:,\s*(--[\w-]+))?[,\s]*(.*)$"
)


# Some tools' plain `--help` is a pointer, not a flag dump -- the real
# option table needs a specific subcommand/flag. Found by inspecting each
# tool's actual --help output on this host, not guessed.
HELP_OVERRIDES = {
    "ps": [["--help", "all"]],
    "mlr": [["help", "usage"], ["--usage"]],
}


def get_help(binary):
    # An override is trusted outright if it returns anything -- picking
    # "whichever attempt returned the most text" is not safe here: a
    # malformed generic fallback (e.g. `ps -h`) can silently run the tool
    # for real instead of erroring, producing live output (a process
    # listing) far longer than the actual help text it was competing
    # against. Found by hitting exactly that on `ps` on this host.
    for args in HELP_OVERRIDES.get(binary, []):
        text = _run(binary, args)
        if text.strip():
            return text
    best = ""
    for args in (["--help"], ["-h"], ["-h", "--help"]):
        text = _run(binary, args)
        if len(text) > len(best):
            best = text
    return best


def _run(binary, args):
    try:
        r = subprocess.run(
            [binary] + args, capture_output=True, text=True, timeout=5
        )
        return (r.stdout or "") + (r.stderr or "")
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return ""


def parse_flags(help_text):
    """Return {letter: description} from a --help block.

    Best-effort: takes the first line naming a given short flag, trims
    leading long-flag noise, caps description length. This is a triage
    aid, not a parser -- a human/model reads the description before
    trusting the classification.
    """
    flags = {}
    for line in help_text.splitlines():
        m = FLAG_LINE.match(line)
        if not m:
            continue
        letter, longflag, rest = m.groups()
        desc = rest.strip()
        if longflag and not desc:
            desc = longflag
        elif longflag:
            desc = f"{longflag}: {desc}"
        desc = re.sub(r"\s+", " ", desc)[:160]
        if letter not in flags and desc:
            flags[letter] = desc
        elif letter not in flags:
            flags[letter] = longflag or ""
    return flags


def main():
    results = []
    for classic, modern in PAIRS:
        c_flags = parse_flags(get_help(classic))
        m_flags = parse_flags(get_help(modern))
        overlap = sorted(set(c_flags) & set(m_flags))
        results.append(
            {
                "classic": classic,
                "modern": modern,
                "classic_flag_count": len(c_flags),
                "modern_flag_count": len(m_flags),
                "overlap_count": len(overlap),
                "overlap": [
                    {
                        "letter": letter,
                        "classic_desc": c_flags[letter],
                        "modern_desc": m_flags[letter],
                    }
                    for letter in overlap
                ],
            }
        )
    json.dump(results, sys.stdout, indent=2)
    print()


if __name__ == "__main__":
    main()
