package selfimprove

import (
	"encoding/json"
	"testing"
)

func ids(r Report) []string {
	out := make([]string, len(r.Proposals))
	for i, p := range r.Proposals {
		out[i] = p.ID
	}
	return out
}

func has(r Report, id string) *Proposal {
	for i := range r.Proposals {
		if r.Proposals[i].ID == id {
			return &r.Proposals[i]
		}
	}
	return nil
}

func TestAnalyze_Empty(t *testing.T) {
	r := Analyze(Snapshot{})
	if len(r.Proposals) != 0 {
		t.Errorf("expected no proposals, got %v", ids(r))
	}
	if r.Summary == "" {
		t.Error("summary should be set")
	}
}

func TestAnalyze_ReliabilityHighVsMedium(t *testing.T) {
	r := Analyze(Snapshot{Tools: []ToolStat{
		{Name: "tool_high", Calls: 10, Errors: 3}, // 30% → high
		{Name: "tool_med", Calls: 100, Errors: 8}, // 8% → medium
		{Name: "tool_ok", Calls: 100, Errors: 1},  // 1% → none
		{Name: "tool_few", Calls: 3, Errors: 3},   // below minCalls → none
	}})
	if p := has(r, "reliability:tool_high"); p == nil || p.Severity != "high" {
		t.Errorf("tool_high should be high reliability, got %+v", p)
	}
	if p := has(r, "reliability:tool_med"); p == nil || p.Severity != "medium" {
		t.Errorf("tool_med should be medium, got %+v", p)
	}
	if has(r, "reliability:tool_ok") != nil || has(r, "reliability:tool_few") != nil {
		t.Error("tool_ok / tool_few should not produce a proposal")
	}
}

func TestAnalyze_TokenEconomy(t *testing.T) {
	r := Analyze(Snapshot{
		SessionCalls: 100,
		Tools: []ToolStat{
			{Name: "chatty_big", Calls: 30, AvgRespBytes: 8000}, // big + 30% share → medium
			{Name: "rare_big", Calls: 2, AvgRespBytes: 9000},    // big but rare → skipped
			{Name: "small", Calls: 50, AvgRespBytes: 100},       // small → skipped
		},
	})
	if p := has(r, "token_economy:chatty_big"); p == nil || p.Severity != "medium" {
		t.Errorf("chatty_big should be medium token_economy, got %+v", p)
	}
	if has(r, "token_economy:rare_big") != nil {
		t.Error("rare_big should be skipped (low call share)")
	}
	if has(r, "token_economy:small") != nil {
		t.Error("small should be skipped")
	}
}

func TestAnalyze_Retire(t *testing.T) {
	r := Analyze(Snapshot{Skills: []SkillScore{
		{Path: "a/SKILL.md", Score: 30, Retire: true},
		{Path: "b/SKILL.md", Score: 95, Retire: false},
	}})
	if has(r, "retire:a/SKILL.md") == nil {
		t.Error("low-score skill should be a retire proposal")
	}
	if has(r, "retire:b/SKILL.md") != nil {
		t.Error("high-score skill should not be retired")
	}
}

func TestAnalyze_Coverage(t *testing.T) {
	r := Analyze(Snapshot{CoverageGaps: []string{"feedback:runtime"}})
	if p := has(r, "coverage:feedback:runtime"); p == nil || p.Severity != "medium" {
		t.Errorf("coverage gap should yield a medium proposal, got %+v", p)
	}
}

func TestAnalyze_FitnessRegression(t *testing.T) {
	r := Analyze(Snapshot{
		PrevTools: []ToolStat{{Name: "evolving", Calls: 100, Errors: 2}},  // 2%
		Tools:     []ToolStat{{Name: "evolving", Calls: 100, Errors: 20}}, // 20% → +18% regression
	})
	p := has(r, "fitness:evolving")
	if p == nil || p.Severity != "high" {
		t.Fatalf("expected high fitness regression proposal, got %+v", p)
	}
	// high severity ranks first
	if r.Proposals[0].Kind != "fitness" {
		t.Errorf("fitness regression should rank first, got %s", r.Proposals[0].Kind)
	}
}

func TestAnalyze_NoFitnessWhenImproved(t *testing.T) {
	r := Analyze(Snapshot{
		PrevTools: []ToolStat{{Name: "evolving", Calls: 100, Errors: 20}},
		Tools:     []ToolStat{{Name: "evolving", Calls: 100, Errors: 1}}, // improved
	})
	if has(r, "fitness:evolving") != nil {
		t.Error("improvement should not produce a regression proposal")
	}
}

func TestAnalyze_Deterministic_SeverityOrder(t *testing.T) {
	snap := Snapshot{
		SessionCalls: 100,
		Tools: []ToolStat{
			{Name: "z_high", Calls: 10, Errors: 5},            // high reliability
			{Name: "a_chatty", Calls: 30, AvgRespBytes: 8000}, // medium token_economy
		},
		Skills:       []SkillScore{{Path: "x", Score: 10, Retire: true}}, // low retire
		CoverageGaps: []string{"q1"},                                     // medium coverage
	}
	r1 := Analyze(snap)
	r2 := Analyze(snap)
	b1, _ := json.Marshal(r1)
	b2, _ := json.Marshal(r2)
	if string(b1) != string(b2) {
		t.Error("Analyze must be deterministic")
	}
	// order: high first, low (retire) last
	if r1.Proposals[0].Severity != "high" {
		t.Errorf("first proposal should be high, got %s", r1.Proposals[0].Severity)
	}
	if r1.Proposals[len(r1.Proposals)-1].Kind != "retire" {
		t.Errorf("retire (low) should be last, got %s", r1.Proposals[len(r1.Proposals)-1].Kind)
	}
}
