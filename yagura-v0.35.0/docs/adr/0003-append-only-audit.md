# ADR-0003: Append-only audit log with SHA-256 hash chain

- **Status**: Accepted
- **Date**: 2026-05-13
- **Deciders**: yagura maintainers

## Context

Yagura's security spec (S0.3) requires tamper-evident audit logging. The
designer must be able to verify after-the-fact that no audit records have
been altered or removed.

We need a mechanism that:
1. Works on local disk (no cloud requirement)
2. Detects insertion, deletion, and modification of records
3. Does not require external infrastructure (zero dependencies)
4. Can be verified offline by a third party

## Decision

Use **JSON Lines files** at `~/.yagura/state/audit/YYYY-MM-DD.jsonl`, with each
record containing `seq`, `prev_hash`, and `hash` fields forming a SHA-256
hash chain.

```
record[N].hash      = SHA-256(JSON of record[N] with hash field empty)
record[N].prev_hash = record[N-1].hash
record[N].seq       = record[N-1].seq + 1
```

Files are opened with `O_APPEND | O_CREATE`, mode 0600. Each Write is followed
by an fsync. Hash chain continues across daemon restarts by tailing the
existing file.

Verification re-computes the hash for each record and validates the chain.

## Consequences

### Positive
- Modifying a single byte in any record breaks the chain at that point and
  all subsequent records.
- Deleting a record breaks `seq` continuity.
- Appending a fake record requires producing a valid `prev_hash`, which
  requires knowing the latest hash (which they would, but they need the
  exact format to match the SHA-256).
- Append-only at OS level (O_APPEND) prevents accidental overwrite.
- Verifiable with any SHA-256 implementation, offline.
- File mode 0600 prevents casual read by other users.

### Negative
- Not append-only against a privileged attacker (root can rewrite files).
  Mitigation: ship audit to an external git remote (future S0.3 extension).
- Hash chain is per-file, not across files. Mitigation: each file embeds
  date, and `Verify()` reports all files.
- fsync on every write adds latency (~1-5 ms per record on SSD). Acceptable
  given typical MCP call rates.

### Neutral
- The mechanism is a simple Merkle-list (no tree). Sigstore's Rekor uses
  Merkle trees for the same purpose at internet scale; we use the simpler
  variant because local-scale doesn't need O(log N) inclusion proofs.

## Alternatives considered

### Option A: Sign each record with Sigstore
Stronger guarantees (non-repudiation via OIDC identity), but requires
network for keyless signing, and Cosign as a runtime dependency.

### Option B: HMAC chain
Faster than SHA-256, but requires a secret key. Key management would itself
become a new attack target.

### Option C: External database with WORM (write-once-read-many)
e.g., AWS QLDB. Adds cloud dependency, violates the local-first principle.

### Option D (chosen): SHA-256 hash chain
Simple, verifiable, zero-dep, sufficient for the threat model (detecting
tampering rather than preventing it).

## Future work

Sigstore signing of the daily rotation point (sign each `YYYY-MM-DD.jsonl`
when the day closes) is on the roadmap. Pushing to a git remote with signed
commits gives external timestamping.
