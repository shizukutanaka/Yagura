package harness

import (
	"strings"
	"testing"
)

func cmIssue(issues []string, substr string) bool {
	for _, i := range issues {
		if strings.Contains(i, substr) {
			return true
		}
	}
	return false
}

// マンデート準拠の理想的な CLAUDE.md: H1 + 4 セクション + 適度な命令数。
const goodClaudeMd = `# MyProject CLAUDE.md

## Why
このプロジェクトが存在する理由。

## Map
- src/auth/  # 認証
- src/api/   # API

## Rules
- キャッシュ TTL は 300 秒固定
- user_id は UUID 移行予定

## Workflows
- テスト: make test
- ビルド: make build
`

func TestAuditClaudeMd_WellFormedScoresHigh(t *testing.T) {
	r := AuditClaudeMd(goodClaudeMd)
	if !r.HasTitle {
		t.Error("H1 title should be detected")
	}
	if len(r.MissingSections) != 0 {
		t.Errorf("all 4 sections present, got missing=%v", r.MissingSections)
	}
	if r.Score < 90 {
		t.Errorf("well-formed CLAUDE.md should score >=90, got %d (issues %v)", r.Score, r.Issues)
	}
}

func TestAuditClaudeMd_EmptyIsZero(t *testing.T) {
	r := AuditClaudeMd("")
	if r.Score != 0 {
		t.Errorf("empty content should score 0, got %d", r.Score)
	}
	if !cmIssue(r.Issues, "empty") {
		t.Errorf("empty should report an empty issue, got %v", r.Issues)
	}
}

func TestAuditClaudeMd_DetectsFourSections(t *testing.T) {
	r := AuditClaudeMd(goodClaudeMd)
	for _, want := range []string{"Why", "Map", "Rules", "Workflows"} {
		found := false
		for _, s := range r.SectionsFound {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("section %q should be detected, got %v", want, r.SectionsFound)
		}
	}
}

func TestAuditClaudeMd_SectionWithEmDashHeader(t *testing.T) {
	// yagura 自身の "## Why — ..." 形式も検出できること。
	content := "# P\n\n## Why — 背景\nx\n## Map — 地図\ny\n## Rules\nz\n## Workflows\nw\n"
	r := AuditClaudeMd(content)
	if len(r.MissingSections) != 0 {
		t.Errorf("em-dash headers should be detected, missing=%v", r.MissingSections)
	}
}

func TestAuditClaudeMd_MissingSectionsFlagged(t *testing.T) {
	content := "# P\n\n## Why\nx\n## Rules\ny\n" // Map と Workflows が無い
	r := AuditClaudeMd(content)
	if len(r.MissingSections) != 2 {
		t.Fatalf("expected 2 missing sections, got %v", r.MissingSections)
	}
	if !cmIssue(r.Issues, "Map") || !cmIssue(r.Issues, "Workflows") {
		t.Errorf("missing Map/Workflows should be reported, got %v", r.Issues)
	}
	if r.Score >= 90 {
		t.Errorf("missing 2 sections should drop score, got %d", r.Score)
	}
}

func TestAuditClaudeMd_NoTitleWarns(t *testing.T) {
	content := "## Why\nx\n## Map\ny\n## Rules\nz\n## Workflows\nw\n"
	r := AuditClaudeMd(content)
	if r.HasTitle {
		t.Error("no H1 should set HasTitle=false")
	}
	if !cmIssue(r.Issues, "title") {
		t.Errorf("missing H1 should be reported, got %v", r.Issues)
	}
}

func TestAuditClaudeMd_InstructionCount(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# P\n## Why\nx\n## Map\ny\n## Rules\n")
	for i := 0; i < 10; i++ {
		sb.WriteString("- rule\n")
	}
	sb.WriteString("## Workflows\n")
	for i := 0; i < 5; i++ {
		sb.WriteString("1. step\n")
	}
	r := AuditClaudeMd(sb.String())
	if r.InstructionCount != 15 {
		t.Errorf("instruction count = %d, want 15 (10 bullets + 5 numbered)", r.InstructionCount)
	}
}

func TestAuditClaudeMd_TooManyInstructionsFails(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# P\n## Why\na\n## Map\nb\n## Rules\n")
	for i := 0; i < 205; i++ {
		sb.WriteString("- rule\n")
	}
	sb.WriteString("## Workflows\nc\n")
	r := AuditClaudeMd(sb.String())
	if !cmIssue(r.Issues, "200") {
		t.Errorf("over-200 instructions should be flagged (Lost in the Middle), got %v", r.Issues)
	}
	if r.Score >= 90 {
		t.Errorf("over-limit should drop score, got %d", r.Score)
	}
}

func TestAuditClaudeMd_ApproachingLimitWarns(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# P\n## Why\na\n## Map\nb\n## Rules\n")
	for i := 0; i < 160; i++ {
		sb.WriteString("- rule\n")
	}
	sb.WriteString("## Workflows\nc\n")
	r := AuditClaudeMd(sb.String())
	// 150-200 は警告(suggestion)だが fail ではない。
	if len(r.Suggestions) == 0 {
		t.Errorf("150-200 instructions should produce a suggestion, got none")
	}
}

func TestAuditClaudeMd_Deterministic(t *testing.T) {
	a := AuditClaudeMd(goodClaudeMd)
	b := AuditClaudeMd(goodClaudeMd)
	if a.Score != b.Score || strings.Join(a.MissingSections, ",") != strings.Join(b.MissingSections, ",") {
		t.Error("AuditClaudeMd must be deterministic")
	}
}
