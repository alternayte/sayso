#!/usr/bin/env bash
# Runs go test with -json, writes the stream to <report> and prints the test
# output as text. The exit status is the one of go test.
# Usage: gotest.sh <report> [go test arguments]
set -euo pipefail
report="$1"; shift
mkdir -p "$(dirname "$report")"
go test -json "$@" | tee "$report" | python3 -u -c '
import json, sys
for line in sys.stdin:
    try:
        ev = json.loads(line)
    except ValueError:
        sys.stdout.write(line)
        continue
    if ev.get("Action") in ("output", "build-output"):
        sys.stdout.write(ev.get("Output", ""))
'
