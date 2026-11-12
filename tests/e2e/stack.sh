#!/usr/bin/env bash
# Starts or stops the e2e stack: Compose with local images and the fakes
# profile. "up" prints the environment that the tests need.
# Usage: stack.sh up <env file> | stack.sh down <env file>
set -euo pipefail
cmd="${1:?up or down}"
envfile="${2:?env file}"
root="$(git rev-parse --show-toplevel)"
compose_dir="$root/deploy/compose"

if [[ "$cmd" == down ]]; then
  # shellcheck disable=SC1090
  source "$envfile"
  export SAYSO_SECRET_KEY=unused E2E_GH_PRIVATE_KEY=unused
  docker compose -p "$E2E_PROJECT" --profile fakes -f "$compose_dir/compose.yaml" -f "$root/tests/e2e/compose.e2e.yaml" down -v --remove-orphans >/dev/null 2>&1 || true
  exit 0
fi

project="sayso-e2e-$$"
port=$((20000 + RANDOM % 20000))
key_file="$(mktemp)"
openssl genrsa -traditional -out "$key_file" 2048 2>/dev/null || openssl genrsa -out "$key_file" 2048 2>/dev/null
export SAYSO_IMAGE="${SAYSO_IMAGE:-sayso:dev}" SAYSO_AGENT_IMAGE="${SAYSO_AGENT_IMAGE:-sayso-agent:dev}"
export SAYSO_SECRET_KEY="$(openssl rand -base64 32)" SAYSO_SETUP_TOKEN="e2e-setup-token"
export SAYSO_PORT="$port" SAYSO_FAKE_PORT=9001 SAYSO_BASE_URL="http://127.0.0.1:$port"
export E2E_GH_PRIVATE_KEY="$(cat "$key_file")"
rm -f "$key_file"
cat > "$envfile" <<ENV
E2E_PROJECT=$project
E2E_BASE_URL=http://127.0.0.1:$port
E2E_FAKE_URL=http://github.fake.localhost:9001
E2E_SETUP_TOKEN=$SAYSO_SETUP_TOKEN
ENV
docker compose -p "$project" --profile fakes -f "$compose_dir/compose.yaml" -f "$root/tests/e2e/compose.e2e.yaml" up -d --pull never >/dev/null 2>&1
for _ in $(seq 1 180); do
  curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null 2>&1 && exit 0
  sleep 1
done
echo "the e2e stack did not become ready" >&2
docker compose -p "$project" --profile fakes -f "$compose_dir/compose.yaml" -f "$root/tests/e2e/compose.e2e.yaml" logs --no-color sayso | tail -40 >&2
exit 1
