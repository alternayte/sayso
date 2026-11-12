# The sayso-agent image: the worker with the process runner, and the agent runtime for Kubernetes
# Jobs. Debian slim with Node 22, git, ripgrep and mise, as a non-root user. mise has the current
# LTS versions of Go, Node and the .NET SDK preinstalled (§8.10).
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
COPY skills ./skills
COPY ui/embed.go ./ui/embed.go
COPY --from=ui /src/ui/dist ./ui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/sayso ./cmd/sayso

FROM node:22.23.5-bookworm-slim AS runtime
WORKDIR /opt/sayso/agent
COPY agent/package.json agent/package-lock.json ./
RUN npm ci --no-audit --no-fund --ignore-scripts
COPY agent/tsconfig.json agent/tsconfig.build.json ./
COPY agent/src ./src
RUN npx tsc -p tsconfig.build.json && npm prune --omit=dev --no-audit --no-fund
COPY agent/prompts ./prompts

FROM node:22.23.5-bookworm-slim
ARG MISE_VERSION=v2026.11.7
ARG GO_VERSION=1.26.11
ARG NODE_LTS=24.22.3
ARG DOTNET_SDK=10.0.404
ARG TARGETARCH
ENV MISE_DATA_DIR=/opt/mise \
    MISE_YES=1 \
    MISE_PARANOID=0 \
    GOTOOLCHAIN=local \
    DOTNET_CLI_TELEMETRY_OPTOUT=1 \
    DOTNET_NOLOGO=1
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git ripgrep libicu72 \
 && rm -rf /var/lib/apt/lists/* \
 && case "$TARGETARCH" in amd64) a=x64 ;; arm64) a=arm64 ;; *) echo "unsupported arch $TARGETARCH" >&2; exit 1 ;; esac \
 && curl -fsSL -o /usr/local/bin/mise "https://github.com/jdx/mise/releases/download/${MISE_VERSION}/mise-${MISE_VERSION}-linux-${a}" \
 && chmod 0755 /usr/local/bin/mise \
 && useradd --uid 10001 --create-home --shell /usr/sbin/nologin sayso \
 && mkdir -p /opt/mise && chown 10001:10001 /opt/mise
USER 10001:10001
RUN mise install "go@${GO_VERSION}" "node@${NODE_LTS}" "dotnet@${DOTNET_SDK}" \
 && mise use --global "go@${GO_VERSION}" "node@${NODE_LTS}" "dotnet@${DOTNET_SDK}" \
 && mkdir -p /opt/mise/default-config && cp /home/sayso/.config/mise/config.toml /opt/mise/default-config/config.toml \
 && rm -rf /home/sayso/.config /home/sayso/.cache
COPY --from=runtime --chown=0:0 /opt/sayso/agent /opt/sayso/agent
COPY --from=build /out/sayso /usr/local/bin/sayso
# The global mise defaults of the image; a repository's version files change them per run.
ENV MISE_GLOBAL_CONFIG_FILE=/opt/mise/default-config/config.toml
WORKDIR /home/sayso
ENTRYPOINT ["/usr/local/bin/sayso"]
CMD ["serve", "--role", "worker"]
