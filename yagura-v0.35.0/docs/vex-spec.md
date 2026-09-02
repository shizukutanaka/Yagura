# Yagura VEX Specification

Status: **Normative** for `internal/vex`, the `yagura_vex` MCP tool, the
`yagura vex-audit` CLI, and the `docs/vex/` artifact directory.
Version: 1.0 (aligns with Yagura v0.35.0).

This document specifies how Yagura **produces, maintains, and validates**
[OpenVEX](https://openvex.dev) (Vulnerability Exploitability eXchange)
documents. It complements [`security-spec.md`](security-spec.md) (which
*mandates* publishing VEX statements) and [`vex/README.md`](vex/README.md)
(the on-disk convention). Where this document and those disagree, this
document wins for the implementation; `vex/README.md` wins for the file-naming
convention.

Key words **MUST**, **SHOULD**, **MAY** are used per RFC 2119.

## 1. Motivation

An SBOM answers *"what is in this product"*. It does **not** answer *"which of
the CVEs against those components actually matter here"* — so SBOM-only
consumers over-report risk (arXiv 2511.20313). VEX closes that gap: for each
vulnerability it asserts one of four statuses in the product's context, with a
machine-readable justification. Because Yagura depends only on the Go standard
library (ADR-0001), the vulnerabilities it must speak to are CVEs against Go
itself, expressed at the subcomponent granularity (e.g. `pkg:golang/net/http`).

Yagura does **not** decide exploitability with an LLM or a reachability engine.
VEX is a *producer claim*; the producer is responsible for its truth. Yagura's
job is narrow and deterministic: build well-formed documents, maintain them
across scans without losing human triage, and lint them structurally.

## 2. Conformance target

Documents conform to **OpenVEX `https://openvex.dev/ns/v0.2.0`**. Yagura
implements the subset below. Unknown fields in an input document are ignored
on validation (lenient parse) — they are neither required nor rejected.

## 3. Data model

A **Document** is:

| Field | JSON | Required | Notes |
|---|---|---|---|
| Context | `@context` | yes | MUST be `https://openvex.dev/ns/v0.2.0` for generated docs |
| ID | `@id` | yes (generated) | see §6 for the deterministic form |
| Author | `author` | yes | defaults to `yagura` |
| Timestamp | `timestamp` | yes | RFC 3339, UTC |
| Version | `version` | yes | integer ≥ 1 |
| Tooling | `tooling` | no | free-text tool identifier |
| Statements | `statements` | yes (≥ 1) | see below |

A **Statement** is:

| Field | JSON | Required | Notes |
|---|---|---|---|
| Vulnerability | `vulnerability` | yes | `{ "@id"?, "name", "description"? }` — `name` required |
| Products | `products` | no | each `{ "@id", "subcomponents"? }`; `@id` required when present |
| Status | `status` | yes | one of the four §4 enums |
| Justification | `justification` | conditional | required-or-impact for `not_affected` (§4) |
| ImpactStatement | `impact_statement` | conditional | alternative to justification for `not_affected` |
| ActionStatement | `action_statement` | SHOULD | remediation guidance for `affected` |

A **Subcomponent** is `{ "@id": "<purl>" }` — the specific dependency inside the
product that carries the vulnerable code (Yagura's primary use: Go stdlib pkgs).

## 4. Status & justification enums

`status` MUST be one of:

- **`not_affected`** — the vulnerable code path is not reachable in this product.
- **`affected`** — the product is exploitable; consumers should remediate.
- **`fixed`** — a patched release is available.
- **`under_investigation`** — triage is in progress (the default for a bare CVE).

`justification` (only meaningful for `not_affected`) MUST, when present, be one
of the five OpenVEX values:
`component_not_present`, `vulnerable_code_not_present`,
`vulnerable_code_not_in_execute_path`,
`vulnerable_code_cannot_be_controlled_by_adversary`,
`inline_mitigations_already_exist`.

## 5. Operations (`internal/vex`)

### 5.1 `Build(author, now, statements) Document`
Constructs a canonical document. It MUST:
- default empty `author` to `yagura`;
- default empty `status` to `under_investigation`;
- trim `vulnerability.name`;
- sort statements stably by `(vulnerability.name, first product @id)`;
- set `version = 1`, `timestamp = now.UTC()` (RFC 3339);
- set `@id` per §6.

### 5.2 `Merge(base, additions, now) Document`
Maintains a document across re-scans. It MUST:
- add a statement for each addition whose `vulnerability.name` is **not** already
  present in `base` (defaulting status to `under_investigation`);
- **never** modify or remove an existing statement — operator verdicts
  (`not_affected` / `fixed` / `affected`) are preserved;
- if nothing new is added, return `base` **unchanged** (idempotent: same `@id`,
  same `version`, same `timestamp`);
- otherwise re-sort, set `version = base.version + 1`, recompute `timestamp` and
  `@id`. A missing `@context`/`author` on `base` is backfilled with the defaults.

### 5.3 `Validate(doc) []string`
Returns structural problems (empty slice ⇒ conformant). It MUST flag:
- missing `@context`;
- zero statements;
- a statement missing `vulnerability.name`;
- an invalid `status`;
- `not_affected` lacking both a justification and an impact_statement;
- a present-but-invalid `justification`;
- `affected` lacking an `action_statement`;
- a product missing its `@id`.

`Validate` is **advisory about exploitability** — it never asserts a CVE *is* or
*isn't* applicable; it only checks that the claim is well-formed.

### 5.4 `ParseAndValidate(data) (Document, []string, error)`
Unmarshals OpenVEX JSON and runs `Validate`. Returns an `error` **only** when the
bytes are not valid JSON (lint impossible); structural problems come back as the
`[]string`. This is the primitive behind `yagura vex-audit`.

## 6. Determinism

`@id` MUST be `urn:yagura:vex:<h>` where `<h>` is the 8-hex FNV-1a/32 hash of
`author ++ timestamp ++ json(statements)`. Given identical inputs (including
`now`), `Build`/`Merge` MUST produce byte-identical documents. Statement order
MUST be deterministic (the §5.1 sort). This lets reproducible-build and
regression tests compare output directly.

## 7. On-disk artifacts (`docs/vex/`)

Per [`security-spec.md`](security-spec.md) §VEX (OSPS-VM-04.02, Level 3), Yagura
publishes VEX statements about Go-stdlib CVEs in its own context. Files:

- live under `docs/vex/`;
- are OpenVEX v0.2.0 JSON;
- are named `vex-CVE-YYYY-NNNNN.json`, one CVE per file (per `vex/README.md`);
- `example.json` is a non-shipping reference and is also validated.

Every `*.json` under `docs/vex/` MUST pass `Validate` (§5.3).

## 8. Surfaces

| Surface | Form | Use |
|---|---|---|
| MCP `yagura_vex` | `{ author?, tooling?, base?, statements:[{cve, vuln_id?, description?, product?, subcomponents?, status?, justification?, impact?, action?}] }` → `{ document, issues }` | agent/programmatic build & merge; `base` present ⇒ `Merge`, else `Build` |
| CLI `yagura vex-audit [dir]` | `--dir docs/vex` (default), `--strict`, `--json` | lint on-disk VEX files; `--strict` exits non-zero on any failure |

Per repo convention, disk-walking audits (`skill-audit`, `mcp-audit`, …) are
**CLI-only**; `vex-audit` follows suit and does not add an MCP tool.

## 9. CI enforcement

The `publish-gate` job runs `yagura vex-audit --dir docs/vex --strict` so a
malformed or non-conformant VEX file (bad status, missing justification, broken
JSON) fails the build before release — keeping the published `docs/vex/` honest.

## 10. Out of scope

- Exploitability *determination* (reachability analysis, LLM judgement).
- VEX formats other than OpenVEX (CSAF/CycloneDX-VEX).
- Doc-level fields Yagura does not model (e.g. `role`) — tolerated on input,
  not emitted.
- Cryptographic signing of VEX docs (covered by release-level SLSA/Sigstore).

## References

- OpenVEX specification v0.2.0 — https://openvex.dev
- CISA, *Minimum Requirements for Vulnerability Exploitability eXchange (VEX)*
- OpenSSF OSPS Baseline — OSPS-VM-04.02
- ADR-0001 (zero dependencies), [`security-spec.md`](security-spec.md),
  [`vex/README.md`](vex/README.md)
- arXiv 2511.20313 — SBOM reality-check
