package initps1

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
		GeneratedBy:   "yagura 0.34.0",
		WorkDir:       `C:\dev\breeze`,
		Language:      "go",
		RequiredTools: []string{"git", "go", "make"},
		RequiredFiles: []string{"go.mod", "Plan.md"},
		BootCommands:  []string{"go mod download", "make deps"},
		HandoffFiles:  []string{"claude-progress.txt", "AGENTS.md"},
	}
}

// ─── basic shape ─────────────────────────────────────

func TestGenerate_HeaderContainsProject(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "# init.ps1 — boot script for breeze") {
		t.Errorf("header missing project name:\n%s", out[:200])
	}
}

func TestGenerate_PowerShell51CompatNoticeAppears(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "PowerShell 5.1+") {
		t.Error("missing compatibility note")
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

// ─── strict mode ─────────────────────────────────────

func TestGenerate_StrictModeDirectives(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "$ErrorActionPreference = 'Stop'") {
		t.Error("missing ErrorActionPreference = Stop")
	}
	if !strings.Contains(out, "Set-StrictMode -Version Latest") {
		t.Error("missing Set-StrictMode")
	}
}

func TestGenerate_HelperFunctions(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "function Log(") {
		t.Error("missing Log helper")
	}
	if !strings.Contains(out, "function Fail(") {
		t.Error("missing Fail helper")
	}
	if !strings.Contains(out, "exit 1") {
		t.Error("Fail should exit 1")
	}
}

// ─── work dir ────────────────────────────────────────

func TestGenerate_WorkDirSetLocation(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, `Set-Location -Path 'C:\dev\breeze'`) {
		t.Errorf("workdir Set-Location wrong or unquoted:\n%s", out)
	}
}

func TestGenerate_EmptyWorkDirUsesGetLocation(t *testing.T) {
	s := basic()
	s.WorkDir = ""
	out := Generate(s)
	if !strings.Contains(out, "(Get-Location).Path") {
		t.Error("should fall back to Get-Location")
	}
}

func TestGenerate_WorkDirWithSingleQuoteDoubled(t *testing.T) {
	s := basic()
	s.WorkDir = `C:\Users\o'brien\code`
	out := Generate(s)
	if !strings.Contains(out, `'C:\Users\o''brien\code'`) {
		t.Errorf("single quote not doubled per PS literal-string rules:\n%s", out)
	}
}

// ─── tools + files ───────────────────────────────────

func TestGenerate_ToolsCheckedWithGetCommand(t *testing.T) {
	out := Generate(basic())
	for _, tool := range []string{"git", "go", "make"} {
		want := "Get-Command '" + tool + "' -ErrorAction SilentlyContinue"
		if !strings.Contains(out, want) {
			t.Errorf("missing Get-Command check for %s", tool)
		}
	}
}

func TestGenerate_ToolsDeduplicatedAndSorted(t *testing.T) {
	s := basic()
	s.RequiredTools = []string{"zls", "git", "git", "make"}
	out := Generate(s)
	posGit := strings.Index(out, "Get-Command 'git'")
	posMake := strings.Index(out, "Get-Command 'make'")
	posZls := strings.Index(out, "Get-Command 'zls'")
	if !(posGit < posMake && posMake < posZls) {
		t.Errorf("tools not sorted:\n%s", out)
	}
	if strings.Count(out, "Get-Command 'git'") != 1 {
		t.Error("git should appear exactly once after dedup")
	}
}

func TestGenerate_FilesCheckedWithTestPath(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "Test-Path -LiteralPath 'go.mod' -PathType Leaf") {
		t.Error("go.mod Test-Path check missing or wrong form")
	}
	if !strings.Contains(out, "Test-Path -LiteralPath 'Plan.md'") {
		t.Error("Plan.md check missing")
	}
}

// ─── language checks ─────────────────────────────────

func TestGenerate_GoLangCheck(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "go mod verify") {
		t.Error("go-specific check missing")
	}
	if !strings.Contains(out, "$LASTEXITCODE -ne 0") {
		t.Error("missing exit code check for native exe")
	}
}

func TestGenerate_NodeLangCheck(t *testing.T) {
	s := basic()
	s.Language = "node"
	out := Generate(s)
	if !strings.Contains(out, "package-lock.json") || !strings.Contains(out, "pnpm-lock.yaml") {
		t.Errorf("node-specific check missing:\n%s", out)
	}
}

func TestGenerate_PythonLangCheck(t *testing.T) {
	s := basic()
	s.Language = "python"
	out := Generate(s)
	if !strings.Contains(out, "Get-Command python") {
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
	s.BootCommands = nil
	out := Generate(s)
	if strings.Contains(out, "go mod") {
		t.Error("should not emit go-check for unknown lang")
	}
}

// ─── boot commands ───────────────────────────────────

func TestGenerate_BootCommandsViaInvokeExpression(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "Invoke-Expression 'go mod download'") {
		t.Errorf("Invoke-Expression wrapping missing:\n%s", out)
	}
}

func TestGenerate_BootCommandsLoggedBeforeRun(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "Log '  > go mod download'") {
		t.Error("boot command not logged before execution")
	}
}

func TestGenerate_BootCommandsTrimAndSkipEmpty(t *testing.T) {
	s := basic()
	s.BootCommands = []string{"  ", "Write-Host hello", "\t"}
	out := Generate(s)
	if strings.Contains(out, "Log '  > '") {
		t.Error("empty command should be skipped")
	}
	if !strings.Contains(out, "Write-Host hello") {
		t.Error("non-empty command should appear")
	}
}

// ─── handoff display ─────────────────────────────────

func TestGenerate_HandoffFilesShownWithGetContent(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "Get-Content -LiteralPath 'claude-progress.txt'") {
		t.Error("missing Get-Content for claude-progress.txt")
	}
	if !strings.Contains(out, "Test-Path -LiteralPath 'AGENTS.md'") {
		t.Error("missing guarded display for AGENTS.md")
	}
}

func TestGenerate_HandoffFilesDeduplicatedAndSorted(t *testing.T) {
	s := basic()
	s.HandoffFiles = []string{"zzz.txt", "AAA.txt", "AAA.txt"}
	out := Generate(s)
	posA := strings.Index(out, "Get-Content -LiteralPath 'AAA.txt'")
	posZ := strings.Index(out, "Get-Content -LiteralPath 'zzz.txt'")
	if !(posA < posZ) {
		t.Errorf("handoff files not sorted ASCII:\n%s", out)
	}
}

// ─── shell safety ────────────────────────────────────

func TestPsQuote_DoublesSingleQuotes(t *testing.T) {
	got := psQuote("a'b")
	want := "'a''b'"
	if got != want {
		t.Errorf("psQuote: %s != %s", got, want)
	}
}

func TestPsQuote_PassesThroughBackticksAndDollars(t *testing.T) {
	// Inside '...' PowerShell literals, backtick and $ are NOT special.
	got := psQuote("$foo`bar")
	want := "'$foo`bar'"
	if got != want {
		t.Errorf("psQuote should pass through `$ and backtick: %s != %s", got, want)
	}
}

func TestEscapeForDoubleQuoted_HandlesSpecials(t *testing.T) {
	got := escapeForDoubleQuoted("$foo`bar\"baz")
	want := "`$foo``bar`\"baz"
	if got != want {
		t.Errorf("escapeForDoubleQuoted: %q != %q", got, want)
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

// ─── pwsh syntactic check (skipped if not installed) ─

func TestGenerate_OutputIsValidPowerShell(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not available; skipping syntactic validation")
	}
	out := Generate(basic())
	// -NoProfile -Command "$null = [ScriptBlock]::Create($stdin)"
	cmd := exec.Command(pwsh, "-NoProfile", "-Command",
		"$body = [Console]::In.ReadToEnd(); $null = [ScriptBlock]::Create($body)")
	cmd.Stdin = strings.NewReader(out)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pwsh rejected output: %v\n%s\n--- script ---\n%s", err, combined, out)
	}
}
