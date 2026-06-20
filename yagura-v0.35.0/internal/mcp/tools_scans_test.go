package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/aiverify"
	"github.com/shizukutanaka/yagura/internal/dedupe"
	"github.com/shizukutanaka/yagura/internal/ghaaudit"
	"github.com/shizukutanaka/yagura/internal/sbom"
)

// ─── yagura_gha_audit ────────────────────────────────────────

func TestGhaAudit_Unavailable(t *testing.T) {
	tool := buildGhaAuditTool(newDeps(t)) // Ghaaudit nil
	b, _ := json.Marshal(map[string]any{"files": map[string]string{"ci.yml": "x"}})
	if _, err := tool.Handler(context.Background(), b); !IsCode(err, "unavailable") {
		t.Errorf("expected unavailable when no auditor configured, got %v", err)
	}
}

func TestGhaAudit_InvalidInputs(t *testing.T) {
	d := newDeps(t)
	d.Ghaaudit = ghaaudit.New()
	tool := buildGhaAuditTool(d)
	for name, args := range map[string]string{
		"bad json":    `{`,
		"empty files": `{"files":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.Handler(context.Background(), json.RawMessage(args)); !IsCode(err, "invalid_input") {
				t.Errorf("expected invalid_input, got %v", err)
			}
		})
	}
}

func TestGhaAudit_FlagsUnpinnedAction(t *testing.T) {
	d := newDeps(t)
	d.Ghaaudit = ghaaudit.New()
	tool := buildGhaAuditTool(d)
	wf := "name: ci\non: [push]\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n"
	r := mustCall(t, tool, map[string]any{"files": map[string]string{".github/workflows/ci.yml": wf}}).(map[string]any)
	if r["results"] == nil || r["summary"] == nil {
		t.Fatalf("expected results+summary, got %v", r)
	}
	// a mutable-tag action must be flagged as unpinned-uses
	js, _ := json.Marshal(r)
	if !strings.Contains(string(js), "unpinned-uses") {
		t.Errorf("expected an unpinned-uses finding:\n%s", js)
	}
}

func TestGhaAudit_SummaryOnly(t *testing.T) {
	d := newDeps(t)
	d.Ghaaudit = ghaaudit.New()
	tool := buildGhaAuditTool(d)
	wf := "name: ci\non: [push]\njobs:\n  b:\n    steps:\n      - uses: actions/checkout@v4\n"
	out := mustCall(t, tool, map[string]any{"files": map[string]string{"w.yml": wf}, "summary_only": true})
	// summary-only returns the Summarize() shape, not the {results,summary} map
	if m, ok := out.(map[string]any); ok && m["results"] != nil {
		t.Errorf("summary_only should not include the full results list: %v", m)
	}
}

// ─── yagura_sbom ─────────────────────────────────────────────

func TestSbom_Unavailable(t *testing.T) {
	tool := buildSbomTool(newDeps(t)) // Sbom nil
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); !IsCode(err, "unavailable") {
		t.Errorf("expected unavailable when no generator configured, got %v", err)
	}
}

func TestSbom_GeneratesAndSummarizes(t *testing.T) {
	d := newDeps(t)
	d.Sbom = sbom.New()
	d.MainModulePath = "github.com/shizukutanaka/yagura"
	d.MainVersion = "0.35.0"
	tool := buildSbomTool(d)

	// full doc
	full := mustCall(t, tool, map[string]any{})
	js, _ := json.Marshal(full)
	if !strings.Contains(string(js), "github.com/shizukutanaka/yagura") {
		t.Errorf("sbom should reference the main module:\n%s", js)
	}
	// summary_only returns a compact summary (different shape, still non-nil)
	if s := mustCall(t, tool, map[string]any{"summary_only": true}); s == nil {
		t.Error("summary_only returned nil")
	}
}

// ─── yagura_ai_verify ────────────────────────────────────────

func TestAIVerify_InvalidInputs(t *testing.T) {
	tool := buildAIVerifyTool(newDeps(t), nil)
	for name, args := range map[string]string{
		"bad json":         `{`,
		"no files or text": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.Handler(context.Background(), json.RawMessage(args)); !IsCode(err, "invalid_input") {
				t.Errorf("expected invalid_input, got %v", err)
			}
		})
	}
}

func TestAIVerify_FlagsRiskPattern(t *testing.T) {
	tool := buildAIVerifyTool(newDeps(t), nil)
	r := mustCall(t, tool, map[string]any{
		"text": "hash := md5(password)\n",
		"path": "auth.go",
	}).(map[string]any)
	if fc, _ := r["finding_count"].(int); fc < 1 {
		t.Errorf("expected >=1 finding for md5(password), got %v (%v)", r["finding_count"], r)
	}
	if r["by_severity"] == nil || r["summary"] == nil {
		t.Errorf("expected by_severity+summary in result: %v", r)
	}
}

func TestAIVerify_SummaryOnly(t *testing.T) {
	tool := buildAIVerifyTool(newDeps(t), nil)
	r := mustCall(t, tool, map[string]any{"text": "x := 1\n", "summary_only": true}).(map[string]any)
	// summary_only omits the per-finding list but keeps the aggregate fields
	if _, hasFindings := r["findings"]; hasFindings {
		t.Errorf("summary_only should omit findings list: %v", r)
	}
	if r["summary"] == nil {
		t.Error("summary_only should still include the summary")
	}
}

func TestAIVerify_FilesMode_NoCache(t *testing.T) {
	// exercises the `in.Files != ""` branch (aiverify.Scan(in.Files))
	// and the testcoverage integration block (len(in.Files) > 0)
	tool := buildAIVerifyTool(newDeps(t), nil)
	files := map[string]string{
		"auth.go":      "package p\nfunc login(pw string) { h := md5(pw) }\n",
		"auth_test.go": "package p\nimport \"testing\"\nfunc TestLogin(t *testing.T){}\n",
	}
	r := mustCall(t, tool, map[string]any{"files": files}).(map[string]any)
	if r["files_scanned"] == nil {
		t.Error("files_scanned should be present in files-mode result")
	}
	if r["findings"] == nil {
		t.Error("findings should be present in non-summary-only files-mode result")
	}
}

func TestAIVerify_FilesMode_WithCache(t *testing.T) {
	// exercises the `cache != nil` branch (aiverify.ScanCached)
	cache := dedupe.New(0, 0)
	tool := buildAIVerifyTool(newDeps(t), cache)
	files := map[string]string{
		"main.go": "package main\nfunc main() { _ = os.Getenv(\"SECRET\") }\n",
	}
	r := mustCall(t, tool, map[string]any{"files": files}).(map[string]any)
	if r["files_scanned"] == nil {
		t.Error("files_scanned should be present in cached files-mode result")
	}
}

func TestAIVerify_FilesMode_SummaryOnly(t *testing.T) {
	// summary_only + files mode: findings omitted, testcoverage integration still runs
	tool := buildAIVerifyTool(newDeps(t), nil)
	files := map[string]string{
		"a.go": "package p",
	}
	r := mustCall(t, tool, map[string]any{"files": files, "summary_only": true}).(map[string]any)
	if _, hasFindings := r["findings"]; hasFindings {
		t.Errorf("summary_only should omit findings even in files mode: %v", r)
	}
}

// ─── formatAIVerifyResult edge cases ─────────────────────────

func TestFormatAIVerifyResult_AIGenWithoutTests_Included(t *testing.T) {
	res := aiverify.Result{
		AIGenWithoutTests: []string{"pkg/foo.go"},
	}
	out := formatAIVerifyResult(res).(map[string]any)
	if _, ok := out["ai_gen_without_tests"]; !ok {
		t.Error("ai_gen_without_tests should be present when non-empty")
	}
}

func TestFormatAIVerifyResult_AIGenWithoutTests_EmptyOmitted(t *testing.T) {
	res := aiverify.Result{AIGenWithoutTests: nil}
	out := formatAIVerifyResult(res).(map[string]any)
	if _, ok := out["ai_gen_without_tests"]; ok {
		t.Error("ai_gen_without_tests should be absent when empty")
	}
}

func TestFormatAIVerifyResult_CacheStats_Present(t *testing.T) {
	res := aiverify.Result{CacheHits: 3, CacheMisses: 1}
	out := formatAIVerifyResult(res).(map[string]any)
	if out["cache_hits"] == nil || out["cache_misses"] == nil {
		t.Error("cache_hits/cache_misses should be present when non-zero")
	}
}

func TestFormatAIVerifyResult_CacheStats_Absent(t *testing.T) {
	res := aiverify.Result{CacheHits: 0, CacheMisses: 0}
	out := formatAIVerifyResult(res).(map[string]any)
	if _, ok := out["cache_hits"]; ok {
		t.Error("cache_hits should be absent when both are zero")
	}
}

func TestFormatAIVerifyResult_NotSummaryOnly_FindingsIncluded(t *testing.T) {
	res := aiverify.Result{
		Findings: []aiverify.Finding{{File: "x.go"}},
	}
	out := formatAIVerifyResult(res).(map[string]any)
	if out["findings"] == nil {
		t.Error("findings should be present when summary_only=false")
	}
}
