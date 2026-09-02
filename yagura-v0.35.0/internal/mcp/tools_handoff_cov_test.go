package mcp

import (
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/quotamonitor"
)

// ─── quota_report: default source, range error, invalid args ─────

// TestQuotaReport_DefaultSource omits "source" so the handler defaults it to
// "manual" (the source == "" branch).
func TestQuotaReport_DefaultSource(t *testing.T) {
	d := handoffDeps(t)
	tool := buildQuotaReportTool(d)
	r := mustCall(t, tool, map[string]any{
		"agent":             "claude_code",
		"remaining_percent": 70,
	}).(map[string]any)
	if r["recorded"] == nil {
		t.Error("recorded field should be present when source defaulted to manual")
	}
}

// TestQuotaReport_RangeError covers the Report() error path: a percent outside
// 0..100 is rejected by the monitor (after the agent name validates).
func TestQuotaReport_RangeError(t *testing.T) {
	d := handoffDeps(t)
	tool := buildQuotaReportTool(d)
	_, err := callErr(t, tool, map[string]any{
		"agent":             "claude_code",
		"remaining_percent": 150,
	})
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input for out-of-range percent, got %v", err)
	}
}

func TestQuotaReport_InvalidArgs(t *testing.T) {
	d := handoffDeps(t)
	tool := buildQuotaReportTool(d)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

// ─── heartbeat / forecast / usage_summary: invalid args ──────────

func TestHeartbeat_InvalidArgs(t *testing.T) {
	d := handoffDeps(t)
	tool := buildHeartbeatTool(d)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestQuotaForecast_InvalidArgs(t *testing.T) {
	d := handoffDeps(t)
	tool := buildQuotaForecastTool(d)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestUsageSummary_InvalidArgs(t *testing.T) {
	d := handoffDeps(t)
	tool := buildUsageSummaryTool(d)
	if err := callRawErr(t, tool, "[1,2,3]"); !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

// ─── forecast: minutes-until-empty (future prediction) ───────────

// TestQuotaForecast_FuturePrediction feeds a slow linear depletion ending near
// real "now" so PredictedEmptyAt lands in the future, exercising the
// minutes_until_empty (remaining > 0) branch.
func TestQuotaForecast_FuturePrediction(t *testing.T) {
	d := handoffDeps(t)
	qm := d.QuotaMonitor.(*quotamonitor.Monitor)
	base := time.Now()
	// 4 samples over the last 30s, depleting 1%/10s → empty ~hundreds of s out.
	samples := []struct {
		off time.Duration
		pct int
	}{
		{-30 * time.Second, 100},
		{-20 * time.Second, 99},
		{-10 * time.Second, 98},
		{0, 97},
	}
	for _, s := range samples {
		when := base.Add(s.off)
		qm.NowFn = func() time.Time { return when }
		_ = qm.Report(quotamonitor.AgentClaudeCode, s.pct, "manual", time.Time{}, time.Time{})
	}
	tool := buildQuotaForecastTool(d)
	r := mustCall(t, tool, map[string]any{"agent": "claude_code"}).(map[string]any)
	if r["minutes_until_empty"].(string) == "" {
		t.Errorf("expected a non-empty minutes_until_empty for a future prediction, got %v", r)
	}
}
