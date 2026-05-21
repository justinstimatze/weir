#!/usr/bin/env python3
"""Sanitize a weir baseline JSON for public release.

Removes PII (usernames, project names, absolute paths in samples,
custom-CLI tool names that identify private projects, jq filter prefixes
that reveal data schemas) while preserving aggregate fields the public
README and `weir measure` need: totals, modern_vs_classic,
modern_tool_counts, antipattern COUNTS, top_binaries (whitelisted),
top_template_2/3 (whitelisted), jq stats minus filter prefixes,
time_series_all/last6.

Drops entirely:
- antipattern_samples: literal commands contain absolute paths + project
  names + sometimes the contents of files those commands inspected.
- per_project_top: project field is `-home-<user>-Documents-<proj>`.
- jq.top_filter_prefixes: literal jq queries reveal data schemas
  from the user's actual work (vulnerability scans, notebook
  structures, internal findings shapes, etc.).

Filters to a whitelist:
- top_binaries: only entries whose binary is in WELL_KNOWN_BINARIES.
  Drops custom / project-specific CLIs whose names would identify the
  private project they're part of.
- top_template_2 / top_template_3: only chains where EVERY stage's
  binary is in WELL_KNOWN_BINARIES. A chain with one custom tool
  reveals the project; safer to drop the whole chain.

Redacts:
- meta.files_glob: replaced with placeholders.

Run: scripts/sanitize-baseline.py SRC.json [DEST.json]
  (if DEST omitted, prints to stdout)
"""
import argparse
import json
import sys


PUBLIC_FIELDS = {
    "meta",
    "totals",
    "top_binaries",
    "modern_vs_classic",
    "modern_tool_counts",
    "antipatterns",
    "top_template_2",
    "top_template_3",
    "jq",
    "time_series_all",
    "time_series_last6",
}


# Whitelist of well-known shell + dev binaries. Anything outside this list
# is suppressed from the public artifact to avoid surfacing custom CLIs
# that identify private projects.
WELL_KNOWN_BINARIES = {
    # POSIX coreutils + common Linux utils
    "cat", "cd", "cp", "mv", "rm", "mkdir", "rmdir", "ls", "find",
    "grep", "egrep", "fgrep", "sed", "awk", "head", "tail", "sort",
    "uniq", "wc", "tr", "cut", "paste", "tee", "echo", "printf",
    "pwd", "ln", "chmod", "chown", "touch", "stat", "file", "tar",
    "gzip", "gunzip", "bzip2", "zstd", "xz", "xargs", "env", "test",
    "basename", "dirname", "kill", "pgrep", "pkill", "ps", "top",
    "htop", "df", "du", "free", "uname", "date", "hostname", "whoami",
    "id", "which", "type", "command", "alias", "history",
    "sleep", "watch", "yes", "true", "false", "seq", "expr", "bc",
    "diff", "cmp", "patch", "od", "xxd", "md5sum", "sha1sum",
    "sha256sum", "base64", "openssl", "iconv", "less", "more", "man",
    "info", "tldr",
    # Networking
    "ssh", "scp", "sftp", "rsync", "curl", "wget", "nc", "ncat", "dig",
    "host", "nslookup", "ping", "traceroute", "ip", "ss", "netstat",
    # Build / lang ecosystems (generic; common across users)
    "make", "cmake", "ninja", "gcc", "g++", "clang", "clang++", "ld",
    "ar", "ranlib", "objdump", "strip", "go", "cargo", "rustc",
    "python", "python3", "pip", "pip3", "uv", "ruff", "mypy", "black",
    "pytest", "ruby", "gem", "bundle", "node", "npm", "npx", "yarn",
    "pnpm", "deno", "java", "javac", "mvn", "gradle", "dotnet",
    "php", "composer", "perl", "lua", "swift",
    # Containers / cloud
    "docker", "podman", "kubectl", "helm", "terraform", "aws", "gcloud",
    "az",
    # Common dev tools
    "git", "hub", "gh", "glab", "tig", "fossil", "hg", "svn",
    "tmux", "screen", "byobu", "vim", "nvim", "emacs", "nano", "code",
    "shellcheck", "shfmt", "jq",
    # Modern shell tools weir tracks
    "rg", "ripgrep", "fd", "fdfind", "bat", "batcat", "sd", "mlr",
    "miller", "eza", "exa", "dust", "duf", "procs", "bottom", "btm",
    "delta", "choose", "hexyl", "yq", "dasel", "gron", "sponge",
    "pv", "parallel", "hyperfine", "up", "teip", "xsv", "qsv", "fzf",
    "watchexec", "entr",
    # Shell builtins / common shell-script chrome
    "source", "exec", "exit", "set", "unset", "export", "declare",
    "local", "read", "if", "then", "else", "fi", "for", "do", "done",
    "while", "case", "esac", "function", "return", "break", "continue",
    "shift", "trap", "wait", "eval", "bash", "sh", "zsh", "fish",
    # Common single-character placeholders that show up in poorly-parsed
    # binary fields (the regex extractor pulls these out for chained
    # commands)
    "#",
}


def whitelist_binary(name: str) -> bool:
    return name in WELL_KNOWN_BINARIES


def whitelist_chain(chain: str) -> bool:
    """A binary chain like `cd→head→head` passes only if every stage
    is in the whitelist."""
    return all(whitelist_binary(b) for b in chain.split("→"))


def sanitize(raw: dict) -> dict:
    out = {k: v for k, v in raw.items() if k in PUBLIC_FIELDS}

    # meta.files_glob has absolute paths
    meta = out.get("meta", {})
    if "files_glob" in meta:
        meta["files_glob"] = ["~/.claude/projects/*/*.jsonl",
                              "~/.claude/projects/*/subagents/*.jsonl"]
    out["meta"] = meta

    # Filter binary lists to whitelist
    if "top_binaries" in out:
        out["top_binaries"] = [[b, c] for b, c in out["top_binaries"]
                               if whitelist_binary(b)]
    if "top_template_2" in out:
        out["top_template_2"] = [[c, n] for c, n in out["top_template_2"]
                                 if whitelist_chain(c)]
    if "top_template_3" in out:
        out["top_template_3"] = [[c, n] for c, n in out["top_template_3"]
                                 if whitelist_chain(c)]

    # jq.top_filter_prefixes leak data schemas; drop entirely.
    # Keep jq.top_next_stage but filter to whitelisted binaries.
    jq = out.get("jq", {})
    jq.pop("top_filter_prefixes", None)
    if "top_next_stage" in jq:
        jq["top_next_stage"] = [[b, c] for b, c in jq["top_next_stage"]
                                if whitelist_binary(b)]
    out["jq"] = jq

    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("src", help="raw baseline JSON path")
    ap.add_argument("dest", nargs="?", help="output path (default: stdout)")
    args = ap.parse_args()

    with open(args.src) as f:
        raw = json.load(f)
    clean = sanitize(raw)
    text = json.dumps(clean, indent=2) + "\n"
    if args.dest:
        with open(args.dest, "w") as f:
            f.write(text)
        sys.stderr.write(f"wrote sanitized baseline -> {args.dest}\n")
    else:
        sys.stdout.write(text)


if __name__ == "__main__":
    main()
