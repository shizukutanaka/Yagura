# Workflows waiting to be activated

These four files are this project's CI and release pipeline. **They are not running.**
GitHub executes workflows only from `.github/workflows/`, and nothing here is in that
directory, so nothing here executes.

## Why they are parked here

They were written long ago and lived at `yagura-v0.35.0/.github/workflows/` — inside a
directory excluded by `.gitignore`. So they were never committed, GitHub never registered
them, and the Actions API reported **zero workflows** for this repository. CI had never run
once; every gate this project claims was in practice run by hand. `release.yml` had never
fired either, which means the long-published explanation that publication was blocked by a
403 on tag push was wrong: there was nothing for a tag to trigger.

Moving them to `.github/workflows/` at the repository root is the fix. That push is refused:

```
refusing to allow a GitHub App to create or update workflow .github/workflows/ci.yml
  without `workflows` permission
```

The agent session that prepared them authenticates as a GitHub App without the `workflows`
permission. That is a deliberate control and is not something to work around — an inert copy
at a non-executing path is not an activation, it is just the work kept where it can be
reviewed and where an ephemeral container cannot lose it.

## Activating them

From a checkout authenticated as a principal that holds `workflows` permission:

```bash
mkdir -p .github/workflows
git mv ci-workflows-pending/*.yml .github/workflows/
git rm ci-workflows-pending/README.md
git commit -m "activate CI: register the workflows GitHub has never seen"
git push
```

`cmd/yagura/repotracked_test.go` enforces that the definitions have **exactly one home** —
activated, or parked here, never both and never neither — so the move above keeps the suite
green and the test starts guarding the live location instead.

## What to expect on the first run

`ci.yml`'s gates were executed locally before parking, because they have never run anywhere
else: `go vet` clean, `go test -race` green, coverage **85.7%** against its 75% threshold,
`go.sum` empty, zero `require` directives. That is evidence, not a promise — CI has its own
environment, and the first real run is the first real test of these files.

`release.yml` is different and should be treated as unverified: it triggers on a pushed tag,
tag push currently returns 403 from the automation, so it has never executed in any form.

The Go module lives under `yagura-v0.35.0/`, so the workflows set
`working-directory: yagura-v0.35.0`. `release.yml` sets it **per job**, because its
`prepare`, `sign` and `release` jobs never check the repository out and a workflow-level
default would point them at a directory that does not exist.
