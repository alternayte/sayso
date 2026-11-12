#!/usr/bin/env bash
# Fails on placeholder words in source files and on private words in tracked
# files and commit messages. The word lists are scripts/forbid-words.txt and,
# when it exists, the private word list in the build directory.
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
cd "$root"
fail=0
src=$(git ls-files --cached --others --exclude-standard -- '*.go' '*.ts' '*.tsx' '*.js' '*.css' '*.sh' '*.sql' \
  | grep -v -e '^scripts/forbid' -e '/gen/' -e '\.gen\.' -e '_gen\.go$' -e '^ui/src/client/' || true)
while IFS= read -r w; do
  [[ -z "$w" ]] && continue
  if [[ -n "$src" ]] && hits=$(grep -nF -- "$w" $src); then
    echo "forbid: placeholder word '$w':" >&2
    echo "$hits" >&2
    fail=1
  fi
done < scripts/forbid-words.txt
private=".build/private-words.txt"
if [[ -f "$private" ]]; then
  tracked=$(git ls-files --cached --others --exclude-standard | grep -v '^\.build/' || true)
  while IFS= read -r w; do
    [[ -z "$w" || "$w" == \#* ]] && continue
    if [[ -n "$tracked" ]] && grep -qiF -- "$w" $tracked 2>/dev/null; then
      echo "forbid: a tracked file contains a private word" >&2
      grep -liF -- "$w" $tracked >&2
      fail=1
    fi
    if git log --format=%B | grep -qiF -- "$w"; then
      echo "forbid: a commit message contains a private word" >&2
      fail=1
    fi
  done < "$private"
fi
exit "$fail"
