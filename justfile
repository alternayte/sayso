set shell := ["bash", "-euo", "pipefail", "-c"]

# List recipes
default:
    @just --list

# Install pinned tool versions and packages
setup:
    go mod download
    cd ui && bun install --frozen-lockfile
    cd agent && npm ci --no-audit --no-fund
    cd tests/ui && bun install --frozen-lockfile && bunx playwright install chromium

# Build the UI into ui/dist, which the binary embeds
ui-build:
    cd ui && bunx vite build && touch dist/.keep

# Build the sayso binary into bin/
build: ui-build
    CGO_ENABLED=0 go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o bin/sayso ./cmd/sayso

# Check formatting
fmt-check:
    test -z "$(gofmt -l cmd internal ui)" || { gofmt -l cmd internal ui; exit 1; }
    cd ui && bunx prettier --check "src/**/*.{ts,tsx,css}" "*.ts" >/dev/null
    cd ui && bunx prettier --check --ignore-path /dev/null "../agent/src/*.ts" >/dev/null

# Lint Go code, repository checks and placeholder words
lint: forbid
    go vet ./...
    golangci-lint run --build-tags contract,e2e,k8s,smoke ./...
    for c in checks/*.sh; do "$c"; done
    cd ui && bunx tsc --noEmit && bunx eslint .
    cd agent && npx tsc --noEmit

# Fail on placeholder words and private words
forbid:
    ./scripts/forbid.sh

# Unit and integration tests (layers U and I)
test: ui-build
    ./scripts/gotest.sh "$(just _reports)/test.jsonl" -race -count=1 ./...
    # NFR-006 timing in the build without the race detector
    ./scripts/gotest.sh "$(just _reports)/test-timing.jsonl" -count=1 -run '^TestSCN_RUN_008' ./internal/runner/
    cd ui && bunx vitest run --reporter=default --reporter=json --outputFile.json="{{justfile_directory()}}/$(just _reports)/ui.vitest.json"
    cd agent && npx vitest run --reporter=default --reporter=json --outputFile.json="{{justfile_directory()}}/$(just _reports)/agent.vitest.json"

# Contract tests against the fakes (layer C)
contract:
    REPORTS="$(just _reports)" ./scripts/layer-test.sh contract

# Generate sqlc code, the OpenAPI documents, the TypeScript clients and the result schema
gen:
    go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
    go run ./cmd/sayso openapi > ui/openapi.json
    go run ./cmd/sayso openapi --doc runner > schemas/runner.openapi.json
    go run ./cmd/sayso openapi --doc result > schemas/result.schema.json
    go run ./cmd/sayso openapi --doc case > schemas/case.schema.json
    cd agent && npx --no-install openapi-ts >/dev/null
    cd ui && bunx openapi-ts >/dev/null && bunx vite build >/dev/null && touch dist/.keep

# Fail when generated code differs from the sources
gen-check: gen
    git diff --exit-code -- '*.gen.go' ui/openapi.json ui/src/client ui/src/routeTree.gen.ts schemas agent/src/protocol
    test -z "$(git ls-files --others --exclude-standard -- '*.gen.go' ui/openapi.json ui/src/client ui/src/routeTree.gen.ts schemas agent/src/protocol)" || { echo "gen-check: generated files are not added to git"; exit 1; }

# End-to-end tests against Compose with the fakes (layer E)
e2e: images
    #!/usr/bin/env bash
    set -euo pipefail
    reports="$(just _reports)"; mkdir -p "$reports"
    root="$PWD"; envfile="$(mktemp)"
    trap '"$root/tests/e2e/stack.sh" down "$envfile"; rm -f "$envfile"' EXIT
    REPORTS="$reports" ./scripts/layer-test.sh e2e -timeout 30m
    "$root/tests/e2e/stack.sh" up "$envfile"
    cd tests/ui && E2E_ENV_FILE="$envfile" E2E_REPORT="$PWD/../../$reports/ui.json" bunx playwright test

# Build the sayso and sayso-agent images with the dev tag
images:
    docker build -q -f deploy/docker/Dockerfile -t sayso:dev . >/dev/null
    docker build -q -f deploy/agent/Dockerfile -t sayso-agent:dev . >/dev/null

# Kubernetes runner and Helm chart on k3s (layer K)
k8s: images
    REPORTS="$(just _reports)" ./scripts/layer-test.sh k8s -timeout 40m

# Image and Compose smoke tests (layer S)
smoke: images
    mkdir -p "$(just _reports)"
    ./tests/smoke/compose.sh "$(just _reports)/smoke.jsonl"
    ./tests/smoke/release.sh "$(just _reports)/smoke.jsonl"

# Build the release artifacts without publishing (goreleaser snapshot)
release-snapshot:
    go run github.com/goreleaser/goreleaser/v2@v2.19.0 release --snapshot --clean --skip=sign

_reports:
    @if [ -d .build ]; then echo .build/reports; else echo tmp/reports; fi

# Replay the recorded workflow histories in tests/replay against the workflow code
replay:
    ./scripts/gotest.sh "$(just _reports)/replay.jsonl" -count=1 -run 'TestSCN_LIFE_012' ./internal/review/

# Record new workflow histories after a change to workflow code
replay-record:
    SAYSO_RECORD_REPLAY="$PWD/tests/replay" go test -count=1 -timeout 30m -run 'TestSCN_(LIFE|FND|PUB)' ./internal/review/

# The full gate
ci: gen-check fmt-check lint test replay contract e2e k8s smoke

# The owner's gate name; runs the same recipes as ci
check: ci

# The specification files: the private build directory when it exists
_specs:
    @if [ -f .build/SDD.md ]; then printf -- '--spec .build/SDD.md'; for f in .build/specs/*.md; do [ -f "$f" ] && printf -- ' --spec %s' "$f"; done; else echo "the private build directory is missing" >&2; exit 1; fi

# Requirement trace over the reports of the last gate run
trace:
    python3 scripts/trace.py $(just _specs) --ledger .build/ledger.md --reports "$(just _reports)" --json "$(just _reports)/trace.json"

# Run each gate of ci, then trace, then write the evidence of HEAD
verify:
    #!/usr/bin/env bash
    set -uo pipefail
    reports="$(just _reports)"; specs="$(just _specs)" || exit 1
    rm -rf "$reports"; mkdir -p "$reports"
    ok=0
    for g in gen-check fmt-check lint test replay contract e2e k8s smoke; do
      start=$(date +%s)
      if just "$g"; then pass=true; else pass=false; ok=1; fi
      printf '{"gate":"%s","pass":%s,"seconds":%s}\n' "$g" "$pass" "$(( $(date +%s) - start ))" >> "$reports/gates.jsonl"
    done
    just trace || ok=1
    python3 scripts/evidence.py write --out .build/evidence --gates "$reports/gates.jsonl" --trace "$reports/trace.json" $specs || ok=1
    exit $ok

# Pass when HEAD has evidence of a verify run with all gates passed
evidence-check:
    python3 scripts/evidence.py check --out .build/evidence $(just _specs)
