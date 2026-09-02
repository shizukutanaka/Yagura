# Vulnerability Exploitability eXchange (VEX) Statements

This directory contains [OpenVEX](https://openvex.dev) statements about
vulnerabilities reported against Yagura's dependencies. Since Yagura uses
only the Go standard library (per [ADR-0001](../adr/0001-zero-dependencies.md)),
the only relevant CVEs are those affecting Go itself.

A VEX statement says one of four things about a given CVE in our context:

- **`not_affected`** — The vulnerable code path is not reachable in Yagura
- **`affected`** — Yagura is exploitable; users should upgrade
- **`fixed`** — A patched release is available
- **`under_investigation`** — Triage in progress

This satisfies OSPS-VM-04.02 (Level 3): non-applicable vulnerabilities
are documented in VEX form so downstream users can accurately assess risk.

## Format

We use OpenVEX 0.2.0 JSON format. Files are named
`vex-CVE-YYYY-NNNNN.json`, one CVE per file.

## Template

```json
{
  "@context": "https://openvex.dev/ns/v0.2.0",
  "@id": "https://github.com/shizukutanaka/yagura/docs/vex/vex-CVE-2025-XXXXX",
  "author": "Yagura maintainers",
  "timestamp": "2026-01-01T00:00:00Z",
  "version": 1,
  "statements": [
    {
      "vulnerability": {
        "@id": "https://nvd.nist.gov/vuln/detail/CVE-2025-XXXXX",
        "name": "CVE-2025-XXXXX"
      },
      "products": [
        { "@id": "pkg:github/shizukutanaka/yagura" }
      ],
      "status": "not_affected",
      "justification": "vulnerable_code_not_in_execute_path",
      "impact_statement": "The affected code path in the Go standard library is reachable only via X, which Yagura does not use."
    }
  ]
}
```

## Justification codes

OpenVEX defines these `justification` values for `not_affected`:

- `component_not_present`
- `vulnerable_code_not_present`
- `vulnerable_code_not_in_execute_path`
- `vulnerable_code_cannot_be_controlled_by_adversary`
- `inline_mitigations_already_exist`

## How to verify

Yagura validates these files natively — no external tooling, no network
(ADR-0001). The structural contract is specified in
[`../vex-spec.md`](../vex-spec.md).

```bash
# Lint every docs/vex/*.json (structure, status/justification enums, etc.)
yagura vex-audit --dir docs/vex

# CI gate: exit non-zero on any malformed or non-conformant file
yagura vex-audit --dir docs/vex --strict
```

This is what the `publish-gate` CI job runs. To build or merge statements
programmatically, use the `yagura_vex` MCP tool (pass `base` to merge a new
scan into an existing document without clobbering triage verdicts).

## Current statements

(none — Yagura v0.2.0 has not yet observed CVEs affecting Go that warrant
a VEX statement)
