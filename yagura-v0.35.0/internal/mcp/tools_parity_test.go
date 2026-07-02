package mcp

import (
	"path/filepath"
	"testing"

	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/audit"
	"github.com/shizukutanaka/yagura/internal/ccsecurity"
	"github.com/shizukutanaka/yagura/internal/coverage"
	"github.com/shizukutanaka/yagura/internal/diffscan"
	"github.com/shizukutanaka/yagura/internal/flowrisk"
	"github.com/shizukutanaka/yagura/internal/harness"
	"github.com/shizukutanaka/yagura/internal/reviewgate"
)

// ─── yagura_alert_snapshot ─────────────────────────────────────

func TestAlertSnapshot_InvalidStatus(t *testing.T) {
	store, err := alertfix.NewStore(filepath.Join(t.TempDir(), "alert_state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	tool := buildAlertSnapshotTool(store)
	if err := callRawErr(t, tool, `{"status":"bogus"}`); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input for unknown status, got %v", err)
	}
}

func TestAlertSnapshot_ReturnsLifecycleStats(t *testing.T) {
	store, err := alertfix.NewStore(filepath.Join(t.TempDir(), "alert_state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	tool := buildAlertSnapshotTool(store)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	if _, ok := r["lifecycle_stats"]; !ok {
		t.Errorf("expected lifecycle_stats in report, got %v", r)
	}
}

// ─── yagura_self_improve_history ───────────────────────────────

func TestSelfImproveHistory_EmptyWhenNoRecords(t *testing.T) {
	d := newDeps(t)
	d.StateDir = t.TempDir()
	tool := buildSelfImproveHistoryTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	if r["count"].(int) != 0 {
		t.Errorf("expected count 0 with no prior assessments, got %v", r)
	}
}

func TestSelfImproveHistory_ReturnsAppendedRecords(t *testing.T) {
	d := newDeps(t)
	d.StateDir = t.TempDir()
	logger, err := audit.New(filepath.Join(d.StateDir, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append(audit.Record{Kind: "self_improve", Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	logger.Close()

	tool := buildSelfImproveHistoryTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	if r["count"].(int) != 1 {
		t.Fatalf("expected count 1, got %v", r)
	}
}

// ─── yagura_coverage ───────────────────────────────────────────

func TestCoverage_InvalidArgs(t *testing.T) {
	d := newDeps(t)
	tool := buildCoverageTool(d)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestCoverage_RequiresPaths(t *testing.T) {
	d := newDeps(t)
	tool := buildCoverageTool(d)
	if err := callRawErr(t, tool, "{}"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input when paths omitted, got %v", err)
	}
}

func TestCoverage_PurePythonShowsASTLensGapNotSensorGap(t *testing.T) {
	d := newDeps(t)
	tool := buildCoverageTool(d)
	r := mustCall(t, tool, map[string]any{
		"paths": []string{"a.py", "b.py"},
	}).(coverage.Report)
	if r.CoverageRatio != 1.0 {
		t.Errorf("sensor-tier coverage_ratio should be 1.0 for covered-lang files, got %v", r.CoverageRatio)
	}
	if r.ASTLensCoverageRatio != 0.0 {
		t.Errorf("AST-lens-tier ratio should be 0 for pure-Python (Go-only lenses), got %v", r.ASTLensCoverageRatio)
	}
}

// ─── yagura_diff_scan ──────────────────────────────────────────

const sampleDiff = `--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,3 @@
 package foo
+var apiKey = "AKIAIOSFODNN7EXAMPLE"
 func F() {}
`

func TestDiffScan_InvalidArgs(t *testing.T) {
	d := newDeps(t)
	tool := buildDiffScanTool(d)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestDiffScan_RequiresDiff(t *testing.T) {
	d := newDeps(t)
	tool := buildDiffScanTool(d)
	if err := callRawErr(t, tool, "{}"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input when diff omitted, got %v", err)
	}
}

func TestDiffScan_ReportsAddedLines(t *testing.T) {
	d := newDeps(t)
	tool := buildDiffScanTool(d)
	r := mustCall(t, tool, map[string]any{"diff": sampleDiff}).(map[string]any)
	added, ok := r["added_lines"].([]diffscan.AddedLine)
	if !ok || len(added) == 0 {
		t.Fatalf("expected non-empty added_lines, got %v", r)
	}
}

// ─── yagura_flow_risk ──────────────────────────────────────────

func TestFlowRisk_InvalidArgs(t *testing.T) {
	d := newDeps(t)
	tool := buildFlowRiskTool(d)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestFlowRisk_RequiresSteps(t *testing.T) {
	d := newDeps(t)
	tool := buildFlowRiskTool(d)
	if err := callRawErr(t, tool, "{}"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input when steps omitted, got %v", err)
	}
}

func TestFlowRisk_DetectsExfiltration(t *testing.T) {
	d := newDeps(t)
	tool := buildFlowRiskTool(d)
	r := mustCall(t, tool, map[string]any{
		"steps": []map[string]any{
			{"name": "read_env", "capability": "secret-read"},
			{"name": "http_post", "capability": "network"},
		},
	}).(map[string]any)
	risks, ok := r["risks"].([]flowrisk.FlowRisk)
	if !ok || len(risks) == 0 {
		t.Fatalf("expected a detected exfiltration flow risk, got %v", r)
	}
}

// ─── yagura_cc_security ────────────────────────────────────────

func TestCCSecurity_InvalidArgs(t *testing.T) {
	d := newDeps(t)
	tool := buildCCSecurityTool(d)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestCCSecurity_AuditsSuppliedFacts(t *testing.T) {
	d := newDeps(t)
	tool := buildCCSecurityTool(d)
	r := mustCall(t, tool, map[string]any{
		"has_gitignore": false,
		"has_claude_md": false,
	}).(ccsecurity.Report)
	if r.Checked == 0 {
		t.Errorf("expected practices to be checked, got %+v", r)
	}
}

// ─── yagura_claudemd_audit ─────────────────────────────────────

func TestClaudeMdAudit_InvalidArgs(t *testing.T) {
	d := newDeps(t)
	tool := buildClaudeMdAuditTool(d)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestClaudeMdAudit_RequiresContent(t *testing.T) {
	d := newDeps(t)
	tool := buildClaudeMdAuditTool(d)
	if err := callRawErr(t, tool, "{}"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input when content omitted, got %v", err)
	}
}

func TestClaudeMdAudit_MissingSectionsAreFlagged(t *testing.T) {
	d := newDeps(t)
	tool := buildClaudeMdAuditTool(d)
	r := mustCall(t, tool, map[string]any{
		"content": "# Title\n\nsome text with no canonical sections",
	}).(harness.ClaudeMdAuditResult)
	if len(r.MissingSections) == 0 {
		t.Fatalf("expected missing_sections to be reported, got %+v", r)
	}
}

// ─── yagura_review_gate ────────────────────────────────────────

func TestReviewGate_InvalidArgs(t *testing.T) {
	d := newDeps(t)
	tool := buildReviewGateTool(d)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestReviewGate_HardSignalBlocks(t *testing.T) {
	d := newDeps(t)
	tool := buildReviewGateTool(d)
	r := mustCall(t, tool, map[string]any{
		"secret_findings": 1,
	}).(reviewgate.Decision)
	if r.Tier != reviewgate.TierBlock {
		t.Errorf("secret findings must trigger a block tier, got %+v", r)
	}
}

func TestReviewGate_CleanSignalsAllow(t *testing.T) {
	d := newDeps(t)
	tool := buildReviewGateTool(d)
	r := mustCall(t, tool, map[string]any{}).(reviewgate.Decision)
	if r.Tier != reviewgate.TierAllow {
		t.Errorf("all-zero signals must allow, got %+v", r)
	}
}
