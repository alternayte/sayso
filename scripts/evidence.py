#!/usr/bin/env python3
"""Evidence record of a verify run, and its check.

write: evidence.py write --out <dir> --gates <gates.jsonl> --trace <trace.json> --spec <file>...
check: evidence.py check --out <dir> --spec <file>...

The spec hash is the SHA-256 of the spec files in the given order, read as
one stream.
"""

import argparse
import datetime
import hashlib
import json
import os
import re
import subprocess
import sys


def spec_hash(paths):
    h = hashlib.sha256()
    for p in paths:
        with open(p, "rb") as f:
            h.update(f.read())
    return h.hexdigest()


def git(*args):
    return subprocess.run(["git", *args], capture_output=True, text=True, check=True).stdout.strip()


def version(cmd):
    try:
        out = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
    except (OSError, subprocess.TimeoutExpired):
        return "not found"
    text = (out.stdout or out.stderr).strip().splitlines()
    return text[0] if text else "unknown"


def write(a):
    sha = git("rev-parse", "HEAD")
    gates = []
    with open(a.gates, encoding="utf-8") as f:
        for line in f:
            if line.strip():
                gates.append(json.loads(line))
    with open(a.trace, encoding="utf-8") as f:
        trace = json.load(f)
    tools = {
        "go": version(["go", "version"]),
        "node": version(["node", "--version"]),
        "bun": version(["bun", "--version"]),
        "just": version(["just", "--version"]),
        "docker": version(["docker", "version", "--format", "{{.Server.Version}}"]),
        "golangci-lint": version(["golangci-lint", "version", "--short"]),
    }
    lines = [
        f"# Evidence {sha}",
        "",
        f"date: {datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')}",
        f"sdd_sha256: {spec_hash(a.spec)}",
        f"clean_tree: {'yes' if git('status', '--porcelain') == '' else 'no'}",
        "",
        "## Gates",
        "",
        "| Gate | Result | Seconds |",
        "|---|---|---|",
    ]
    for g in gates:
        lines.append(f"| {g['gate']} | {'PASS' if g['pass'] else 'FAIL'} | {g['seconds']} |")
    lines += [
        "",
        "## Counts",
        "",
        f"- requirements: {trace['requirements']}",
        f"- scenarios: {trace['scenarios']}",
        f"- scenarios with a passing test: {trace['scenarios_passed']}",
        f"- tests that name a scenario: {trace['tests']}",
        f"- trace problems: {len(trace['problems'])}",
        "",
        "## Tools",
        "",
    ]
    lines += [f"- {k}: {v}" for k, v in tools.items()]
    if trace["problems"]:
        lines += ["", "## Trace problems", ""] + [f"- {p}" for p in trace["problems"]]
    os.makedirs(a.out, exist_ok=True)
    path = os.path.join(a.out, sha + ".md")
    with open(path, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")
    print(f"evidence: wrote {path}")
    return 0 if all(g["pass"] for g in gates) and not trace["problems"] else 1


def check(a):
    problems = []
    if git("status", "--porcelain") != "":
        problems.append("the working tree is not clean")
    sha = git("rev-parse", "HEAD")
    path = os.path.join(a.out, sha + ".md")
    if not os.path.exists(path):
        problems.append(f"no evidence for HEAD {sha}")
    else:
        with open(path, encoding="utf-8") as f:
            text = f.read()
        if re.search(r"^\| \S+ \| FAIL \|", text, re.M):
            problems.append("a gate failed")
        if not re.search(r"^\| \S+ \| PASS \|", text, re.M):
            problems.append("the evidence has no gate results")
        if re.search(r"^- trace problems: [1-9]", text, re.M):
            problems.append("the trace has problems")
        m = re.search(r"^sdd_sha256: (\w+)$", text, re.M)
        if not m or m.group(1) != spec_hash(a.spec):
            problems.append("sdd_sha256 differs from the current hash of the specification")
    for p in problems:
        print("evidence-check: " + p)
    if not problems:
        print(f"evidence-check: {sha} passes")
    return 1 if problems else 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", choices=["write", "check"])
    ap.add_argument("--out", required=True)
    ap.add_argument("--spec", action="append", required=True)
    ap.add_argument("--gates")
    ap.add_argument("--trace")
    a = ap.parse_args()
    return write(a) if a.mode == "write" else check(a)


if __name__ == "__main__":
    sys.exit(main())
