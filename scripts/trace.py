#!/usr/bin/env python3
"""Requirement trace: every requirement has a scenario, every scenario has a
passing test in the reports, no test names an unknown ID, and the ledger has
the same IDs as the specification.

Usage: trace.py --spec <file> [--spec <file> ...] --ledger <file> --reports <dir>

Spec files are read in the given order; a later file replaces a scenario of
an earlier one. Reports are `go test -json` streams (*.jsonl with Action
fields), Playwright JSON (ui.json), Vitest JSON (*.vitest.json) and smoke
lines ({"scn": ..., "pass": ...}).
"""

import argparse
import glob
import json
import os
import re
import sys

ID = r"(?:REQ-[A-Z]+-\d+|NFR-\d+|SI-\d+)"
REQ_ROW = re.compile(r"^\| (" + ID + r") \|")
REQ_ITEM = re.compile(r"^- \*\*(" + ID + r")\*\*")
SCN = re.compile(r"^- \*\*(SCN-[A-Z0-9]+-\d+)\*\* \[([A-Z])\] \(([^)]*)\)")
GO_TEST = re.compile(r"^TestSCN_([A-Z0-9]+)_(\d{3}(?:_\d{3})*)(?:_|$)")
TITLE = re.compile(r"\bSCN-([A-Z0-9]+)-(\d{3})\b")


def parse_spec(paths):
    reqs, scns = set(), {}
    for p in paths:
        with open(p, encoding="utf-8") as f:
            for line in f:
                m = REQ_ROW.match(line) or REQ_ITEM.match(line)
                if m:
                    reqs.add(m.group(1))
                    continue
                m = SCN.match(line)
                if m:
                    scns[m.group(1)] = set(re.findall(ID, m.group(3)))
    return reqs, scns


def go_ids(name):
    m = GO_TEST.match(name)
    if not m:
        return []
    area = m.group(1)
    return [f"SCN-{area}-{n}" for n in m.group(2).split("_")]


def passed_tests(reports):
    """Returns {scenario: set(test names)} for passing tests and a list of
    (test, id) pairs that name a scenario."""
    passed, named = {}, []

    def add(scn, test, ok):
        named.append((test, scn))
        if ok:
            passed.setdefault(scn, set()).add(test)

    # go test -json streams: the last pass/fail action of a top-level test decides.
    for path in glob.glob(os.path.join(reports, "*.jsonl")):
        final = {}
        with open(path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line.startswith("{"):
                    continue
                try:
                    ev = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if "scn" in ev:  # smoke lines: one entry per check
                    key = ("smoke:" + ev["scn"], ev["scn"])
                    final[key] = final.get(key, True) and bool(ev.get("pass"))
                    continue
                test = ev.get("Test", "")
                if not test or "/" in test or ev.get("Action") not in ("pass", "fail"):
                    continue
                for scn in go_ids(test):
                    final[(test, scn)] = ev["Action"] == "pass"
        for (test, scn), ok in final.items():
            add(scn, test, ok)

    # Playwright JSON: suites with specs; a spec passes when all its tests pass.
    pw = os.path.join(reports, "ui.json")
    if os.path.exists(pw):
        with open(pw, encoding="utf-8") as f:
            data = json.load(f)

        def walk(suite):
            for spec in suite.get("specs", []):
                ok = spec.get("ok", False)
                for area, n in TITLE.findall(spec.get("title", "")):
                    add(f"SCN-{area}-{n}", "playwright: " + spec["title"], ok)
            for s in suite.get("suites", []):
                walk(s)

        for s in data.get("suites", []):
            walk(s)

    # Vitest JSON reporter output.
    for path in glob.glob(os.path.join(reports, "*.vitest.json")):
        with open(path, encoding="utf-8") as f:
            data = json.load(f)
        for file in data.get("testResults", []):
            for t in file.get("assertionResults", []):
                name = t.get("fullName") or t.get("title", "")
                for area, n in TITLE.findall(name):
                    add(f"SCN-{area}-{n}", "vitest: " + name, t.get("status") == "passed")
    return passed, named


def ledger_ids(path):
    ids = set()
    row = re.compile(r"^\| ((?:" + ID + r")|SCN-[A-Z0-9]+-\d+) \|")
    with open(path, encoding="utf-8") as f:
        for line in f:
            m = row.match(line)
            if m:
                ids.add(m.group(1))
    return ids


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--spec", action="append", required=True)
    ap.add_argument("--ledger", required=True)
    ap.add_argument("--reports", required=True)
    ap.add_argument("--json", help="write the result as JSON to this file")
    a = ap.parse_args()

    reqs, scns = parse_spec(a.spec)
    passed, named = passed_tests(a.reports)
    problems = []

    cited = set()
    for ids in scns.values():
        cited |= ids
    for r in sorted(reqs - cited):
        problems.append(f"{r}: no scenario cites it")
    for s, ids in sorted(scns.items()):
        for i in sorted(ids - reqs):
            problems.append(f"{s}: cites {i}, which is not defined")
    for s in sorted(scns):
        if s not in passed:
            problems.append(f"{s}: no passing test in the reports")
    for test, s in sorted(set(named)):
        if s not in scns:
            problems.append(f"{test}: names {s}, which is not defined")

    ledger = ledger_ids(a.ledger)
    spec_ids = reqs | set(scns)
    for i in sorted(spec_ids - ledger):
        problems.append(f"{i}: not in the ledger")
    for i in sorted(ledger - spec_ids):
        problems.append(f"{i}: in the ledger, not in the specification")

    summary = {
        "requirements": len(reqs),
        "scenarios": len(scns),
        "scenarios_passed": len([s for s in scns if s in passed]),
        "tests": len({t for t, _ in named}),
        "problems": problems,
    }
    if a.json:
        with open(a.json, "w", encoding="utf-8") as f:
            json.dump(summary, f, indent=2)
    for p in problems:
        print("trace: " + p)
    print(
        f"trace: {summary['requirements']} requirements, {summary['scenarios']} scenarios, "
        f"{summary['scenarios_passed']} with a passing test, {len(problems)} problems"
    )
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
