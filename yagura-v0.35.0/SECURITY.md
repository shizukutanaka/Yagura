# Security Policy

## Reporting a Vulnerability

If you find a security vulnerability in Yagura, please report it privately:

1. **Preferred**: Use GitHub's [Private vulnerability reporting](https://github.com/shizukutanaka/yagura/security/advisories/new)
2. **Email**: open an issue without details requesting a contact channel (do not include details in public issues)

**Do not** open public Issues for security vulnerabilities.

## Response timeline

| Stage | Target |
|---|---|
| Acknowledgment | ≤ 48 hours |
| Initial assessment | ≤ 7 days |
| Patch ready (CRITICAL) | ≤ 14 days |
| Patch ready (HIGH) | ≤ 30 days |
| Patch ready (MEDIUM/LOW) | next minor release |
| Coordinated disclosure | 90 days from initial report |

## Severity classification

We follow CVSS v3.1 scoring.

| Severity | CVSS | Action |
|---|---|---|
| CRITICAL | ≥ 9.0 | Emergency release within 14 days |
| HIGH     | 7.0–8.9 | Next planned release, ≤ 30 days |
| MEDIUM   | 4.0–6.9 | Next minor release |
| LOW      | < 4.0 | Tracked, fixed in regular cycle |

## Threat model

Yagura is designed for single-developer use, running on the developer's local
machine. The threat model assumes:

- **In scope**: prompt injection through Issue/README/CVE descriptions read by Yagura,
  MCP tool poisoning, audit log tampering, credential exfiltration, supply chain
  attacks on Go module dependencies (currently: standard library only)
- **Out of scope**: physical access to the developer's machine, compromise of
  the host operating system, side-channel attacks on the local file system

For a complete threat model see [`docs/security-spec.md`](docs/security-spec.md).

## Supported versions

Yagura follows semantic versioning. Security updates are provided for:

| Version | Supported |
|---|---|
| 0.1.x (current) | ✓ until v0.3.0 |
| 0.0.x (pre-MVP) | ✗ |

End-of-life versions receive no security patches.

## Verification

All releases from v0.2.0 onward are signed with Sigstore. To verify:

```bash
cosign verify-blob \
  --certificate yagura-vX.Y.Z.crt \
  --signature yagura-vX.Y.Z.sig \
  --certificate-identity-regexp 'https://github.com/shizukutanaka/yagura/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  yagura-vX.Y.Z-linux-amd64
```

SBOM (CycloneDX format) is attached to every release for vulnerability analysis.

## Hardening recommendations for users

When running Yagura in production:

1. Bind to `127.0.0.1` only (default). Avoid `0.0.0.0` unless behind authentication.
2. Set `YAGURA_MCP_TOKEN` to a strong random value if exposing beyond loopback.
3. Use a fine-grained GitHub PAT with `metadata:read` only.
4. Run `yagura verify` periodically to detect audit log tampering.
5. Back up `~/.yagura/state/audit/` to immutable storage (S3 Object Lock, git remote).
6. Restrict file system access to `~/.yagura/` via tools like bubblewrap or seatbelt.
7. Rotate the GitHub PAT every 90 days.

## Disclosure history

No known vulnerabilities at this time.
