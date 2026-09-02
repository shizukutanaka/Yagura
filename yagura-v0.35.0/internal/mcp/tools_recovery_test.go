package mcp

import (
	"encoding/json"
	"testing"
)

func TestRecoveryDecideTool(t *testing.T) {
	tool := buildRecoveryDecideTool(newDeps(t))
	res := mustCall(t, tool, map[string]any{"class": "quota", "attempt": 1, "agent": "claude_code"})
	b, _ := json.Marshal(res)
	var out struct {
		Action string `json:"action"`
		Budget struct {
			Remaining int `json:"remaining"`
		} `json:"budget"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if out.Action != "substitute_agent" {
		t.Errorf("quota should substitute_agent, got %q", out.Action)
	}
	if out.Budget.Remaining != 2 { // default max 3, attempt 1
		t.Errorf("budget remaining should be 2, got %d", out.Budget.Remaining)
	}
	// alias + escalate path.
	r2 := mustCall(t, tool, map[string]any{"class": "403", "attempt": 1})
	b2, _ := json.Marshal(r2)
	var o2 struct {
		Action   string `json:"action"`
		Terminal bool   `json:"terminal"`
	}
	_ = json.Unmarshal(b2, &o2)
	if o2.Action != "escalate" || !o2.Terminal {
		t.Errorf("'403' (auth) should escalate terminally, got %+v", o2)
	}
	// missing class → error.
	bad, _ := json.Marshal(map[string]any{"attempt": 1})
	if _, err := tool.Handler(tCtx(), bad); err == nil {
		t.Error("expected error when class is missing")
	}
}
