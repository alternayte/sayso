# The sayso image: the static binary with the embedded UI, on distroless, as a non-root user.
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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sayso /usr/local/bin/sayso
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/sayso"]
CMD ["serve"]
