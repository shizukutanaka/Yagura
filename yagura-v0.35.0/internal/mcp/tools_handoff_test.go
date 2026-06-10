package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/agentlauncher"
	"github.com/shizukutanaka/yagura/internal/handoff"
	"github.com/shizukutanaka/yagura/internal/quotamonitor"
)

// ─── test helpers ────────────────────────────────────────────

func handoffDeps(t *testing.T) Deps {
	t.Helper()
	d := newDeps(t)
	qm := quotamonitor.New()
	hs, err := handoff.New(t.TempDir())
	if err != nil {
		t.Fatalf("handoff.New: %v", err)
	}
	d.QuotaMonitor = qm
	d.HandoffStore = hs
	d.AgentLauncher = agentlauncher.New()
	d.WorkspaceRoot = t.TempDir()
	return d
}

// ─── nil-guard: all handoff tools return "unavailable" when deps nil ──

func TestQuotaReport_Unavailable(t *testing.T) {
	d := newDeps(t) // QuotaMonitor is nil
	tool := buildQuotaReportTool(d)
	_, err := callErr(t, tool, map[string]any{"agent": "claude_code", "remaining_percent": 50})
	if err == nil {
		t.Error("nil QuotaMonitor should return error")
	}
	if te, ok := err.(*ToolError); !ok || te.Code != "unavailable" {
		t.Errorf("expected unavailable ToolError, got %v", err)
	}
}

func TestAgentStatus_Unavailable(t *testing.T) {
	d := newDeps(t)
	tool := buildAgentStatusTool(d)
	_, err := callErr(t, tool, struct{}{})
	if err == nil || err.(*ToolError).Code != "unavailable" {
		t.Errorf("nil QuotaMonitor should return unavailable error: %v", err)
	}
}

func TestSessionSave_Unavailable(t *testing.T) {
	d := newDeps(t)
	tool := buildSessionSaveTool(d)
	_, err := callErr(t, tool, map[string]any{"workspace": "/tmp/w"})
	if err == nil || err.(*ToolError).Code != "unavailable" {
		t.Errorf("nil HandoffStore should return unavailable error: %v", err)
	}
}

func TestSessionLoad_Unavailable(t *testing.T) {
	d := newDeps(t)
	tool := buildSessionLoadTool(d)
	_, err := callErr(t, tool, struct{}{})
	if err == nil || err.(*ToolError).Code != "unavailable" {
		t.Errorf("nil HandoffStore should return unavailable error: %v", err)
	}
}

func TestHandoff_Unavailable(t *testing.T) {
	d := newDeps(t)
	tool := buildHandoffTool(d)
	_, err := callErr(t, tool, map[string]any{"target": "windsurf"})
	if err == nil || err.(*ToolError).Code != "unavailable" {
		t.Errorf("nil deps should return unavailable error: %v", err)
	}
}

func TestHeartbeat_Unavailable(t *testing.T) {
	d := newDeps(t)
	tool := buildHeartbeatTool(d)
	_, err := callErr(t, tool, map[string]any{"agent": "claude_code"})
	if err == nil || err.(*ToolError).Code != "unavailable" {
		t.Errorf("nil QuotaMonitor should return unavailable error: %v", err)
	}
}

func TestQuotaForecast_Unavailable(t *testing.T) {
	d := newDeps(t)
	tool := buildQuotaForecastTool(d)
	_, err := callErr(t, tool, map[string]any{"agent": "claude_code"})
	if err == nil || err.(*ToolError).Code != "unavailable" {
		t.Errorf("nil QuotaMonitor should return unavailable error: %v", err)
	}
}

func TestUsageSummary_Unavailable(t *testing.T) {
	d := newDeps(t)
	tool := buildUsageSummaryTool(d)
	_, err := callErr(t, tool, struct{}{})
	if err == nil || err.(*ToolError).Code != "unavailable" {
		t.Errorf("nil QuotaMonitor should return unavailable error: %v", err)
	}
}

// ─── yagura_quota_report ──────────────────────────────────────

func TestQuotaReport_Happy(t *testing.T) {
	d := handoffDeps(t)
	tool := buildQuotaReportTool(d)
	r := mustCall(t, tool, map[string]any{
		"agent":             "claude_code",
		"remaining_percent": 80,
		"source":            "manual",
	}).(map[string]any)
	if r["recorded"] == nil {
		t.Error("recorded field should be present")
	}
	if _, ok := r["should_handoff"]; !ok {
		t.Error("should_handoff field should be present")
	}
}

func TestQuotaReport_InvalidAgent(t *testing.T) {
	d := handoffDeps(t)
	tool := buildQuotaReportTool(d)
	_, err := callErr(t, tool, map[string]any{
		"agent":             "unknown_bot",
		"remaining_percent": 50,
	})
	if err == nil {
		t.Error("invalid agent name should return error")
	}
}

// ─── yagura_agent_status ─────────────────────────────────────

func TestAgentStatus_Happy(t *testing.T) {
	d := handoffDeps(t)
	tool := buildAgentStatusTool(d)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	if _, ok := r["statuses"]; !ok {
		t.Error("statuses field should be present")
	}
	if _, ok := r["recommended_agent"]; !ok {
		t.Error("recommended_agent field should be present")
	}
}

// ─── yagura_session_save / _load ─────────────────────────────

func TestSessionSaveAndLoad(t *testing.T) {
	d := handoffDeps(t)
	saveTool := buildSessionSaveTool(d)
	loadTool := buildSessionLoadTool(d)

	// Save a context
	r := mustCall(t, saveTool, map[string]any{
		"workspace":   "/home/user/project",
		"saved_by":    "claude_code",
		"branch":      "feat/x",
		"free_notes":  "mid-session note",
	}).(map[string]any)
	if r["saved"] != true {
		t.Errorf("saved = %v, want true", r["saved"])
	}

	// Load it back
	lr := mustCall(t, loadTool, struct{}{}).(map[string]any)
	if lr["context"] == nil {
		t.Error("loaded context should not be nil after save")
	}
}

func TestSessionLoad_NothingSaved(t *testing.T) {
	d := handoffDeps(t)
	tool := buildSessionLoadTool(d)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	// ErrNotSaved is handled gracefully — no error, context is nil
	if r["context"] != nil {
		t.Errorf("context should be nil when nothing saved, got %v", r["context"])
	}
}

// ─── yagura_handoff ──────────────────────────────────────────

func TestHandoff_DryRun(t *testing.T) {
	d := handoffDeps(t)
	tool := buildHandoffTool(d)
	r := mustCall(t, tool, map[string]any{
		"target":   "windsurf",
		"dry_run":  true,
	}).(map[string]any)
	if r["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", r["dry_run"])
	}
	if r["target_agent"] != "windsurf" {
		t.Errorf("target_agent = %v, want windsurf", r["target_agent"])
	}
	if r["handoff_complete"] != false {
		t.Errorf("dry_run should not set handoff_complete=true")
	}
}

func TestHandoff_DryRunClaudeCode(t *testing.T) {
	d := handoffDeps(t)
	tool := buildHandoffTool(d)
	r := mustCall(t, tool, map[string]any{
		"target":  "claude_code",
		"dry_run": true,
	}).(map[string]any)
	if r["source_agent"] != "windsurf" {
		t.Errorf("when target=claude_code, source should be windsurf, got %v", r["source_agent"])
	}
}

func TestHandoff_InvalidTarget(t *testing.T) {
	d := handoffDeps(t)
	tool := buildHandoffTool(d)
	_, err := callErr(t, tool, map[string]any{"target": "gpt-99", "dry_run": true})
	if err == nil {
		t.Error("invalid target should return error")
	}
}

func TestHandoff_NoWorkspaceError(t *testing.T) {
	d := handoffDeps(t)
	d.WorkspaceRoot = "" // no default workspace
	tool := buildHandoffTool(d)
	_, err := callErr(t, tool, map[string]any{
		"target":  "windsurf",
		"dry_run": true,
		// no workspace field either
	})
	if err == nil {
		t.Error("missing workspace should return error")
	}
	if te, ok := err.(*ToolError); ok && !strings.Contains(te.Message, "workspace") {
		t.Errorf("error should mention workspace: %v", te.Message)
	}
}

// ─── yagura_heartbeat ────────────────────────────────────────

func TestHeartbeat_Happy(t *testing.T) {
	d := handoffDeps(t)
	tool := buildHeartbeatTool(d)
	r := mustCall(t, tool, map[string]any{"agent": "claude_code"}).(map[string]any)
	if r["recorded"] != true {
		t.Errorf("recorded = %v, want true", r["recorded"])
	}
	if r["agent"] != "claude_code" {
		t.Errorf("agent = %v, want claude_code", r["agent"])
	}
	if r["at"] == nil {
		t.Error("at field should be present")
	}
}

func TestHeartbeat_InvalidAgent(t *testing.T) {
	d := handoffDeps(t)
	tool := buildHeartbeatTool(d)
	_, err := callErr(t, tool, map[string]any{"agent": "ghost"})
	if err == nil {
		t.Error("invalid agent should return error")
	}
}

// ─── yagura_quota_forecast ───────────────────────────────────

func TestQuotaForecast_InsufficientSamples(t *testing.T) {
	d := handoffDeps(t)
	tool := buildQuotaForecastTool(d)
	// Fresh monitor has no history — forecast gracefully returns zero prediction
	r := mustCall(t, tool, map[string]any{"agent": "claude_code"}).(map[string]any)
	if r["agent"] != "claude_code" {
		t.Errorf("agent = %v, want claude_code", r["agent"])
	}
	if r["forecast"] == nil {
		t.Error("forecast field should be present even with no samples")
	}
}

func TestQuotaForecast_InvalidAgent(t *testing.T) {
	d := handoffDeps(t)
	tool := buildQuotaForecastTool(d)
	_, err := callErr(t, tool, map[string]any{"agent": "bogus"})
	if err == nil {
		t.Error("invalid agent should return error")
	}
}

func TestQuotaForecast_AfterReports(t *testing.T) {
	d := handoffDeps(t)
	qm := d.QuotaMonitor.(*quotamonitor.Monitor)
	// Feed 4 data points to satisfy ≥3 samples for forecast
	for i, pct := range []int{90, 60, 30, 10} {
		resetAt := time.Now().Add(time.Duration(i+1) * time.Hour)
		if err := qm.Report(quotamonitor.AgentClaudeCode, pct, "test", resetAt, resetAt); err != nil {
			t.Fatalf("Report %d: %v", i, err)
		}
	}
	tool := buildQuotaForecastTool(d)
	r := mustCall(t, tool, map[string]any{"agent": "claude_code"}).(map[string]any)
	if r["forecast"] == nil {
		t.Error("forecast should be non-nil after sufficient reports")
	}
}

// ─── yagura_usage_summary ────────────────────────────────────

func TestUsageSummary_BothAgents(t *testing.T) {
	d := handoffDeps(t)
	tool := buildUsageSummaryTool(d)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	if r["summaries"] == nil {
		t.Error("summaries field should be present for both-agents default")
	}
}

func TestUsageSummary_SpecificAgent(t *testing.T) {
	d := handoffDeps(t)
	tool := buildUsageSummaryTool(d)
	r := mustCall(t, tool, map[string]any{"agent": "windsurf"}).(map[string]any)
	if r["summary"] == nil {
		t.Error("summary field should be present for specific agent")
	}
}

func TestUsageSummary_InvalidAgent(t *testing.T) {
	d := handoffDeps(t)
	tool := buildUsageSummaryTool(d)
	_, err := callErr(t, tool, map[string]any{"agent": "gpt4"})
	if err == nil {
		t.Error("invalid agent should return error")
	}
}

// ─── handoff/session error + real-launch branches ────────────

// failingHandoffStore fails Save/Load to drive the save_failed/load_failed branches.
type failingHandoffStore struct{}

func (failingHandoffStore) Save(*handoff.Context) error { return errors.New("disk full") }
func (failingHandoffStore) Load() (*handoff.Context, error) {
	return nil, errors.New("corrupt file")
}
func (failingHandoffStore) Clear() error { return nil }
func (failingHandoffStore) Path() string { return "/dev/null/handoff.json" }

// failingLauncher fails every launch to drive the launch_failed branch.
type failingLauncher struct{}

func (failingLauncher) LaunchWindsurf(context.Context, string) error {
	return errors.New("windsurf not installed")
}
func (failingLauncher) LaunchClaudeCode(context.Context, string) error {
	return errors.New("claude not installed")
}
func (failingLauncher) LastCommand() (string, []string) { return "", nil }

func TestSessionSave_SaveFails(t *testing.T) {
	d := handoffDeps(t)
	d.HandoffStore = failingHandoffStore{}
	tool := buildSessionSaveTool(d)
	_, err := callErr(t, tool, map[string]any{"workspace": "/ws"})
	if err == nil {
		t.Fatal("expected save_failed error")
	}
	if te, ok := err.(*ToolError); !ok || te.Code != "save_failed" {
		t.Errorf("expected ToolError save_failed, got %v", err)
	}
}

func TestSessionSave_WorkspaceDefaultsToRoot(t *testing.T) {
	d := handoffDeps(t)
	tool := buildSessionSaveTool(d)
	// No workspace in args → falls back to d.WorkspaceRoot.
	r := mustCall(t, tool, map[string]any{"saved_by": "claude_code"}).(map[string]any)
	if r["workspace"] != d.WorkspaceRoot {
		t.Errorf("workspace = %v, want WorkspaceRoot %q", r["workspace"], d.WorkspaceRoot)
	}
}

func TestSessionSave_InvalidJSON(t *testing.T) {
	d := handoffDeps(t)
	tool := buildSessionSaveTool(d)
	_, err := tool.Handler(context.Background(), []byte("{not json"))
	if err == nil {
		t.Error("expected invalid_input error for malformed JSON")
	}
}

func TestSessionLoad_LoadFails(t *testing.T) {
	d := handoffDeps(t)
	d.HandoffStore = failingHandoffStore{}
	tool := buildSessionLoadTool(d)
	_, err := callErr(t, tool, struct{}{})
	if err == nil {
		t.Fatal("expected load_failed error for non-ErrNotSaved failure")
	}
	if te, ok := err.(*ToolError); !ok || te.Code != "load_failed" {
		t.Errorf("expected ToolError load_failed, got %v", err)
	}
}

func TestHandoff_SaveFails(t *testing.T) {
	d := handoffDeps(t)
	d.HandoffStore = failingHandoffStore{}
	tool := buildHandoffTool(d)
	_, err := callErr(t, tool, map[string]any{"target": "windsurf", "dry_run": true})
	if err == nil {
		t.Fatal("expected save_failed error")
	}
	if te, ok := err.(*ToolError); !ok || te.Code != "save_failed" {
		t.Errorf("expected ToolError save_failed, got %v", err)
	}
}

func TestHandoff_LaunchFails(t *testing.T) {
	d := handoffDeps(t)
	d.AgentLauncher = failingLauncher{}
	tool := buildHandoffTool(d)
	_, err := callErr(t, tool, map[string]any{"target": "windsurf"}) // dry_run=false
	if err == nil {
		t.Fatal("expected launch_failed error")
	}
	if te, ok := err.(*ToolError); !ok || te.Code != "launch_failed" {
		t.Errorf("expected ToolError launch_failed, got %v", err)
	}
}

func TestHandoff_RealLaunch_WindsurfDryLauncher(t *testing.T) {
	d := handoffDeps(t)
	// Launcher-level DryRun: the tool's non-dry-run path runs (handoff_complete
	// =true, LaunchWindsurf called) but no process is actually spawned.
	d.AgentLauncher = &agentlauncher.Launcher{DryRun: true}
	tool := buildHandoffTool(d)
	r := mustCall(t, tool, map[string]any{"target": "windsurf"}).(map[string]any)
	if r["handoff_complete"] != true {
		t.Errorf("handoff_complete = %v, want true", r["handoff_complete"])
	}
	cmd := r["launch_command"].([]string)
	if len(cmd) == 0 || cmd[0] == "" {
		t.Errorf("launch_command should record the assembled command, got %v", cmd)
	}
}

func TestHandoff_RealLaunch_ClaudeCodeDryLauncher(t *testing.T) {
	d := handoffDeps(t)
	d.AgentLauncher = &agentlauncher.Launcher{DryRun: true}
	tool := buildHandoffTool(d)
	r := mustCall(t, tool, map[string]any{"target": "claude_code"}).(map[string]any)
	if r["handoff_complete"] != true {
		t.Errorf("handoff_complete = %v, want true", r["handoff_complete"])
	}
	if r["source_agent"] != "windsurf" {
		t.Errorf("source_agent = %v, want windsurf", r["source_agent"])
	}
}

func TestHandoff_InvalidJSON(t *testing.T) {
	d := handoffDeps(t)
	tool := buildHandoffTool(d)
	_, err := tool.Handler(context.Background(), []byte("{bad"))
	if err == nil {
		t.Error("expected invalid_input error for malformed JSON")
	}
}
