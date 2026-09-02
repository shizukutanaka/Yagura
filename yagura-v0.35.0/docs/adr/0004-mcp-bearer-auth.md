# ADR-0004: Bearer token auth for MCP endpoint

- **Status**: Accepted
- **Date**: 2026-05-13
- **Deciders**: yagura maintainers

## Context

Yagura exposes `/mcp` as a JSON-RPC 2.0 HTTP endpoint that any process on
the machine (or network, if exposed) can call. The MCP specification's 2025
revision requires OAuth 2.1 with PKCE for *remote* MCP servers but is
flexible for local ones.

Yagura is designed for single-user, local-bind use. We need to decide what
authentication is appropriate.

## Decision

Use **Bearer token authentication** via the `YAGURA_MCP_TOKEN` environment
variable.

- If `YAGURA_MCP_TOKEN` is unset and `YAGURA_ADDR` is loopback (`127.0.0.1`
  or `localhost`), the server runs **without authentication**.
- If `YAGURA_MCP_TOKEN` is set, every request to `/mcp` must include
  `Authorization: Bearer <token>`.
- If `YAGURA_ADDR` binds to a non-loopback interface (`0.0.0.0`, public IP,
  hostname) and `YAGURA_MCP_TOKEN` is unset, the daemon **refuses to start**.

Token comparison uses `crypto/subtle.ConstantTimeCompare` to prevent timing
attacks where an attacker measures response time differences to discover
the token byte-by-byte.

## Consequences

### Positive
- Zero-config for typical local use (no token to set).
- One environment variable to enable remote access safely.
- The "no token + public bind" combination is structurally impossible to
  reach unintentionally — the daemon refuses to start.
- No OAuth dance for a single-user tool.

### Negative
- Bearer tokens are bearer credentials. Anyone who reads them can use them.
  Mitigation: store in age-encrypted secrets store (ADR-0006, future).
- No per-tool scope: any caller with the token can call any tool.
  Mitigation: HITL principle (ADR-0005) means no destructive tools exist.
- Token rotation is manual (restart with new token).

### Neutral
- Standard Bearer scheme works with curl, Claude Code, and any HTTP client.

## Alternatives considered

### Option A: mTLS
Strong, but requires certificate management. Overkill for local use.

### Option B: Unix domain socket
Eliminates network exposure entirely. Considered for v1.0; complicates the
dashboard which uses an HTTP browser client.

### Option C: OAuth 2.1 + PKCE
Required by the MCP spec for *remote* servers. Yagura is local, so this is
not mandated. Adds substantial complexity (token server, refresh flows).

### Option D (chosen): Bearer + public-bind safety check
Adequate for local use; safely degrades for remote use; refuses unsafe
configurations.
