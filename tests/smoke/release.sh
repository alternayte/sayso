#!/usr/bin/env bash
# Layer S: build a goreleaser snapshot and check SCN-CORE-010 and SCN-CORE-004
# (architectures, users, sizes). Each check writes one JSON line to the report.
# Usage: release.sh <report file>
set -euo pipefail
report="$(cd "$(dirname "${1:?report file}")" && pwd)/$(basename "$1")"
root="$(git rev-parse --show-toplevel)"
cd "$root"

emit() { # emit <scn> <pass> <ms> [detail]
  printf '{"scn":"%s","pass":%s,"ms":%s,"detail":"%s"}\n' "$1" "$2" "$3" "${4:-}" >> "$report"
  echo "$1 pass=$2 ${4:-}"
}
now() { python3 -c 'import time;print(int(time.time()*1000))'; }
fail=0

t0=$(now)
go run github.com/goreleaser/goreleaser/v2@v2.19.0 release --snapshot --clean --skip=sign >&2
version=$(python3 -c 'import json;print(json.load(open("dist/metadata.json"))["version"])')

# SCN-CORE-010: binaries, both images, the chart package, the Compose assets and checksums.
missing=()
for os in linux darwin; do
  for arch in amd64 arm64; do
    [[ -f "dist/sayso_${version}_${os}_${arch}.tar.gz" ]] || missing+=("archive ${os}/${arch}")
  done
done
for f in "sayso-${version}.tgz" compose.yaml .env.example; do
  [[ -f "tmp/release/$f" ]] || missing+=("$f")
  grep -q "  ${f}\$" dist/checksums.txt || missing+=("checksum of $f")
done
for img in sayso sayso-agent; do
  for arch in amd64 arm64; do
    docker image inspect "ghcr.io/alternayte/${img}:${version}-${arch}" >/dev/null 2>&1 || missing+=("image ${img} ${arch}")
  done
done
if (( ${#missing[@]} == 0 )); then
  emit SCN-CORE-010 true "$(( $(now) - t0 ))" "snapshot ${version}: archives, images, chart, Compose assets and checksums"
else
  emit SCN-CORE-010 false "$(( $(now) - t0 ))" "missing: ${missing[*]}"
  fail=1
fi

# SCN-CORE-004: each image exists for amd64 and arm64, runs as non-root, and meets NFR-003 on amd64.
t0=$(now)
problems=()
limit_sayso=$((60 * 1000 * 1000))
limit_agent=$((3 * 1000 * 1000 * 1000))
for img in sayso sayso-agent; do
  for arch in amd64 arm64; do
    ref="ghcr.io/alternayte/${img}:${version}-${arch}"
    got=$(docker image inspect -f '{{.Architecture}}' "$ref" 2>/dev/null || echo none)
    [[ "$got" == "$arch" ]] || problems+=("$ref architecture $got")
    user=$(docker image inspect -f '{{.Config.User}}' "$ref" 2>/dev/null || echo "")
    if [[ -z "$user" || "${user%%:*}" == 0 || "$user" == root ]]; then problems+=("$ref user '$user'"); fi
  done
done
size_sayso=$(docker image inspect -f '{{.Size}}' "ghcr.io/alternayte/sayso:${version}-amd64" 2>/dev/null || echo 0)
size_agent=$(docker image inspect -f '{{.Size}}' "ghcr.io/alternayte/sayso-agent:${version}-amd64" 2>/dev/null || echo 0)
(( size_sayso > 0 && size_sayso <= limit_sayso )) || problems+=("sayso size ${size_sayso}")
(( size_agent > 0 && size_agent <= limit_agent )) || problems+=("sayso-agent size ${size_agent}")
if (( ${#problems[@]} == 0 )); then
  emit SCN-CORE-004 true "$(( $(now) - t0 ))" "amd64 and arm64, non-root, sizes ${size_sayso} and ${size_agent} bytes"
else
  emit SCN-CORE-004 false "$(( $(now) - t0 ))" "${problems[*]}"
  fail=1
fi
exit "$fail"
