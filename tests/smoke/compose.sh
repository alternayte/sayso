#!/usr/bin/env bash
# Layer S: start Compose with local images and the fakes profile, and check
# SCN-CORE-002 and SCN-CORE-011. Each check writes one JSON line to the report.
# Usage: compose.sh <report file>
set -euo pipefail
report="$(cd "$(dirname "${1:?report file}")" && pwd)/$(basename "$1")"
root="$(git rev-parse --show-toplevel)"
cd "$root/deploy/compose"
project="sayso-smoke-$$"
port=$((20000 + RANDOM % 20000))
fake_port=$((port + 1))
export SAYSO_IMAGE="${SAYSO_IMAGE:-sayso:dev}"
export SAYSO_AGENT_IMAGE="${SAYSO_AGENT_IMAGE:-sayso-agent:dev}"
export SAYSO_SECRET_KEY="$(openssl rand -base64 32)"
export SAYSO_SETUP_TOKEN="smoke-token"
export SAYSO_PORT="$port" SAYSO_FAKE_PORT="$fake_port"
compose=(docker compose -p "$project" --profile fakes)

cleanup() { "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

emit() { # emit <scn> <pass> <ms> [detail]
  printf '{"scn":"%s","pass":%s,"ms":%s,"detail":"%s"}\n' "$1" "$2" "$3" "${4:-}" >> "$report"
  echo "$1 pass=$2 ${4:-}"
}

fail=0
now() { python3 -c 'import time;print(int(time.time()*1000))'; }
start=$(now)
"${compose[@]}" up -d --pull never >/dev/null

# SCN-CORE-002: /readyz returns 200 within 180 s, and the port listens on 127.0.0.1 only.
ready=false
while (( $(now) - start < 180000 )); do
  if curl -fsS "http://127.0.0.1:${port}/readyz" >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
done
ms=$(( $(now) - start ))
binding=$(docker port "${project}-sayso-1" 8080/tcp | head -1)
if $ready && [[ "$binding" == 127.0.0.1:* ]]; then
  emit SCN-CORE-002 true "$ms" "ready, bound to $binding"
else
  emit SCN-CORE-002 false "$ms" "ready=$ready binding=$binding"
  "${compose[@]}" logs --no-color sayso worker | tail -40 >&2
  fail=1
fi

# SCN-CORE-011: temporal-ui publishes no port and cannot change workflows.
t0=$(now)
ports=$(docker port "${project}-temporal-ui-1" 2>/dev/null || true)
write=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "${project}-temporal-ui-1" | grep -c '^TEMPORAL_DISABLE_WRITE_ACTIONS=true$' || true)
if [[ -z "$ports" && "$write" == 1 ]]; then
  emit SCN-CORE-011 true "$(( $(now) - t0 ))" "no published port, write actions off"
else
  emit SCN-CORE-011 false "$(( $(now) - t0 ))" "ports=$ports write_off=$write"
  fail=1
fi
# SCN-CORE-004 (user part): both images run as a non-root user.
t0=$(now)
u1=$(docker inspect -f '{{.Config.User}}' "$SAYSO_IMAGE")
u2=$(docker inspect -f '{{.Config.User}}' "$SAYSO_AGENT_IMAGE")
if [[ -n "$u1" && "${u1%%:*}" != 0 && "$u1" != root && -n "$u2" && "${u2%%:*}" != 0 && "$u2" != root ]]; then
  emit SCN-CORE-004 true "$(( $(now) - t0 ))" "users $u1 and $u2"
else
  emit SCN-CORE-004 false "$(( $(now) - t0 ))" "users '$u1' and '$u2'"
  fail=1
fi
exit "$fail"
