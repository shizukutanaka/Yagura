# ADR-0002: JSON files for state persistence

- **Status**: Accepted
- **Date**: 2026-05-13
- **Deciders**: yagura maintainers

## Context

Yagura needs to persist:
- The list of registered projects (up to ~100)
- The per-project state (manual fields + auto-scanned fields)
- The audit log

We need to decide the persistence mechanism.

## Decision

Use **one JSON file per project** at `~/.yagura/state/projects/<slug>.json`,
plus daily JSONL files for audit logs at `~/.yagura/state/audit/`. All files
are mode 0600, directory 0700. Writes are atomic (temp file + rename).

In-memory cache is the read path; disk is for durability.

## Consequences

### Positive
- Human-readable. Emergency edits with any text editor.
- `git` works directly on the state directory (commit history, conflict
  resolution).
- `grep` works for ad-hoc queries.
- Atomic POSIX rename gives transaction-like semantics with zero dependencies.
- Backup is `cp -r` or `rsync`.
- 1 file/project keeps merge conflicts narrow when state is git-versioned.

### Negative
- O(N) scan to load all projects at startup (~10 ms for 100 projects, acceptable).
- No transactions across multiple files.
- Concurrent process writes need OS-level locking (not implemented; assumed
  single-daemon).
- Performance degrades past ~10k projects (not a concern for this use case).

### Neutral
- JSON is more verbose than binary formats, but disk usage is trivial.

## Alternatives considered

### Option A: SQLite
Powerful and well-supported, but adds a CGO dependency in most distributions,
violating ADR-0001. Pure-Go SQLite drivers exist but add ~50k LOC of dep.
Reconsider at >100 projects.

### Option B: Single JSON file
Simpler write logic, but every save rewrites the entire portfolio. Doesn't
play well with git diffs (whole-file changes for single-project updates).

### Option C: BoltDB / Badger / Pebble
Embedded KV stores. All add module dependencies. None gives human-readable
on-disk format.

### Option D (chosen): Per-project JSON files
Best balance of human-readability, git-friendliness, zero-dependencies, and
adequate performance for the target scale.
