package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/harness"
)

func TestCLI_ClaudeMdAudit_WellFormed(t *testing.T) {
	dir := t.TempDir()
	good := "# P CLAUDE.md\n\n## Why\nx\n## Map\n- a\n## Rules\n- r\n## Workflows\n- test: make test\n"
	writeFile(t, dir, "CLAUDE.md", good)

	code, out, errOut := runCLICapture(t, "claudemd-audit", "--file", filepath.Join(dir, "CLAUDE.md"))
	if code != 0 {
		t.Fatalf("well-formed should exit 0, got %d (stderr %s)", code, errOut)
	}
	if !strings.Contains(out, "score:") || !strings.Contains(out, "title: yes") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestCLI_ClaudeMdAudit_MissingSectionsJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "CLAUDE.md", "# P\n\n## Why\nx\n## Rules\n- r\n") // no Map/Workflows

	code, out, _ := runCLICapture(t, "claudemd-audit", "--file", filepath.Join(dir, "CLAUDE.md"), "--json")
	if code != 0 {
		t.Fatalf("audit exit 0 expected, got %d", code)
	}
	var res harness.ClaudeMdAuditResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not valid ClaudeMdAuditResult JSON: %v\n%s", err, out)
	}
	if len(res.MissingSections) != 2 {
		t.Errorf("expected 2 missing sections, got %v", res.MissingSections)
	}
}

func TestCLI_ClaudeMdAudit_MinScoreGate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "CLAUDE.md", "just text, no structure\n")

	code, _, errOut := runCLICapture(t, "claudemd-audit", "--file", filepath.Join(dir, "CLAUDE.md"), "--min-score", "90")
	if code != 1 {
		t.Fatalf("low score with --min-score 90 should exit 1, got %d", code)
	}
	if !strings.Contains(errOut, "below --min-score") {
		t.Errorf("stderr should explain the gate failure: %s", errOut)
	}
}

func TestCLI_ClaudeMdAudit_MissingFile(t *testing.T) {
	code, _, errOut := runCLICapture(t, "claudemd-audit", "--file", filepath.Join(t.TempDir(), "nope.md"))
	if code != 1 {
		t.Fatalf("missing file should exit 1, got %d", code)
	}
	if errOut == "" {
		t.Error("expected an error message for a missing file")
	}
}

func TestCLI_ClaudeMdAudit_DefaultsToCLAUDEmd(t *testing.T) {
	// Positional arg overrides the default; verify positional path works.
	dir := t.TempDir()
	p := filepath.Join(dir, "OTHER.md")
	if err := os.WriteFile(p, []byte("# x\n## Why\na\n## Map\nb\n## Rules\nc\n## Workflows\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLICapture(t, "claudemd-audit", p)
	if code != 0 {
		t.Fatalf("positional path should work, got exit %d", code)
	}
	if !strings.Contains(out, "OTHER.md") {
		t.Errorf("output should name the audited file: %s", out)
	}
}
