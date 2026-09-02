# Contributing to Yagura

Thank you for considering a contribution. This document describes the requirements
and conventions for submitting changes.

## Ground rules

Yagura is intentionally minimal: zero dependencies, single binary, single user.
Before opening a pull request, please ensure your change preserves these properties.

- **Zero external Go module dependencies.** The standard library only.
  If you need a new dependency, open an Issue first to discuss the trade-off.
- **Single binary.** No split releases for daemon/CLI/etc.
- **Single user assumption.** Yagura is not multi-tenant. Do not add features
  that only make sense for teams (RBAC, SSO, etc.).
- **Read-default, write-explicit.** Yagura never writes to GitHub or external
  systems without explicit human approval. Pull requests that violate this
  principle will be rejected.

## Development setup

```bash
git clone https://github.com/shizukutanaka/yagura
cd yagura
go test ./...
```

## Pull request checklist

Before submitting, verify all of the following pass locally:

```bash
# 1. All tests pass with race detector
go test -race -count=1 ./...

# 2. Coverage stays at or above 70%
go test -coverprofile=cover.out ./... && go tool cover -func=cover.out | tail -1

# 3. Linting clean
golangci-lint run ./...

# 4. No vulnerabilities in dependencies (currently none, but verify)
go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# 5. Static analysis clean
go vet ./...

# 6. Build succeeds on linux/macos/windows
GOOS=linux   go build ./...
GOOS=darwin  go build ./...
GOOS=windows go build ./...
```

CI runs all of these automatically. PRs cannot be merged until all checks pass.

## Commit message convention

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <description>

<optional body>

<optional footer>
```

Types: `feat` / `fix` / `refactor` / `docs` / `test` / `chore` / `build` / `ci`
Scopes: `audit` / `mcp` / `scanner` / `registry` / `dashboard` / `config` / `cmd`

Breaking changes append `!` to type (e.g. `feat!:` / `fix!:`) and include a
`BREAKING CHANGE:` footer.

Example:
```
feat(audit): add Sigstore signing for daily log rotation

The audit log file is now signed with cosign on rotation. Signatures
are stored alongside as <date>.jsonl.sig.

Closes #42
```

## Code style

- Format with `gofmt -s`. No tabs/spaces debates.
- All exported identifiers must have godoc comments starting with the identifier name.
- Comments explain *why*, not *what*. Code already shows what.
- Error messages start lowercase and do not end with punctuation: `errors.New("registry: dir is required")`.
- Wrap errors with `%w` to preserve the chain.

## Test conventions

- Test files live next to the source: `foo.go` → `foo_test.go`.
- Use table-driven tests where multiple cases share structure.
- Test names follow `Test<Function>_<Condition>_<Expected>`: e.g. `TestAppend_RejectsEmptyKind`.
- For race-sensitive code, add explicit `-race` coverage in CI.
- Mock external dependencies (GitHub API, file system) via interface or `httptest`.

## Adding a new MCP tool

To add a new `yagura_*` MCP tool:

1. Define the handler in `internal/mcp/tools.go` as `buildXTool(d Deps) *Tool`.
2. Register it in `RegisterDefaultTools()`.
3. Add tests in `internal/mcp/tools_test.go` covering: happy path, missing
   required input, invalid input, downstream error.
4. Update README with the tool name and purpose.
5. Add an entry in CHANGELOG under `### Added`.

## Architecture decisions

Significant architectural changes require an ADR. Copy `docs/adr/0001-template.md`
and submit alongside the implementation PR.

## Security-sensitive changes

Changes touching the following require extra review:

- `internal/audit/` — audit log integrity is a security property
- `internal/config/` — secret handling
- `internal/mcp/server.go` — authentication
- `cmd/yagura/main.go` — startup security checks

Tag such PRs with the `security` label.

## License

By contributing, you agree that your contributions will be licensed under the
[MIT License](LICENSE).
