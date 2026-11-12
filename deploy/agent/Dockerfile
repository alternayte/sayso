# The sayso-agent image: the worker with the process runner, and the agent runtime for Kubernetes
# Jobs. Debian slim with Node 22, git, ripgrep and mise, as a non-root user.
FROM oven/bun:1.4.4 AS ui
WORKDIR /src/ui
COPY ui/package.json ui/bun.lock ./
RUN bun install --frozen-lockfile
COPY ui/ ./
RUN bunx vite build

FROM golang:1.26.11-bookworm AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY ui/embed.go ./ui/embed.go
COPY --from=ui /src/ui/dist ./ui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/sayso ./cmd/sayso

FROM node:22.23.5-bookworm-slim
ARG MISE_VERSION=v2026.11.7
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git ripgrep \
 && rm -rf /var/lib/apt/lists/* \
 && case "$TARGETARCH" in amd64) a=x64 ;; arm64) a=arm64 ;; *) echo "unsupported arch $TARGETARCH" >&2; exit 1 ;; esac \
 && curl -fsSL -o /usr/local/bin/mise "https://github.com/jdx/mise/releases/download/${MISE_VERSION}/mise-${MISE_VERSION}-linux-${a}" \
 && chmod 0755 /usr/local/bin/mise \
 && useradd --uid 10001 --create-home --shell /usr/sbin/nologin sayso
COPY --from=build /out/sayso /usr/local/bin/sayso
USER 10001:10001
WORKDIR /home/sayso
ENTRYPOINT ["/usr/local/bin/sayso"]
CMD ["serve", "--role", "worker"]
