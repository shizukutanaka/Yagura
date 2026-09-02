# Where the workflows live

GitHub registers workflows **only** from `.github/workflows/` at the **repository root** —
never here, beside the Go module. This project's `ci.yml`, `codeql.yml`, `release.yml` and
`scorecard.yml` are currently parked at `ci-workflows-pending/` in the repository root,
committed but inert, because the automation that prepared them authenticates as a GitHub App
without `workflows` permission and cannot write to `.github/workflows/`. See
`ci-workflows-pending/README.md` for the one command that activates them.

They used to be here. Because this directory sat inside a gitignored tree, they were never
committed and GitHub reported **zero registered workflows**: CI had never run, and
`release.yml` could not fire even with a correctly-spelled tag. Keeping a second copy next
to the module would reintroduce exactly that failure mode in a quieter form — two files
claiming to be the CI definition, one of which can never execute — so there is only one
copy, at the root, and `cmd/yagura/repotracked_test.go` fails if it is not tracked there.

The Go module is under `yagura-v0.35.0/`, so the root workflows set
`working-directory: yagura-v0.35.0` (per job in `release.yml`, whose `prepare`, `sign` and
`release` jobs never check the repository out).
