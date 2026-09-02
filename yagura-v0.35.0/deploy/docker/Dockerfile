# syntax=docker/dockerfile:1.7
#
# Multi-stage build for Yagura:
#   - Stage 1: build the static binary with -trimpath -buildvcs=false
#   - Stage 2: copy into scratch (no shell, no libc, no anything)
#
# Result: ~8 MB container with attack surface = the binary only.
# Verified reproducible: SHA-256 matches `make verify` output.

# ─── builder ─────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# Pin to specific version for reproducible builds
WORKDIR /src

# go.mod / go.sum first to maximize cache hit
COPY go.mod go.sum* ./

# Vendor empty go.sum is allowed by ADR-0001 (no external deps)
RUN if [ -f go.sum ]; then go mod download; fi

COPY . .

# Static build flags:
#   -trimpath:        strip absolute paths from binary (reproducibility)
#   -buildvcs=false:  exclude VCS info (reproducibility)
#   -s -w:            strip symbol table + DWARF debug info (smaller binary)
#   CGO_ENABLED=0:    no libc dependency (works on scratch image)
#   GOOS=linux:       explicit target
ARG VERSION=0.5.0
RUN CGO_ENABLED=0 GOOS=linux \
    go build \
      -trimpath \
      -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /yagura \
      ./cmd/yagura

# ─── runtime ─────────────────────────────────────────────────
FROM scratch

# CA certificates for outbound HTTPS to api.github.com / api.osv.dev / api.scorecard.dev
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Timezone data (Yagura logs use RFC3339 with UTC, but services may need TZ)
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Non-root user (uid 65532 = nobody equivalent for distroless conventions)
# In scratch we can't create users at runtime, so we declare uid:gid statically
USER 65532:65532

COPY --from=builder /yagura /yagura

# Default state dir under /home/nonroot — caller should mount a volume here
ENV YAGURA_STATE_DIR=/data
ENV YAGURA_ADDR=0.0.0.0:8080

EXPOSE 8080

# OCI labels for provenance
LABEL org.opencontainers.image.title="yagura"
LABEL org.opencontainers.image.description="Portfolio orchestrator daemon for GitHub projects via MCP"
LABEL org.opencontainers.image.source="https://github.com/shizukutanaka/yagura"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.vendor="Sovereign Computing Stack"
LABEL org.opencontainers.image.base.name="scratch"

ENTRYPOINT ["/yagura"]
