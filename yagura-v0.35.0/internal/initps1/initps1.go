// Package initps1 generates init.ps1 — the PowerShell equivalent of init.sh
// for long-running coding-agent sessions on Windows.
//
// Motivation (v0.34.0):
//
//	yagura v0.33 introduced init.sh for Anthropic's 2-agent harness pattern.
//	It works great on macOS / Linux. On Windows, agents either need WSL or
//	they hit "/usr/bin/env sh: not found".
//
//	Windows is a first-class target for the sovereign computing stack — m
//	runs many projects on Windows directly (Loco, Wave DAW, Otedama bank
//	converters). The Initializer agent needs a native boot script there too.
//
// Design (ADR-0001 zero-dep):
//   - Pure function: initsh.BootSpec → PowerShell string (same input type
//     as initsh — one source of truth for project facts, two output formats)
//   - $ErrorActionPreference = 'Stop' for fail-fast (PowerShell's set -e)
//   - Get-Command for tool detection (cross-shell-version)
//   - Test-Path for file checks
//   - Native PowerShell logging (Write-Host with color hint)
//   - Idempotent — safe to re-run
//
// Reference (PS5+ compatible features only):
//
//	https://learn.microsoft.com/en-us/powershell/scripting/learn/ps101/01-getting-started
package initps1

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// BootSpec mirrors initsh.BootSpec — we intentionally duplicate the type
// rather than import initsh to keep this package cycle-free and to allow
// PowerShell-specific tweaks in the future without leaking them upstream.
type BootSpec struct {
	Project     string
	GeneratedAt time.Time
	GeneratedBy string

	WorkDir       string   // absolute Windows path (e.g. C:\dev\breeze)
	Language      string   // go / node / python / rust
	RequiredTools []string // checked with Get-Command
	RequiredFiles []string // checked with Test-Path
	BootCommands  []string // PowerShell statements, run in order
	HandoffFiles  []string // Get-Content at the end
}

// Generate renders init.ps1 as a string. Deterministic for the same input.
func Generate(spec BootSpec) string {
	var sb strings.Builder
	writeHeader(&sb, spec)
	writeStrictMode(&sb)
	writeWorkDir(&sb, spec)
	writeRequiredTools(&sb, spec)
	writeRequiredFiles(&sb, spec)
	writeLanguageChecks(&sb, spec)
	writeBootCommands(&sb, spec)
	writeHandoffDisplay(&sb, spec)
	writeFooter(&sb)
	return sb.String()
}

func writeHeader(sb *strings.Builder, spec BootSpec) {
	when := spec.GeneratedAt
	if when.IsZero() {
		when = time.Now().UTC()
	}
	by := spec.GeneratedBy
	if by == "" {
		by = "yagura"
	}
	proj := spec.Project
	if proj == "" {
		proj = "(unknown project)"
	}
	fmt.Fprintf(sb, "# init.ps1 — boot script for %s\n", proj)
	fmt.Fprintf(sb, "# Generated at %s by %s\n", when.UTC().Format(time.RFC3339), by)
	sb.WriteString("#\n")
	sb.WriteString("# Run at the start of every long-running coding-agent session on Windows.\n")
	sb.WriteString("# Verifies the environment, runs boot commands, then displays\n")
	sb.WriteString("# the latest cross-session handoff state.\n")
	sb.WriteString("#\n")
	sb.WriteString("# Idempotent — safe to run repeatedly.\n")
	sb.WriteString("# Compatible with PowerShell 5.1+ (the default on Windows 10/11).\n\n")
}

func writeStrictMode(sb *strings.Builder) {
	// $ErrorActionPreference = 'Stop' makes terminating-error behaviour the
	// default for cmdlets that would otherwise be non-fatal. Combined with
	// Set-StrictMode, this is the closest PowerShell gets to `set -eu`.
	sb.WriteString("$ErrorActionPreference = 'Stop'\n")
	sb.WriteString("Set-StrictMode -Version Latest\n\n")
	sb.WriteString("function Log([string]$msg) { Write-Host \"[init.ps1] $msg\" -ForegroundColor Cyan }\n")
	sb.WriteString("function Fail([string]$msg) {\n")
	sb.WriteString("  Write-Host \"[init.ps1] FAIL: $msg\" -ForegroundColor Red\n")
	sb.WriteString("  exit 1\n")
	sb.WriteString("}\n\n")
}

func writeWorkDir(sb *strings.Builder, spec BootSpec) {
	if spec.WorkDir == "" {
		sb.WriteString("Log \"Working in $((Get-Location).Path)\"\n\n")
		return
	}
	// Single-quote literal — PowerShell single quotes don't interpolate, so
	// $vars and backticks inside the path are taken literally. Single quotes
	// inside the path are doubled per PS literal-string rules.
	fmt.Fprintf(sb, "Set-Location -Path %s\n", psQuote(spec.WorkDir))
	fmt.Fprintf(sb, "Log %s\n\n", psQuote("Working in "+spec.WorkDir))
}

func writeRequiredTools(sb *strings.Builder, spec BootSpec) {
	if len(spec.RequiredTools) == 0 {
		return
	}
	tools := uniqueSorted(spec.RequiredTools)
	sb.WriteString("Log \"Checking required CLI tools…\"\n")
	for _, t := range tools {
		fmt.Fprintf(sb, "if (-not (Get-Command %s -ErrorAction SilentlyContinue)) { Fail %s }\n",
			psQuote(t), psQuote(t+" not in PATH"))
	}
	sb.WriteString("\n")
}

func writeRequiredFiles(sb *strings.Builder, spec BootSpec) {
	if len(spec.RequiredFiles) == 0 {
		return
	}
	files := uniqueSorted(spec.RequiredFiles)
	sb.WriteString("Log \"Checking required files…\"\n")
	for _, f := range files {
		fmt.Fprintf(sb, "if (-not (Test-Path -LiteralPath %s -PathType Leaf)) { Fail %s }\n",
			psQuote(f), psQuote("missing required file: "+f))
	}
	sb.WriteString("\n")
}

func writeLanguageChecks(sb *strings.Builder, spec BootSpec) {
	switch strings.ToLower(spec.Language) {
	case "go", "golang":
		sb.WriteString("Log \"Go: verifying module…\"\n")
		sb.WriteString("& go mod verify | Out-Null\n")
		sb.WriteString("if ($LASTEXITCODE -ne 0) { Fail \"go mod verify failed\" }\n\n")
	case "node", "nodejs", "javascript", "typescript":
		sb.WriteString("Log \"Node: verifying lockfile…\"\n")
		sb.WriteString("if (-not (Test-Path -LiteralPath 'package-lock.json' -PathType Leaf) -and\n")
		sb.WriteString("    -not (Test-Path -LiteralPath 'pnpm-lock.yaml' -PathType Leaf) -and\n")
		sb.WriteString("    -not (Test-Path -LiteralPath 'yarn.lock' -PathType Leaf)) {\n")
		sb.WriteString("  Fail \"no lockfile (package-lock.json / pnpm-lock.yaml / yarn.lock)\"\n")
		sb.WriteString("}\n\n")
	case "python":
		sb.WriteString("Log \"Python: verifying interpreter…\"\n")
		sb.WriteString("if (-not (Get-Command python -ErrorAction SilentlyContinue) -and\n")
		sb.WriteString("    -not (Get-Command python3 -ErrorAction SilentlyContinue)) {\n")
		sb.WriteString("  Fail \"python / python3 not found\"\n")
		sb.WriteString("}\n\n")
	case "rust":
		sb.WriteString("Log \"Rust: verifying Cargo.toml…\"\n")
		sb.WriteString("if (-not (Test-Path -LiteralPath 'Cargo.toml' -PathType Leaf)) { Fail 'Cargo.toml missing' }\n\n")
	}
}

func writeBootCommands(sb *strings.Builder, spec BootSpec) {
	if len(spec.BootCommands) == 0 {
		return
	}
	sb.WriteString("Log \"Running boot commands…\"\n")
	for _, c := range spec.BootCommands {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		// Log first, then execute the raw command. We use Invoke-Expression
		// instead of plain `c` because users may write shell-style pipelines
		// like "go mod download && make deps" that PowerShell parses
		// differently. Invoke-Expression treats the whole string as a
		// PowerShell statement.
		fmt.Fprintf(sb, "Log %s\n", psQuote("  > "+c))
		fmt.Fprintf(sb, "Invoke-Expression %s\n", psQuote(c))
		// Check $LASTEXITCODE only if the command was a native exe, but
		// also catch terminating errors via ErrorActionPreference=Stop.
		sb.WriteString("if ($LASTEXITCODE -ne $null -and $LASTEXITCODE -ne 0) {\n")
		fmt.Fprintf(sb, "  Fail %s\n", psQuote("command failed: "+c))
		sb.WriteString("}\n")
	}
	sb.WriteString("\n")
}

func writeHandoffDisplay(sb *strings.Builder, spec BootSpec) {
	if len(spec.HandoffFiles) == 0 {
		return
	}
	files := uniqueSorted(spec.HandoffFiles)
	sb.WriteString("Log \"Displaying handoff state…\"\n")
	for _, f := range files {
		fmt.Fprintf(sb, "if (Test-Path -LiteralPath %s -PathType Leaf) {\n", psQuote(f))
		fmt.Fprintf(sb, "  Write-Host \"`n=== %s ===`n\"\n", escapeForDoubleQuoted(f))
		fmt.Fprintf(sb, "  Get-Content -LiteralPath %s\n", psQuote(f))
		sb.WriteString("  Write-Host \"\"\n")
		sb.WriteString("}\n")
	}
	sb.WriteString("\n")
}

func writeFooter(sb *strings.Builder) {
	sb.WriteString("Log \"Boot complete. Coding agent: pick the topmost pending feature.\"\n")
}

// ─── helpers ──────────────────────────────────────────

// psQuote returns a PowerShell single-quoted literal.
//
// PowerShell rule: inside '...', a single quote is doubled (”). Backticks
// and $vars are taken literally (no interpolation). This is the safest
// quoting for paths and arbitrary strings.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// escapeForDoubleQuoted escapes a string for inclusion in a "..."-quoted
// PowerShell literal where interpolation is allowed but we want to suppress it.
//
// Used only inside writeHandoffDisplay for the "=== name ===" banner, which
// must remain a double-quoted string because we use the `n escape for newline.
func escapeForDoubleQuoted(s string) string {
	// $ → `$, " → ``", ` → ``
	s = strings.ReplaceAll(s, "`", "``")
	s = strings.ReplaceAll(s, "$", "`$")
	s = strings.ReplaceAll(s, "\"", "`\"")
	return s
}

// uniqueSorted removes duplicates and sorts ASCII-ascending.
//
// Mirrors initsh.uniqueSorted for consistent output between the two packages.
func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
