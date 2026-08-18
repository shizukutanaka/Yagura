# Compatibility contract (v1)

Yagura reached **v1.0.0** after the cleanup releases v0.129.0–v0.131.0. This document says
exactly what v1 promises, what it does not, and which promises are enforced by tests rather
than by good intentions.

The short version: **if a promise here is not enforced by a test, treat it as an intention,
not a guarantee.** Most of them are enforced.

## What v1 guarantees

### MCP tool names
The 79 tool names published by v1.0.0 will not be removed or renamed in any v1.x release.

*Enforced by* `TestAPIStability_V1ToolsAreAllStillRegistered`, which holds the full list.
Additions are allowed — a larger tool set does not break an existing client — and are
reported by `TestAPIStability_AdditionsAreAllowedAndVisible` so they cannot land unnoticed.

### MCP tool inputs
Existing input fields keep their names, types and meaning. New fields may be added, but only
as **optional** ones: a call that was valid under v1.0.0 stays valid.

*Partially enforced.* The tool-name test does not inspect schemas; `docs/MCP_TOOLS.md` is
regenerated from the live registry on every release and its diff makes schema changes
visible in review.

### Protocol
MCP protocol version `2025-06-18`, JSON-RPC 2.0 over HTTP POST at `/mcp`. `initialize`
reports the real running version and only advertises capabilities that are actually wired
in — a capability is never claimed to satisfy a client.

### The trust base
Sensor data (`vuln_critical`, `ci_status`, `scorecard_score`, `latest_activity`, …) stays
writable **only by the scanner**. No MCP tool can forge a sensor value. `yagura_update`
accepts manual metadata only. This is a security property, not a convenience, and v1 will
not relax it.

### Zero external Go dependencies
`go.mod` requires only the standard library and `go.sum` is absent (ADR-0001). This holds for
every v1 release.

### Reproducible builds
`make verify` produces byte-identical binaries across runs with `-trimpath -buildvcs=false`
and `CGO_ENABLED=0`. Unbroken since v0.6.

### Local-first defaults
Binds `127.0.0.1`. Origin header validated on every route. Write endpoints require a bearer
token when one is configured, and enforce body size limits.

### Environment-variable requirements
A variable that is **optional** in a v1 release stays optional. Making a required variable
optional is backwards compatible (v1.2.0 did exactly that with `YAGURA_GITHUB_TOKEN`);
making an optional one required is **not**, and needs a major bump — it breaks every
working deployment at once, and does so at startup rather than at the call site.

Where a credential is genuinely needed, the tool that needs it says so **when it is called**.
The daemon does not demand credentials on behalf of features you are not using.

### CLI subcommand names
Existing subcommands keep their names and their `--flag` meanings. Output *formatting* is not
covered — see below.

### State and audit formats
State files stay readable by later v1 releases. The audit log stays append-only JSONL with
its hash chain; the chain is verifiable across upgrades.

## What v1 does **not** guarantee

- **Internal packages.** Everything under `internal/` is unimportable by design and may be
  restructured freely. `internal/lens`'s table, in particular, is expected to grow.
- **Human-readable CLI output.** Wording, column layout and ordering of the non-`--json`
  output may change. Parse `--json`, never the prose.
- **Prose inside JSON responses.** `note`, `summary` and `recommendation` strings are written
  for humans and models to read, not to match on. Their *presence* is stable; their wording
  is not.
- **Finding counts and scores.** Lens thresholds and scoring may be recalibrated as evidence
  accumulates — several releases exist precisely because a previous number was wrong. Treat
  a score as a reading, not a constant.
- **The HTML dashboard.** Markup, CSS variables and layout are free to change.
- **Research-derived metrics.** These follow the literature, and if a paper's method is
  applied incorrectly it will be corrected rather than preserved for compatibility. Such
  corrections are documented in the CHANGELOG with the old and new numbers side by side.

## Removed in v0.129.0 (before v1)

29 single-lens MCP tools were replaced by `yagura_lens`. Migration is mechanical:

```jsonc
// before
{"name": "yagura_nest_depth", "arguments": {"files": {"a.go": "<entire file>"}}}
// after
{"name": "yagura_lens", "arguments": {"lens": "nest_depth", "slug": "myproject"}}
```

The replacement takes a `slug` or `dir` and reads from disk, so source no longer passes
through the model's context. Omitting `lens` returns finding counts for all 29 with
one-line summaries.

This removal happened **before** 1.0 deliberately. It could not happen now.

## Changing something guaranteed

1. Bump the **major** version.
2. Update `v1ToolNames` in `cmd/yagura/apistability_test.go` in the same commit.
3. Document the migration in `CHANGELOG.md`, as v0.129.0 did.

Step 2 is manual on purpose. Having to edit the list is the moment you notice you are
breaking a promise.
