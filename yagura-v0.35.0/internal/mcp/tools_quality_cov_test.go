package mcp

import (
	"testing"
)

// ─── yagura_quality_check error + files branches ─────────────────

func TestQualityCheck_InvalidArgs(t *testing.T) {
	d := newDeps(t)
	tool := buildQualityCheckTool(d, nil)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestQualityCheck_RequiresFilesOrText(t *testing.T) {
	d := newDeps(t)
	tool := buildQualityCheckTool(d, nil)
	if err := callRawErr(t, tool, "{}"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input when neither files nor text given, got %v", err)
	}
}

// TestQualityCheck_FilesModeWithByFile exercises the ScanFilesCached path
// (files, not text) and the by_file output branch in formatQualityResult.
func TestQualityCheck_FilesModeWithByFile(t *testing.T) {
	d := newDeps(t)
	tool := buildQualityCheckTool(d, nil)
	r := mustCall(t, tool, map[string]any{
		"files": map[string]string{
			"a.ts": "const x = y as any; // TODO fix later",
		},
		"summary_only": false,
	}).(map[string]any)
	if r["finding_count"].(int) == 0 {
		t.Fatalf("expected findings in files mode, got %v", r)
	}
	if _, ok := r["by_file"]; !ok {
		t.Errorf("non-summary files-mode output should include by_file: %v", r)
	}
}

// ─── yagura_ai_verify files mode (untested annotation) ───────────

// TestAIVerify_FilesModeAnnotatesUntested covers the len(in.Files) > 0 path
// that runs the testcoverage join + AnnotateUntested.
func TestAIVerify_FilesModeAnnotatesUntested(t *testing.T) {
	d := newDeps(t)
	tool := buildAIVerifyTool(d, nil)
	r := mustCall(t, tool, map[string]any{
		"files": map[string]string{
			"svc.go":      "package x\nfunc Charge() {}",
			"svc_test.go": "package x\nimport \"testing\"\nfunc TestCharge(t *testing.T){}",
		},
	}).(map[string]any)
	if _, ok := r["files_scanned"]; !ok {
		t.Errorf("ai_verify files mode should return a result: %v", r)
	}
}

// ─── yagura_test_audit invalid args ──────────────────────────────

func TestTestAudit_InvalidArgs(t *testing.T) {
	d := newDeps(t)
	tool := buildTestAuditTool(d)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}
