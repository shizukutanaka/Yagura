package ccsecurity

import (
	"strings"
	"testing"
)

func hasPractice(rs []Practice, id string) *Practice {
	for i := range rs {
		if rs[i].ID == id {
			return &rs[i]
		}
	}
	return nil
}

// 完全にクリーンなプロジェクト: env なし / dangerous flag なし / CLAUDE.md に
// セキュリティルールあり / settings に deny / git あり / WORKLOG あり。
func cleanInput() Input {
	return Input{
		HasGitDir:    true,
		HasClaudeMd:  true,
		ClaudeMd:     "## セキュリティ最優先ルール\n- .env や secret を読まない\n- rm -rf は確認する",
		HasSettings:  true,
		SettingsJSON: `{"permissions":{"deny":["Read(./.env)","Bash(rm -rf*)"]}}`,
		HasGitignore: true,
		Gitignore:    ".env\nnode_modules/\n",
		HasWorklog:   true,
	}
}

func TestAudit_CleanProjectScoresHigh(t *testing.T) {
	r := Audit(cleanInput())
	if r.Score != 100 {
		t.Errorf("clean project should score 100, got %d (practices: %+v)", r.Score, r.Practices)
	}
	if r.Failed != 0 {
		t.Errorf("clean project should have 0 failures, got %d", r.Failed)
	}
	// マニュアル項目は常にガイダンスとして提示される。
	if len(r.ManualPractices) == 0 {
		t.Error("manual (human-process) practices must always be listed as guidance")
	}
}

func TestAudit_EnvFileInProjectIsHighFinding(t *testing.T) {
	in := cleanInput()
	in.EnvFiles = []string{".env", ".env.local"}
	r := Audit(in)
	p := hasPractice(r.Practices, "P02-env-in-project")
	if p == nil || p.Status != StatusFail {
		t.Fatalf("P02 should FAIL when .env present: %+v", p)
	}
	if p.Severity != SevHigh {
		t.Errorf("P02 should be HIGH, got %s", p.Severity)
	}
	if !strings.Contains(p.Detail, ".env.local") {
		t.Errorf("P02 detail should name the offending files: %q", p.Detail)
	}
	if r.Score >= 100 {
		t.Errorf("score should drop below 100 with an .env present, got %d", r.Score)
	}
}

func TestAudit_EnvNotGitignoredAddsGap(t *testing.T) {
	in := cleanInput()
	in.EnvFiles = []string{".env"}
	in.Gitignore = "node_modules/\n" // does NOT cover .env
	r := Audit(in)
	g := hasPractice(r.Practices, "P02-env-gitignore")
	if g == nil || g.Status != StatusFail {
		t.Fatalf("P02-env-gitignore should FAIL when .env is not ignored: %+v", g)
	}
}

func TestAudit_EnvGitignoredNoGap(t *testing.T) {
	in := cleanInput()
	in.EnvFiles = []string{".env"}
	in.Gitignore = ".env\n"
	r := Audit(in)
	g := hasPractice(r.Practices, "P02-env-gitignore")
	if g == nil || g.Status != StatusPass {
		t.Fatalf("gitignored .env should PASS the gitignore check: %+v", g)
	}
}

func TestAudit_DangerousSkipFlagIsCritical(t *testing.T) {
	in := cleanInput()
	in.ExtraText = []NamedText{{Name: "run.sh", Text: "claude --dangerously-skip-permissions"}}
	r := Audit(in)
	p := hasPractice(r.Practices, "P05-dangerous-skip")
	if p == nil || p.Status != StatusFail {
		t.Fatalf("P05 should FAIL when the dangerous flag appears: %+v", p)
	}
	if p.Severity != SevCritical {
		t.Errorf("P05 must be CRITICAL, got %s", p.Severity)
	}
	if !strings.Contains(p.Detail, "run.sh") {
		t.Errorf("P05 detail should name the file: %q", p.Detail)
	}
}

func TestAudit_DangerousFlagDetectedInSettingsAndClaudeMd(t *testing.T) {
	in := cleanInput()
	in.ClaudeMd += "\nalways pass --dangerously-skip-permissions"
	r := Audit(in)
	if p := hasPractice(r.Practices, "P05-dangerous-skip"); p == nil || p.Status != StatusFail {
		t.Fatalf("P05 should catch the flag inside CLAUDE.md: %+v", p)
	}
}

func TestAudit_MissingClaudeMdWarns(t *testing.T) {
	in := cleanInput()
	in.HasClaudeMd = false
	in.ClaudeMd = ""
	r := Audit(in)
	p := hasPractice(r.Practices, "P07-claude-md-rules")
	if p == nil || p.Status != StatusWarn {
		t.Fatalf("P07 should WARN when CLAUDE.md is missing: %+v", p)
	}
}

func TestAudit_ClaudeMdWithoutSecurityRulesWarns(t *testing.T) {
	in := cleanInput()
	in.ClaudeMd = "# My Project\n\nJust a description, no rules."
	r := Audit(in)
	p := hasPractice(r.Practices, "P07-claude-md-rules")
	if p == nil || p.Status != StatusWarn {
		t.Fatalf("P07 should WARN when CLAUDE.md has no security rules: %+v", p)
	}
}

func TestAudit_NoDenyRulesWarns(t *testing.T) {
	in := cleanInput()
	in.SettingsJSON = `{"permissions":{"allow":["Read(*)"]}}` // allow but no deny
	r := Audit(in)
	p := hasPractice(r.Practices, "P06-permission-deny")
	if p == nil || p.Status != StatusWarn {
		t.Fatalf("P06 should WARN with no deny rules: %+v", p)
	}
}

func TestAudit_MissingSettingsWarns(t *testing.T) {
	in := cleanInput()
	in.HasSettings = false
	in.SettingsJSON = ""
	r := Audit(in)
	p := hasPractice(r.Practices, "P06-permission-deny")
	if p == nil || p.Status != StatusWarn {
		t.Fatalf("P06 should WARN with no settings at all: %+v", p)
	}
}

func TestAudit_NoGitWarns(t *testing.T) {
	in := cleanInput()
	in.HasGitDir = false
	r := Audit(in)
	p := hasPractice(r.Practices, "P08-git-rollback")
	if p == nil || p.Status != StatusWarn {
		t.Fatalf("P08 should WARN with no .git: %+v", p)
	}
}

func TestAudit_TooManyMCPServersWarns(t *testing.T) {
	in := cleanInput()
	in.MCPServerCount = 9
	r := Audit(in)
	p := hasPractice(r.Practices, "P12-mcp-minimal")
	if p == nil || p.Status != StatusWarn {
		t.Fatalf("P12 should WARN with many MCP servers: %+v", p)
	}
}

func TestAudit_FewMCPServersPass(t *testing.T) {
	in := cleanInput()
	in.MCPServerCount = 2
	r := Audit(in)
	p := hasPractice(r.Practices, "P12-mcp-minimal")
	if p == nil || p.Status != StatusPass {
		t.Fatalf("P12 should PASS with few MCP servers: %+v", p)
	}
}

func TestAudit_ScoreFlooredAtZero(t *testing.T) {
	// Everything wrong: dangerous flag + env + no claude.md + no settings + no git.
	in := Input{
		EnvFiles:   []string{".env"},
		ExtraText:  []NamedText{{Name: "x.sh", Text: "--dangerously-skip-permissions"}},
		HasGitDir:  false,
		HasWorklog: false,
	}
	r := Audit(in)
	if r.Score < 0 || r.Score > 100 {
		t.Errorf("score must be clamped to [0,100], got %d", r.Score)
	}
}

func TestAudit_PracticesSortedByID(t *testing.T) {
	r := Audit(cleanInput())
	for i := 1; i < len(r.Practices); i++ {
		if r.Practices[i-1].ID > r.Practices[i].ID {
			t.Errorf("practices must be sorted by ID: %q before %q", r.Practices[i-1].ID, r.Practices[i].ID)
		}
	}
}

func TestAudit_Deterministic(t *testing.T) {
	in := cleanInput()
	in.EnvFiles = []string{".env"}
	in.MCPServerCount = 9
	a := Audit(in)
	b := Audit(in)
	if a.Score != b.Score || len(a.Practices) != len(b.Practices) {
		t.Error("Audit must be deterministic for identical input")
	}
	for i := range a.Practices {
		if a.Practices[i] != b.Practices[i] {
			t.Errorf("practice %d differs across runs: %+v vs %+v", i, a.Practices[i], b.Practices[i])
		}
	}
}
