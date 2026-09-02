package initsh

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
}

func basic() BootSpec {
	return BootSpec{
		Project:       "breeze",
		GeneratedAt:   fixedNow(),
		GeneratedBy:   "yagura 0.33.0",
		WorkDir:       "/home/m/breeze",
		Language:      "go",
		RequiredTools: []string{"git", "go", "make"},
		RequiredFiles: []string{"go.mod", "Plan.md"},
		BootCommands:  []string{"go mod download", "make deps"},
		HandoffFiles:  []string{"claude-progress.txt", "AGENTS.md"},
	}
}

// ─── header + shape ──────────────────────────────────

func TestGenerate_StartsWithShebang(t *testing.T) {
	out := Generate(basic())
	if !strings.HasPrefix(out, "#!/usr/bin/env sh\n") {
		t.Errorf("expected shebang first line, got: %.50s", out)
	}
}

func TestGenerate_IncludesProjectInHeader(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "# init.sh — boot script for breeze") {
		t.Error("header missing project name")
	}
}

func TestGenerate_FallbackProjectAndBy(t *testing.T) {
	out := Generate(BootSpec{})
	if !strings.Contains(out, "(unknown project)") {
		t.Error("project fallback missing")
	}
	if !strings.Contains(out, "by yagura\n") {
		t.Error("by-fallback missing")
	}
}

func TestGenerate_StrictModeAndHelpers(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "set -eu") {
		t.Error("missing set -eu")
	}
	if !strings.Contains(out, "log() {") {
		t.Error("missing log helper")
	}
	if !strings.Contains(out, "fail() {") {
		t.Error("missing fail helper")
	}
	// pipefail intentionally absent (POSIX sh compat)
	if strings.Contains(out, "set -o pipefail") || strings.Contains(out, "set -euo") {
		t.Error("pipefail must be omitted for POSIX sh")
	}
}

// ─── workdir ─────────────────────────────────────────

func TestGenerate_WorkDirCd(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "cd '/home/m/breeze'") {
		t.Errorf("workdir cd missing or unquoted:\n%s", out)
	}
}

func TestGenerate_EmptyWorkDirUsesPWD(t *testing.T) {
	s := basic()
	s.WorkDir = ""
	out := Generate(s)
	if !strings.Contains(out, "Working in $PWD") {
		t.Error("should fall back to PWD")
	}
	if strings.Contains(out, "cd ''") {
		t.Error("should not emit empty cd")
	}
}

func TestGenerate_WorkDirWithSingleQuoteEscaped(t *testing.T) {
	s := basic()
	s.WorkDir = "/home/o'brien/x"
	out := Generate(s)
	if !strings.Contains(out, `'/home/o'\''brien/x'`) {
		t.Errorf("single quote not escaped properly:\n%s", out)
	}
}

// ─── tools + files ────────────────────────────────────

func TestGenerate_ToolsCheckedWithCommandV(t *testing.T) {
	out := Generate(basic())
	for _, tool := range []string{"git", "go", "make"} {
		want := "command -v '" + tool + "' >/dev/null 2>&1"
		if !strings.Contains(out, want) {
			t.Errorf("missing check for %s", tool)
		}
	}
}

func TestGenerate_ToolsDeduplicatedAndSorted(t *testing.T) {
	s := basic()
	s.RequiredTools = []string{"zls", "git", "git", "make", "make"}
	out := Generate(s)
	// Verify deterministic order: git → make → zls
	posGit := strings.Index(out, "command -v 'git'")
	posMake := strings.Index(out, "command -v 'make'")
	posZls := strings.Index(out, "command -v 'zls'")
	if !(posGit < posMake && posMake < posZls) {
		t.Errorf("tools not sorted:\n%s", out)
	}
	if strings.Count(out, "command -v 'git'") != 1 {
		t.Error("git should appear exactly once after dedup")
	}
}

func TestGenerate_FilesCheckedWithTestF(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "test -f 'go.mod'") {
		t.Error("go.mod check missing")
	}
	if !strings.Contains(out, "test -f 'Plan.md'") {
		t.Error("Plan.md check missing")
	}
}

func TestGenerate_NoToolsSectionWhenEmpty(t *testing.T) {
	s := basic()
	s.RequiredTools = nil
	out := Generate(s)
	if strings.Contains(out, "Checking required CLI tools") {
		t.Error("tools section should be omitted")
	}
}

// ─── language checks ─────────────────────────────────

func TestGenerate_GoLangCheck(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "go mod verify") {
		t.Error("go-specific check missing")
	}
}

func TestGenerate_NodeLangCheck(t *testing.T) {
	s := basic()
	s.Language = "node"
	out := Generate(s)
	if !strings.Contains(out, "package-lock.json") {
		t.Error("node-specific check missing")
	}
}

func TestGenerate_PythonLangCheck(t *testing.T) {
	s := basic()
	s.Language = "python"
	out := Generate(s)
	if !strings.Contains(out, "python3") {
		t.Error("python-specific check missing")
	}
}

func TestGenerate_RustLangCheck(t *testing.T) {
	s := basic()
	s.Language = "rust"
	out := Generate(s)
	if !strings.Contains(out, "Cargo.toml") {
		t.Error("rust-specific check missing")
	}
}

func TestGenerate_UnknownLangNoCheck(t *testing.T) {
	s := basic()
	s.Language = "klingon"
	s.BootCommands = nil // remove go-related boot commands so we can isolate language block
	out := Generate(s)
	if strings.Contains(out, "go mod") {
		t.Error("should not emit go-check for unknown lang")
	}
}

// ─── boot commands ───────────────────────────────────

func TestGenerate_BootCommandsRunInOrder(t *testing.T) {
	out := Generate(basic())
	posGoDl := strings.Index(out, "go mod download")
	posMake := strings.Index(out, "make deps")
	if !(posGoDl < posMake) {
		t.Errorf("boot commands order broken:\n%s", out)
	}
}

func TestGenerate_BootCommandsLoggedBeforeRun(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, `log "  $ go mod download"`) {
		t.Error("boot command not logged before execution")
	}
}

func TestGenerate_BootCommandsTrimAndSkipEmpty(t *testing.T) {
	s := basic()
	s.BootCommands = []string{"  ", "echo hello", "\t"}
	out := Generate(s)
	if strings.Contains(out, `log "  $ "`) {
		t.Error("empty command should be skipped")
	}
	if !strings.Contains(out, "echo hello") {
		t.Error("non-empty command should appear")
	}
}

// ─── handoff display ─────────────────────────────────

func TestGenerate_HandoffFilesCatted(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "if [ -f 'claude-progress.txt' ]") {
		t.Error("missing guarded display for claude-progress.txt")
	}
	if !strings.Contains(out, "cat 'AGENTS.md'") {
		t.Error("missing cat for AGENTS.md")
	}
}

func TestGenerate_HandoffFilesDeduplicatedAndSorted(t *testing.T) {
	s := basic()
	s.HandoffFiles = []string{"zzz.txt", "AAA.txt", "AAA.txt"}
	out := Generate(s)
	// uppercase A < lowercase z in ASCII
	posA := strings.Index(out, "cat 'AAA.txt'")
	posZ := strings.Index(out, "cat 'zzz.txt'")
	if !(posA < posZ) {
		t.Errorf("handoff files not sorted ASCII:\n%s", out)
	}
}

// ─── shell safety ────────────────────────────────────

func TestShQuote_EscapesSingleQuotes(t *testing.T) {
	got := shQuote("a'b")
	want := `'a'\''b'`
	if got != want {
		t.Errorf("shQuote: %s != %s", got, want)
	}
}

func TestShQuote_PlainString(t *testing.T) {
	if shQuote("abc") != "'abc'" {
		t.Error("plain string should be single-quoted")
	}
}

// ─── footer + determinism ────────────────────────────

func TestGenerate_FooterAlwaysPresent(t *testing.T) {
	out := Generate(BootSpec{})
	if !strings.Contains(out, "Boot complete") {
		t.Error("footer missing")
	}
}

func TestGenerate_Deterministic(t *testing.T) {
	if Generate(basic()) != Generate(basic()) {
		t.Error("output must be deterministic for the same input")
	}
}

// ─── shellcheck-compatible smoke (sh -n) ─────────────

func TestGenerate_OutputIsValidSh(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	out := Generate(basic())
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(out)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh -n rejected output: %v\n%s\n--- script ---\n%s", err, combined, out)
	}
}
