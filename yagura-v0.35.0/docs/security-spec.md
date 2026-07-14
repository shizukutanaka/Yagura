# Yagura Security Specification

This document captures Yagura's threat model and the controls implemented
(or planned) in response. It complements [`SECURITY.md`](../SECURITY.md)
(vulnerability reporting policy) and the ADRs in [`docs/adr/`](adr/).

## Scope

Yagura runs on a single developer's local machine and manages metadata
about 20+ GitHub projects. The threat model is centered on:

- A single human operator who is the sole authorized user
- A local-bind HTTP daemon exposing MCP, dashboard, metrics
- An optional GitHub API connection (read-only)
- AI agents (Claude Code etc.) calling MCP tools with operator's intent

Multi-tenant SaaS scenarios, network-exposed deployments, and hostile
end-user devices are explicitly out of scope.

## Threat model (STRIDE)

| Threat | Asset | Likelihood | Impact | Control |
|---|---|---|---|---|
| **T1 Credential exfiltration** | GitHub PAT, MCP token, Anthropic key | Medium | CRIT | S0.1 token separation, S0.2 age-encrypted store (planned), `Config.String()` redaction |
| **T2 MCP supply chain** | Yagura process integrity | Low | CRIT | S2.2 MCP allowlist (planned), zero Go deps (ADR-0001) |
| **T3 Cross-project blast radius** | All 20+ projects | Low | CRIT | P1 read-only token, P6 compose-not-concentrate |
| **T4 Tool poisoning / prompt injection** | Operator's intent | Medium | HIGH | P1 read-default (Yagura cannot escalate), `mcp_audit` (tool-definition poisoning), **S4.3 `inject_scan` (indirect-injection scan of untrusted content, multilingual)** |
| **T5 Confused deputy** | GitHub API write surface | Low | HIGH | ADR-0005 no-write-back |
| **T6 AI generated logic vuln** | Yagura own source | Medium | HIGH | CodeQL SAST, govulncheck, 72% test coverage, fuzz tests |
| **T7 Stale secret** | Long-lived credentials | High | HIGH | S2.4 token rotation reminders (planned) |
| **T8 CI compromise** | Release artifacts | Low | HIGH | All Actions pinned to commit SHA, SLSA L3 provenance, signed releases |
| **T9 Insider error** | Local file state | High | MEDIUM | Atomic writes, append-only audit, daily backups recommended |
| **T10 Rug pull / typosquat** | (N/A: no deps) | N/A | N/A | ADR-0001 zero deps eliminates this category |
| **T11 Audit tampering** | Forensic integrity | Low | HIGH | S0.3 SHA-256 hash chain, file mode 0600 |
| **T12 DoS** | Local availability | Low | LOW | Loopback bind by default, body size limit (1 MB) |
| **T13 DNS rebinding / browser-to-localhost** | No-token loopback-trust mode (ADR-0004) | Medium | HIGH | ADR-0007 Origin header allow-list (`restrictOrigin`), applied uniformly to every route |

## Implemented controls (v0.2.0)

### Tier 0 — Automation prerequisites
- **S0.3**: Append-only audit log with SHA-256 hash chain
  - JSONL files at `~/.yagura/state/audit/YYYY-MM-DD.jsonl`
  - Each record contains `seq`, `prev_hash`, `hash`
  - O_APPEND mode + 0600 file permissions
  - `yagura verify` CLI command
  - 24-hour rotation, 90-day retention (configurable via `YAGURA_AUDIT_KEEP_DAYS`)

### Tier 4 — Self-defense
- Bearer token authentication via `crypto/subtle.ConstantTimeCompare` (timing-attack resistant)
- Refuses to start when bound to non-loopback without `YAGURA_MCP_TOKEN`
- Secret redaction in `Config.String()` output
- Path traversal prevention via strict slug regex
- Atomic file writes via temp + rename
- Body size limit on MCP endpoint (1 MB)
- Origin header allow-list against DNS rebinding / browser-to-localhost
  (ADR-0007): absent `Origin` (non-browser clients) or loopback `Origin` is
  allowed, any other `Origin` — including `"null"` — is rejected with 403,
  applied uniformly to every route via `withSecurityHeaders(restrictOrigin(mux))`

### Supply chain
- Zero external Go module dependencies (ADR-0001)
- All GitHub Actions pinned to full commit SHAs
- CodeQL SAST on every push, PR, and weekly
- OpenSSF Scorecard on every push and weekly
- govulncheck in CI
- SBOM (CycloneDX) generated on every release
- SLSA Level 3 provenance via `slsa-framework/slsa-github-generator`
- Sigstore keyless signing of release artifacts
- Reproducible builds verified by `make verify`

## Planned controls (roadmap)

See [`SECURITY.md`](../SECURITY.md) for the release roadmap. Each S* tag
refers to a section in the original security specification published in
the v0.1.0 ultrathink session.

### v0.3 — Tier 0 hardening
- **S0.1**: Credential separation with per-repo fine-grained PATs
- **S0.2**: age-encrypted local secret store
- **S0.4**: Yagura self-build with SLSA L3 (binary verifies its own provenance at startup)

### v0.4–v0.5 — Tier 1 observability
- **S1.1**: OpenSSF Scorecard automated polling for tracked projects
- **S1.2**: OSV.dev vulnerability scan as MCP tool
- **S1.3**: Secret leak detection via embedded gitleaks
- **S1.4**: SBOM continuous generation per tracked project
- **S1.5**: GitHub Actions hardening audit (zizmor-style ruleset)
- **S1.6**: SHA pin drift detection

### v0.6–v0.8 — Tier 2 policy enforcement
- **S2.1**: Pre-release gate (Scorecard ≥ 5, vuln zero, SBOM fresh)
- **S2.2**: MCP server allowlist with cosign verification
- **S2.3**: Branch protection drift monitoring
- **S2.4**: Token rotation tracking
- **S2.5**: Dependency cooldown (7-day delay) enforcement

### v0.9–v1.0 — Tier 3-4
- **S3.1**: Compromised dependency response (draft PR generation)
- **S3.2**: Stolen credential auto-revoke (single defensive write exception)
- **S3.3**: Rollback playbook enforcement
- **S4.1**: bubblewrap / seatbelt sandbox profile shipping
- **S4.2**: Semantic rate limiting on MCP tool calls
- **S4.3**: Prompt injection defense — **implemented** as `inject_scan` (deterministic,
  multilingual indirect-prompt-injection scan of untrusted content the agent ingests:
  instruction-override / exfiltration / hidden-text / encoding / data-confusion). The
  realistic goal per 2026 research is defense-in-depth with a deterministic policy
  *outside* the LLM — which is exactly Yagura's role (an LLM-independent gate layer)
- **S4.4**: Self-tamper detection at startup

## Trust boundaries

```
┌─────────────────────────────────────────────────────────────┐
│  Trusted: Operator's user account, Yagura binary itself     │
│  ────────────────────────────────────────────────────────   │
│                                                             │
│  ┌──────────────────────────────────────────────────────┐   │
│  │ Yagura process                                       │   │
│  │  - Receives MCP calls from Claude Code              │ ← │ Bearer token boundary
│  │  - Reads ~/.yagura/state/                           │   │
│  │  - Writes audit + drafts                            │   │
│  └──────────┬───────────────────────────────────────────┘   │
│             │                                               │
│             │ Read-only PAT                                 │
│             ▼                                               │
└─────────────│───────────────────────────────────────────────┘
              │
              │ HTTPS (TLS 1.2+, cert pinning to api.github.com)
              ▼
       ┌──────────────────────┐
       │  GitHub API          │  ← External, untrusted
       │  - Untrusted issues  │
       │  - Untrusted READMEs │
       └──────────────────────┘
```

## References

- ADR-0001: Zero external Go module dependencies
- ADR-0002: JSON files for state persistence
- ADR-0003: Append-only audit log with SHA-256 hash chain
- ADR-0004: Bearer token auth for MCP endpoint
- ADR-0005: Yagura never writes to GitHub or external systems
- ADR-0007: Origin header validation (DNS-rebinding mitigation)
- OpenSSF OSPS Baseline v2026.02.19
- MCP Security Best Practices (modelcontextprotocol.io)
- GitHub Actions 2026 Security Roadmap

## VEX (Vulnerability Exploitability eXchange)

Yagura's runtime dependency graph is exactly the Go standard library. When
CVEs are reported against Go itself, we publish VEX statements at
`docs/vex/` indicating whether each is exploitable in the context of
Yagura's usage. See [`docs/vex/README.md`](vex/README.md).

These statements are generated and validated **natively** — no external
tooling, no network (ADR-0001). The OpenVEX subset, the build/merge/validate
contract, the on-disk convention, and the CI gate are specified normatively in
[`docs/vex-spec.md`](vex-spec.md). The `publish-gate` CI job runs
`yagura vex-audit --dir docs/vex --strict`, so a malformed or non-conformant
VEX file fails the build before release (OSPS-VM-04.02, Level 3).
