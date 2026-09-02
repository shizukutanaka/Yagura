# ADR-0001: Zero external Go module dependencies

- **Status**: Accepted
- **Date**: 2026-05-13
- **Deciders**: yagura maintainers

## Context

Yagura is a daemon that runs on developer machines and manages the security
posture of 23+ projects. Every external dependency it pulls in becomes part
of the developer's threat surface. The Trivy March 2026 supply chain attack
demonstrated that even widely-used Go modules can be compromised within hours
of release.

We need to decide whether to use third-party Go modules.

## Decision

Yagura uses **only the Go standard library**. No `go get` of any third-party
package is permitted in `go.mod`.

This applies to:
- HTTP client/server (use `net/http`)
- JSON (use `encoding/json`)
- Logging (use `log/slog`)
- Crypto (use `crypto/sha256` etc.)
- Concurrency (use `sync`, `context`)
- Testing (use `testing`, `net/http/httptest`)

Build tools (golangci-lint, govulncheck, etc.) are exempt — they are
developer tools, not runtime dependencies.

## Consequences

### Positive
- Supply chain attack surface reduced to Go itself.
- `go.sum` remains empty. Binary footprint stays under 10 MB.
- No transitive dependency upgrades to track.
- Reproducible builds are trivial (only Go version matters).
- The OSPS Baseline OSPS-QA-02.01 (dependency list) is trivially satisfied:
  the dependency list is empty.

### Negative
- More code to write internally (e.g., Prometheus exposition is ~180 LOC).
- Some convenience libraries (e.g. structured logging libraries, web frameworks)
  cannot be used.
- New contributors may attempt to add dependencies; CI must enforce this.

### Neutral
- Code style stays closer to standard Go idioms.

## Alternatives considered

### Option A: Allow vetted third-party deps
Permit dependencies on a curated allowlist (e.g., only golang.org/x/* and
google.golang.org/*). Rejected: even golang.org/x modules have had
supply-chain incidents. Allowlists drift over time.

### Option B: Allow any well-known dep
Permit popular packages (gorilla/mux, zap, viper). Rejected: this is exactly
the surface that supply chain attacks target. Yagura's value proposition
includes being a *security-oriented* tool; pulling in vulnerable dependencies
contradicts that.

### Option C (chosen): Zero deps
Selected for the security guarantees, the simplicity, and to align with the
Sovereign Computing Stack ethos.
