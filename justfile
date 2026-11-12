set shell := ["bash", "-euo", "pipefail", "-c"]

# List recipes
default:
    @just --list

# Install pinned tool versions and packages
setup:
    go mod download
    go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
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
    go test -race -count=1 ./...
    cd ui && bunx vitest run
    cd agent && npx vitest run

# Contract tests against the fakes (layer C)
contract:
    ./scripts/layer-test.sh contract

# Generate sqlc code, the OpenAPI documents, the TypeScript clients and the result schema
gen:
    sqlc generate
    go run ./cmd/sayso openapi > ui/openapi.json
    go run ./cmd/sayso openapi --doc runner > schemas/runner.openapi.json
    go run ./cmd/sayso openapi --doc result > schemas/result.schema.json
    cd agent && npx openapi-ts >/dev/null
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
    ./scripts/layer-test.sh e2e -timeout 30m
    "$root/tests/e2e/stack.sh" up "$envfile"
    cd tests/ui && E2E_ENV_FILE="$envfile" E2E_REPORT="$PWD/../../$reports/ui.json" bunx playwright test

# Build the sayso and sayso-agent images with the dev tag
images:
    docker build -q -f deploy/docker/Dockerfile -t sayso:dev . >/dev/null
    docker build -q -f deploy/agent/Dockerfile -t sayso-agent:dev . >/dev/null

# Image and Compose smoke tests (layer S)
smoke: images
    mkdir -p "$(just _reports)"
    ./tests/smoke/compose.sh "$(just _reports)/smoke.jsonl"

_reports:
    @if [ -d .build ]; then echo .build/reports; else echo tmp/reports; fi

# The full gate
ci: gen-check fmt-check lint test contract e2e smoke

# The owner's gate name; runs the same recipes as ci
check: ci
