#!/usr/bin/env bash
# Runs the Go tests of one layer. A layer test file starts with
# "//go:build <layer>". Only the tests in those files run, so the unit and
# integration tests do not run again.
# The go test -json stream goes to $REPORTS/<layer>.jsonl (default tmp/reports).
# Usage: layer-test.sh <layer> [extra go test flags]
set -euo pipefail
layer="$1"; shift
files=$(grep -rl --include='*_test.go' "^//go:build ${layer}\$" cmd internal tests 2>/dev/null || true)
if [[ -z "$files" ]]; then
  echo "layer ${layer}: no test files" >&2
  exit 1
fi
pkgs=$(for f in $files; do echo "./$(dirname "$f")"; done | sort -u)
names=$(grep -ho '^func \(Test[A-Za-z0-9_]*\)' $files | sed 's/^func //' | sort -u | paste -sd'|' -)
"$(dirname "$0")/gotest.sh" "${REPORTS:-tmp/reports}/${layer}.jsonl" -tags "$layer" -count=1 -race "$@" -run "^(${names})\$" $pkgs
