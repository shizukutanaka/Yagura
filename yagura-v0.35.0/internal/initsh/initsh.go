// Package initsh generates init.sh — the Initializer agent's environment boot
// script from Anthropic's 2-agent long-running harness pattern.
//
// Motivation (v0.33.0):
//
//	Anthropic "Effective harnesses for long-running agents" (2026):
//	  "An initializer agent runs once at the start of a project to set up the
//	   environment, expand the prompt into a structured feature-list.json,
//	   and write an init.sh that future sessions will run on boot."
//
//	yagura already produces feature-list.json (v0.32). The missing companion
//	is init.sh — the script every Coding agent session runs to verify the
//	environment is sane and load the handoff state before doing real work.
//
// Design (ADR-0001 zero-dep):
//   - Pure function: BootSpec → string
//   - POSIX sh compatible (bash extensions avoided)
//   - set -euo pipefail at the top — fail fast
//   - Idempotent — safe to run on every session
//   - Echoes progress to stderr so coding-agent sees what's happening
//
// Reference: https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents
package initsh

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// BootSpec is the structured input — caller assembles from registry facts.
type BootSpec struct {
	Project     string
	GeneratedAt time.Time
	GeneratedBy string

	// Working directory (absolute). If empty, script uses $PWD.
	WorkDir string

	// Language hint (go / node / python / rust / unknown). Drives which
	// language-specific checks to emit.
	Language string

	// Required CLI tools (e.g. "git", "go", "make"). Each is checked with
	// `command -v` and the script exits 1 if missing.
	RequiredTools []string

	// Required files relative to WorkDir (e.g. "go.mod", "Plan.md"). Each is
	// checked with `test -f` and exit 1 if missing.
	RequiredFiles []string

	// Boot commands to run after checks pass (e.g. "make deps", "go mod download").
	// Each runs in order; failure aborts.
	BootCommands []string

	// Handoff file paths to display at the end (e.g. "claude-progress.txt").
	// Each is `cat`-ed so the coding agent sees the latest state immediately.
	HandoffFiles []string
}

// Generate renders init.sh as a string. Output is deterministic given the
// same input and safe to re-run.
func Generate(spec BootSpec) string {
	var sb strings.Builder
	writeShebangAndHeader(&sb, spec)
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

func writeShebangAndHeader(sb *strings.Builder, spec BootSpec) {
	sb.WriteString("#!/usr/bin/env sh\n")
	by := spec.GeneratedBy
	if by == "" {
		by = "yagura"
	}
	when := spec.GeneratedAt
	if when.IsZero() {
		when = time.Now().UTC()
	}
	proj := spec.Project
	if proj == "" {
		proj = "(unknown project)"
	}
	fmt.Fprintf(sb, "# init.sh — boot script for %s\n", proj)
	fmt.Fprintf(sb, "# Generated at %s by %s\n", when.UTC().Format(time.RFC3339), by)
	sb.WriteString("#\n")
	sb.WriteString("# This script is run at the start of every long-running agent session.\n")
	sb.WriteString("# It verifies the environment, runs boot commands, then displays\n")
	sb.WriteString("# the latest cross-session handoff state.\n")
	sb.WriteString("#\n")
	sb.WriteString("# Idempotent — safe to run repeatedly.\n\n")
}

func writeStrictMode(sb *strings.Builder) {
	sb.WriteString("set -eu\n")
	sb.WriteString("# Fail fast on any unhandled error, undefined var, or pipeline failure.\n")
	sb.WriteString("# (pipefail is bash-only, so we omit it for POSIX sh compatibility.)\n\n")
	sb.WriteString("log() { printf '[init.sh] %s\\n' \"$*\" >&2; }\n")
	sb.WriteString("fail() { printf '[init.sh] FAIL: %s\\n' \"$*\" >&2; exit 1; }\n\n")
}

func writeWorkDir(sb *strings.Builder, spec BootSpec) {
	if spec.WorkDir == "" {
		sb.WriteString("log \"Working in $PWD\"\n\n")
		return
	}
	fmt.Fprintf(sb, "cd %s || fail \"cannot cd to %s\"\n", shQuote(spec.WorkDir), spec.WorkDir)
	fmt.Fprintf(sb, "log \"Working in %s\"\n\n", spec.WorkDir)
}

func writeRequiredTools(sb *strings.Builder, spec BootSpec) {
	if len(spec.RequiredTools) == 0 {
		return
	}
	tools := uniqueSorted(spec.RequiredTools)
	sb.WriteString("log \"Checking required CLI tools…\"\n")
	for _, t := range tools {
		fmt.Fprintf(sb, "command -v %s >/dev/null 2>&1 || fail \"%s not in PATH\"\n", shQuote(t), t)
	}
	sb.WriteString("\n")
}

func writeRequiredFiles(sb *strings.Builder, spec BootSpec) {
	if len(spec.RequiredFiles) == 0 {
		return
	}
	files := uniqueSorted(spec.RequiredFiles)
	sb.WriteString("log \"Checking required files…\"\n")
	for _, f := range files {
		fmt.Fprintf(sb, "test -f %s || fail \"missing required file: %s\"\n", shQuote(f), f)
	}
	sb.WriteString("\n")
}

func writeLanguageChecks(sb *strings.Builder, spec BootSpec) {
	switch strings.ToLower(spec.Language) {
	case "go", "golang":
		sb.WriteString("log \"Go: verifying module…\"\n")
		sb.WriteString("go mod verify >/dev/null || fail \"go mod verify failed\"\n\n")
	case "node", "nodejs", "javascript", "typescript":
		sb.WriteString("log \"Node: verifying lockfile…\"\n")
		sb.WriteString("test -f package-lock.json || test -f pnpm-lock.yaml || test -f yarn.lock || \\\n")
		sb.WriteString("  fail \"no lockfile (package-lock.json / pnpm-lock.yaml / yarn.lock)\"\n\n")
	case "python":
		sb.WriteString("log \"Python: verifying interpreter…\"\n")
		sb.WriteString("command -v python3 >/dev/null 2>&1 || fail \"python3 not found\"\n\n")
	case "rust":
		sb.WriteString("log \"Rust: verifying Cargo.toml…\"\n")
		sb.WriteString("test -f Cargo.toml || fail \"Cargo.toml missing\"\n\n")
	}
}

func writeBootCommands(sb *strings.Builder, spec BootSpec) {
	if len(spec.BootCommands) == 0 {
		return
	}
	sb.WriteString("log \"Running boot commands…\"\n")
	for _, c := range spec.BootCommands {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		fmt.Fprintf(sb, "log \"  $ %s\"\n", escapeForLog(c))
		// Commands are interpolated verbatim — caller is trusted (yagura
		// registry, not user input from the network).
		fmt.Fprintf(sb, "%s\n", c)
	}
	sb.WriteString("\n")
}

func writeHandoffDisplay(sb *strings.Builder, spec BootSpec) {
	if len(spec.HandoffFiles) == 0 {
		return
	}
	files := uniqueSorted(spec.HandoffFiles)
	sb.WriteString("log \"Displaying handoff state…\"\n")
	for _, f := range files {
		fmt.Fprintf(sb, "if [ -f %s ]; then\n", shQuote(f))
		fmt.Fprintf(sb, "  printf '\\n=== %s ===\\n'\n", escapeForPrintf(f))
		fmt.Fprintf(sb, "  cat %s\n", shQuote(f))
		sb.WriteString("  printf '\\n'\n")
		sb.WriteString("fi\n")
	}
	sb.WriteString("\n")
}

func writeFooter(sb *strings.Builder) {
	sb.WriteString("log \"Boot complete. Coding agent: pick the topmost pending feature.\"\n")
}

// ─── helpers ──────────────────────────────────────────

// shQuote wraps s in single quotes, escaping any embedded single quotes.
//
// POSIX-safe: 'a'\”b' → a'b literally.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// escapeForLog escapes a string for safe inclusion inside a double-quoted
// shell `log "  $ ..."` invocation — strips control chars and quotes.
func escapeForLog(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "$", `\$`)
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}

// escapeForPrintf escapes a string for a printf format. We use printf '...%s...'
// patterns above so we only need to be careful about single quotes and the
// percent sign.
func escapeForPrintf(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	s = strings.ReplaceAll(s, "'", `'\''`)
	return s
}

// uniqueSorted returns a sorted slice with duplicates removed.
//
// Used so init.sh output is deterministic regardless of caller order.
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
