package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/agentparallel"
	"github.com/shizukutanaka/yagura/internal/quotamonitor"
)

// planFromResult は tool Handler の戻り値を agentparallel.Plan に変換する
// (Handler は値を直接返すが、JSON 往復で client が見る形と一致させて検証する)。
func planFromResult(t *testing.T, v any) agentparallel.Plan {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var p agentparallel.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return p
}

func TestParallelPlan_TaskCountFanOut(t *testing.T) {
	tool := buildParallelPlanTool(newDeps(t))
	res := mustCall(t, tool, map[string]any{
		"task_count": 6,
		"agents": []map[string]any{
			{"name": "a", "tier": "mid", "capacity_percent": 100, "max_concurrency": 3},
			{"name": "b", "tier": "mid", "capacity_percent": 100, "max_concurrency": 3},
		},
	})
	p := planFromResult(t, res)
	if p.Strategy != "data" || !p.Barrier {
		t.Errorf("expected data strategy with barrier, got %+v", p)
	}
	if p.FanOutWidth != 2 {
		t.Errorf("expected fan-out 2, got %d", p.FanOutWidth)
	}
	total := 0
	for _, a := range p.Assignments {
		total += len(a.Tasks)
	}
	if total != 6 {
		t.Errorf("all 6 synthesized tasks must be assigned, got %d", total)
	}
}

func TestParallelPlan_TierAliasRouting(t *testing.T) {
	// opus/haiku aliases + a strong task must land on the opus-tier agent.
	tool := buildParallelPlanTool(newDeps(t))
	res := mustCall(t, tool, map[string]any{
		"tasks": []map[string]any{
			{"id": "hard", "min_tier": "opus"},
			{"id": "easy"},
		},
		"agents": []map[string]any{
			{"name": "cheap", "tier": "haiku", "capacity_percent": 100},
			{"name": "strong", "tier": "opus", "capacity_percent": 100},
		},
	})
	p := planFromResult(t, res)
	for _, a := range p.Assignments {
		if a.Agent == "cheap" {
			for _, id := range a.Tasks {
				if id == "hard" {
					t.Error("strong task routed to haiku-tier agent")
				}
			}
		}
	}
	if len(p.Unassigned) != 0 {
		t.Errorf("nothing should be unassigned, got %+v", p.Unassigned)
	}
}

func TestParallelPlan_QuotaFill(t *testing.T) {
	// capacity_percent omitted for a known agent → filled from live quotamonitor.
	mon := quotamonitor.New()
	if err := mon.Report(quotamonitor.AgentClaudeCode, 10, "manual", time.Time{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := mon.Report(quotamonitor.AgentWindsurf, 90, "manual", time.Time{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	d := newDeps(t)
	d.QuotaMonitor = mon
	tool := buildParallelPlanTool(d)
	res := mustCall(t, tool, map[string]any{
		"task_count": 10,
		"agents": []map[string]any{
			{"name": "claude_code", "tier": "strong"}, // capacity omitted → 10
			{"name": "windsurf", "tier": "mid"},       // capacity omitted → 90
		},
	})
	p := planFromResult(t, res)
	// windsurf has 9x the live quota, so it should carry the bulk.
	var cc, ws int
	for _, a := range p.Assignments {
		switch a.Agent {
		case "claude_code":
			cc = len(a.Tasks)
		case "windsurf":
			ws = len(a.Tasks)
		}
	}
	if ws <= cc {
		t.Errorf("higher live-quota agent (windsurf) should carry more: ws=%d cc=%d", ws, cc)
	}
	foundNote := false
	for _, n := range p.Notes {
		if strings.Contains(n, "live quotamonitor quota") {
			foundNote = true
		}
	}
	if !foundNote {
		t.Errorf("expected a note about quota fill, got %+v", p.Notes)
	}
}

func TestParallelPlan_Validation(t *testing.T) {
	tool := buildParallelPlanTool(newDeps(t))
	cases := []struct {
		name string
		args map[string]any
	}{
		{"no agents", map[string]any{"task_count": 3}},
		{"no tasks", map[string]any{"agents": []map[string]any{{"name": "a", "tier": "mid", "capacity_percent": 100}}}},
		{"bad tier", map[string]any{"task_count": 1, "agents": []map[string]any{{"name": "a", "tier": "ultra"}}}},
		{"bad strategy", map[string]any{"task_count": 1, "strategy": "pipeline", "agents": []map[string]any{{"name": "a", "tier": "mid"}}}},
		{"task_count too big", map[string]any{"task_count": maxSynthTasks + 1, "agents": []map[string]any{{"name": "a", "tier": "mid"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, _ := json.Marshal(c.args)
			if _, err := tool.Handler(context.Background(), b); err == nil {
				t.Errorf("%s: expected error, got nil", c.name)
			}
		})
	}
}
