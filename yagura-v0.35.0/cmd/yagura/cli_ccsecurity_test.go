package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/ccsecurity"
)

// writeFile is a small helper for building a fixture project tree.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCLI_CCSecurity_CleanProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "CLAUDE.md", "## セキュリティルール\n- .env を読まない\n")
	writeFile(t, dir, ".claude/settings.json", `{"permissions":{"deny":["Read(./.env)"]}}`)
	writeFile(t, dir, ".gitignore", ".env\n")
	writeFile(t, dir, "WORKLOG.md", "- did stuff\n")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCLICapture(t, "cc-security", "--dir", dir)
	if code != 0 {
		t.Fatalf("clean project should exit 0, got %d (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "score: 100/100") {
		t.Errorf("expected perfect score, got:\n%s", out)
	}
	// manual checklist is always present.
	if !strings.Contains(out, "manual checklist") {
		t.Errorf("manual checklist should always be shown:\n%s", out)
	}
}

func TestCLI_CCSecurity_DetectsEnvAndDangerousFlag(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "API_KEY=secret123\n")
	writeFile(t, dir, "run.sh", "#!/bin/sh\nclaude --dangerously-skip-permissions\n")

	code, out, _ := runCLICapture(t, "cc-security", "--dir", dir)
	if code != 0 {
		t.Fatalf("audit itself should exit 0 without --min-score, got %d", code)
	}
	if !strings.Contains(out, "P02-env-in-project") || !strings.Contains(out, "FAIL") {
		t.Errorf("should report .env as a FAIL:\n%s", out)
	}
	if !strings.Contains(out, "P05-dangerous-skip") {
		t.Errorf("should flag --dangerously-skip-permissions:\n%s", out)
	}
	if !strings.Contains(out, "run.sh") {
		t.Errorf("should name run.sh as the source of the dangerous flag:\n%s", out)
	}
}

func TestCLI_CCSecurity_JSONShape(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "X=1\n")

	code, out, _ := runCLICapture(t, "cc-security", "--dir", dir, "--json")
	if code != 0 {
		t.Fatalf("exit 0 expected, got %d", code)
	}
	var rep ccsecurity.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not valid ccsecurity.Report JSON: %v\n%s", err, out)
	}
	if rep.Score == 100 {
		t.Errorf("score should be below 100 with an .env present, got %d", rep.Score)
	}
	if len(rep.ManualPractices) == 0 {
		t.Error("JSON report should include manual practices")
	}
}

func TestCLI_CCSecurity_MinScoreGate(t *testing.T) {
	dir := t.TempDir()
	// A bare project (no CLAUDE.md, no settings, no git, .env present) scores low.
	writeFile(t, dir, ".env", "X=1\n")

	code, _, errOut := runCLICapture(t, "cc-security", "--dir", dir, "--min-score", "90")
	if code != 1 {
		t.Fatalf("low score with --min-score 90 should exit 1, got %d (stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut, "below --min-score") {
		t.Errorf("stderr should explain the gate failure: %s", errOut)
	}
}

func TestCLI_CCSecurity_MissingDir(t *testing.T) {
	code, _, errOut := runCLICapture(t, "cc-security", "--dir", filepath.Join(t.TempDir(), "does-not-exist"))
	if code != 1 {
		t.Fatalf("missing dir should exit 1, got %d", code)
	}
	if errOut == "" {
		t.Error("expected an error message on stderr for a missing dir")
	}
}
