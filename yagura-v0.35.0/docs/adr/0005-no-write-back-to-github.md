# ADR-0005: Yagura never writes to GitHub or external systems

- **Status**: Accepted
- **Date**: 2026-05-13
- **Deciders**: yagura maintainers

## Context

Yagura observes the state of 23+ projects on GitHub. The natural next step
would be to give Yagura the ability to act: open Issues, comment on PRs,
revoke tokens, push commits, trigger Actions. Each of these is technically
straightforward.

The MCP security research (2025-2026) has documented "confused deputy"
attacks where AI agents with write capabilities are induced (via prompt
injection in Issue bodies, README files, vulnerability descriptions) into
performing actions the operator did not intend.

We need to decide Yagura's write capabilities.

## Decision

**Yagura never performs write operations against GitHub or any external
system without explicit human approval.**

Concretely:
- Yagura's GitHub PAT requires only `metadata:read` scope. It cannot push
  commits, open Issues, comment on PRs, modify settings, or perform any
  write API call. Even if compromised, the blast radius is read-only.
- Suggestions (e.g. "this dependency should be downgraded") are written to
  `~/.yagura/state/drafts/` as files. A human reviews the file and uses
  `gh` or `git` to apply if desired.
- The single exception is **automatic credential revocation** in response
  to detected secret leaks (security spec S3.2). This is a defensive write
  with very high signal-to-noise; the alternative (waiting for human
  approval while credentials are actively compromised) is worse.

This decision is enforced structurally, not by convention:
- The default `YAGURA_GITHUB_TOKEN` does not have write permissions.
- The code does not import HTTP write methods for endpoints other than
  the revocation endpoint.

## Consequences

### Positive
- Prompt injection cannot escalate to real-world side effects.
- Token compromise has read-only blast radius.
- Audit log shows what was *suggested*, distinct from what was *applied*.
- Aligns with the MCP Security 2026 best practice: "Agents should not be
  able to execute destructive or high-risk operations without a human
  checkpoint."

### Negative
- Yagura cannot fully automate workflows that other tools handle
  end-to-end (e.g., Renovate auto-merges minor dependency updates).
- Some operations have a small lag while waiting for human review.

### Neutral
- The "drafts" directory serves as a queue of pending suggestions, which
  is itself a useful artifact for workflow review.

## Alternatives considered

### Option A: Allow writes behind a feature flag
Yagura performs writes when `YAGURA_ALLOW_WRITE=true`. Rejected: feature
flags drift toward "always on" in practice; the safety property must be
structural.

### Option B: Allow writes within a per-tool allowlist
A few specific writes (e.g. "create Issue when CVE detected") permitted.
Rejected: each addition expands attack surface, and the simpler "drafts"
approach achieves the same result with human oversight.

### Option C (chosen): No writes by default, drafts only, one defensive exception
The safest configuration consistent with the use case.
