# ADR-0007: Origin header validation (DNS-rebinding mitigation)

- **Status**: Accepted
- **Date**: 2026-07-14
- **Deciders**: yagura maintainers

## Context

ADR-0004 established that Yagura runs with **no authentication** when
`YAGURA_MCP_TOKEN` is unset and `YAGURA_ADDR` binds to loopback
(`127.0.0.1` or `localhost`). That decision's threat model assumed the only
callers reaching a loopback-bound port are local, non-browser processes
(Claude Code, curl, MCP SDKs) run intentionally by the operator.

That assumption misses one caller class: **the operator's own web browser**,
driven by a malicious or compromised web page the operator has open in a
tab. Browsers do not restrict JavaScript from making requests to
`127.0.0.1`/`localhost` — same-origin policy blocks the *page* from reading
the *response*, but it does not block the *request* from being sent and
processed server-side. A hostile page can silently `fetch("http://127.0.0.1:8090/mcp", {method: "POST", body: ...})`
and drive `tools/call` against every tool Yagura exposes, purely from a tab
the operator happens to have open — the classic browser-to-localhost /
DNS-rebinding attack pattern. Neither ADR-0004 nor
`docs/security-spec.md`'s STRIDE table accounted for this: the loopback
bind was treated as sufficient isolation on its own, when in fact loopback
binding keeps *other machines* out but does nothing against *the operator's
own browser*.

This is the same class of gap the project has found and fixed before
(v0.105.0's `/hooks/*` endpoints having no auth despite the receiver's own
doc comment claiming one) — an invariant asserted in prose/ADR with no
corresponding runtime check.

## Decision

Reject any request whose `Origin` header is present and does not resolve to
a loopback host (`localhost`, `127.0.0.1`, `::1`).

- **No `Origin` header at all → allowed.** Non-browser MCP clients (CLI
  tools, SDKs, curl, Claude Code's MCP transport) never set this header;
  requiring it would break every legitimate caller.
- **`Origin` present and its host is loopback → allowed.** Covers the
  dashboard's own same-origin `fetch()` calls and any browser tab
  legitimately pointed at the daemon's own address.
- **`Origin` present and not loopback → rejected with 403.** Covers foreign
  web pages, browser extensions, and the `"null"` origin (sandboxed iframe
  / `data:` URL) — all untrusted contexts that should never reach the
  daemon.

Implemented as `originAllowed(origin string) bool` (a pure function) and a
`restrictOrigin` middleware (`cmd/yagura/security_headers.go`), composed
**inside** the existing `withSecurityHeaders` seam
(`withSecurityHeaders(restrictOrigin(mux))`, `cmd/yagura/main.go`) so it
applies uniformly to every route this daemon serves — dashboard, `/mcp`,
`/hooks/*`, the HTTP API, `/metrics` — not just `/mcp`. This mirrors how
`withSecurityHeaders` and the auth+body-limit rule (`docs/adr/0004`) were
already unified across the whole HTTP surface rather than duplicated
per-endpoint; a fix scoped to only `/mcp` would repeat the exact
narrow-scope mistake this project already corrected once for panic
recovery (v0.107.0 → v0.109.0).

This is a transport-level gate, independent of Bearer-token configuration:
it applies whether or not `YAGURA_MCP_TOKEN` is set, since a stolen or
guessed token doesn't change the fact that a foreign-origin browser request
has no legitimate reason to reach this daemon at all.

## Consequences

### Positive
- Closes the DNS-rebinding / browser-to-localhost gap in the exact mode
  (no-token, loopback-bind) ADR-0004 documented as the zero-config default.
- Zero-config: no new environment variable, no behavior change for any
  existing non-browser MCP client.
- Single seam (`withSecurityHeaders(restrictOrigin(mux))`) — every route
  gets the same protection with no per-endpoint duplication.
- Defense-in-depth: applies even when a Bearer token is configured, so a
  leaked token still can't be exploited by tricking the operator's browser
  into replaying it cross-origin (the browser wouldn't have the token
  either, but the check doesn't rely on that).

### Negative
- A browser extension or a second local tool that legitimately wants to hit
  the daemon from a non-loopback origin (rare) would need to either omit
  the `Origin` header (most non-fetch/XHR clients do) or be proxied through
  a loopback-origin page. No such use case exists in this project today.
- Adds one more thing to reason about when debugging connectivity issues
  (a request rejected with 403 rather than reaching the handler).

### Neutral
- Zero dependency: uses only `net/url` from the standard library
  (ADR-0001).

## Alternatives considered

### Option A: Require a token even on loopback
Would close the gap but reverses ADR-0004's zero-config-for-local-use
decision and breaks every existing local setup. Rejected.

### Option B: Bind to a Unix domain socket instead of TCP loopback
Eliminates the browser-reachability problem entirely (browsers cannot open
Unix sockets). Considered in ADR-0004 itself and deferred because it
complicates the dashboard's HTTP browser client. Still the strongest
long-term fix; Origin validation is the pragmatic interim control that
works within the existing TCP-loopback transport.

### Option C (chosen): Origin header allow-list, applied at the shared middleware seam
Zero-config, zero-dependency, applies uniformly to the whole HTTP surface,
and requires no change from any legitimate caller (non-browser clients
never set `Origin`).
