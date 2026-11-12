set shell := ["bash", "-euo", "pipefail", "-c"]

# List recipes
default:
    @just --list

# Install pinned tool versions and packages
setup:
    go mod download
    go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0

# Build the sayso binary into bin/
build:
    CGO_ENABLED=0 go build -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o bin/sayso ./cmd/sayso

# Check formatting
fmt-check:
    test -z "$(gofmt -l cmd internal)"

# Lint Go code, repository checks and placeholder words
lint: forbid
    go vet ./...
    golangci-lint run ./...
    for c in checks/*.sh; do "$c"; done

# Fail on placeholder words and private words
forbid:
    ./scripts/forbid.sh

# Unit and integration tests (layers U and I)
test:
    go test -race -count=1 ./...

# The full gate
ci: fmt-check lint test

# The owner's gate name; runs the same recipes as ci
check: ci
