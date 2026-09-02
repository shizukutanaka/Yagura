# syntax=docker/dockerfile:1.10
#
# Multi-stage Dockerfile for Yagura. Final image uses Google distroless static.
#
# Why distroless static:
#   - No package manager, no shell, no busybox, no glibc/musl
#   - Smallest possible attack surface (~2MB base + 8MB Yagura)
#   - Read-only filesystem compatible
#   - Reproducible builds (Bazel-built, pinned by digest)
#
# Build:
#   docker build -t yagura:dev .
#
# Run:
#   docker run -d --read-only \
#     -e YAGURA_GITHUB_TOKEN=ghp_... \
#     -v yagura-state:/state \
#     -p 127.0.0.1:8090:8090 \
#     -e YAGURA_STATE_DIR=/state \
#     -e YAGURA_ADDR=0.0.0.0:8090 \
#     -e YAGURA_MCP_TOKEN=$RANDOM_TOKEN \
#     yagura:dev

# ─── Stage 1: build ──────────────────────────────────────────
FROM golang:1.22.10-alpine3.21@sha256:c694a4d291a13a9f9d94933395673494fc2cc9d4777b85928d1ef1e1f7d20e0b AS build

WORKDIR /src
COPY go.mod ./
# go.sum is intentionally absent: Yagura has zero dependencies (ADR-0001).
RUN test ! -f go.sum || (echo "go.sum should not exist (zero deps)" && exit 1)

COPY . .

# Inject version via -ldflags at build time
ARG VERSION=0.1.0

RUN CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) \
    go build \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -trimpath \
      -o /out/yagura \
      ./cmd/yagura \
 && /out/yagura version

# ─── Stage 2: runtime ────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot@sha256:6ec5aa99dc335666e79dc64e4a6c8b89c33a543a1967f20d360922a80dd21f02

# OCI labels for registry discoverability
LABEL org.opencontainers.image.title="Yagura" \
      org.opencontainers.image.description="Portfolio orchestrator daemon" \
      org.opencontainers.image.url="https://github.com/shizukutanaka/yagura" \
      org.opencontainers.image.source="https://github.com/shizukutanaka/yagura" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.vendor="shizukutanaka"

COPY --from=build /out/yagura /usr/local/bin/yagura

# Distroless nonroot is uid 65532; state dir must be writable by it.
USER nonroot:nonroot
WORKDIR /home/nonroot

EXPOSE 8090

ENV YAGURA_STATE_DIR=/home/nonroot/.yagura/state \
    YAGURA_ADDR=0.0.0.0:8090

# Liveness check uses the binary itself (no curl/wget available in distroless)
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/yagura", "version"]

ENTRYPOINT ["/usr/local/bin/yagura"]
