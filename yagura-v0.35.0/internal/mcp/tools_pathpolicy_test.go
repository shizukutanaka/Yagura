package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPathPolicyTool(t *testing.T) {
	tool := buildPathPolicyTool(Deps{})
	out, err := tool.Handler(context.Background(), json.RawMessage(`{
		"policy": {"rules": [
			{"path": "go.mod", "action": "deny", "reason": "ADR-0001"},
			{"path": "internal/audit/**", "action": "review"}
		]},
		"changed": ["go.mod", "internal/audit/audit.go", "README.md"]
	}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	b, _ := json.Marshal(out)
	var r struct {
		Worst   string   `json:"worst"`
		Denied  []string `json:"denied"`
		Review  []string `json:"review"`
		Allowed int      `json:"allowed"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Worst != "deny" {
		t.Errorf("worst should be deny, got %q (%s)", r.Worst, b)
	}
	if len(r.Denied) != 1 || len(r.Review) != 1 || r.Allowed != 1 {
		t.Errorf("counts off: %+v", r)
	}
}

func TestPathPolicyTool_EmptyChanged(t *testing.T) {
	tool := buildPathPolicyTool(Deps{})
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"policy":{},"changed":[]}`)); err == nil {
		t.Error("expected error for empty changed")
	}
}

func TestPathPolicyTool_BadInput(t *testing.T) {
	tool := buildPathPolicyTool(Deps{})
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
}
