# Where the workflows live

GitHub registers workflows **only** from `.github/workflows/` at the **repository root**,
so that is where this project's `ci.yml`, `codeql.yml`, `release.yml` and `scorecard.yml`
are — not here, beside the Go module.

They used to be here. Because this directory sat inside a gitignored tree, they were never
committed and GitHub reported **zero registered workflows**: CI had never run, and
`release.yml` could not fire even with a correctly-spelled tag. Keeping a second copy next
to the module would reintroduce exactly that failure mode in a quieter form — two files
claiming to be the CI definition, one of which can never execute — so there is only one
copy, at the root, and `cmd/yagura/repotracked_test.go` fails if it is not tracked there.

The Go module is under `yagura-v0.35.0/`, so the root workflows set
`working-directory: yagura-v0.35.0` (per job in `release.yml`, whose `prepare`, `sign` and
`release` jobs never check the repository out).
