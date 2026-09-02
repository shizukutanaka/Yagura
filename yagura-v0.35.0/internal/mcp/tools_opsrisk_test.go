package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOpsRiskTool(t *testing.T) {
	tool := buildOpsRiskTool(Deps{})
	out, err := tool.Handler(context.Background(), json.RawMessage(`{
		"operations": [
			{"name": "read_config", "capability": "read"},
			{"name": "charge_card", "capability": "billing"},
			{"name": "write_file", "capability": "write", "has_gate": true}
		]
	}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	b, _ := json.Marshal(out)
	var r struct {
		Worst     string         `json:"worst"`
		ByTier    map[string]int `json:"by_tier"`
		Decisions []struct {
			Name string `json:"name"`
			Tier string `json:"tier"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Worst != "human" {
		t.Errorf("worst should be human (billing present), got %q (%s)", r.Worst, b)
	}
	byName := map[string]string{}
	for _, d := range r.Decisions {
		byName[d.Name] = d.Tier
	}
	if byName["read_config"] != "auto" || byName["charge_card"] != "human" || byName["write_file"] != "log" {
		t.Errorf("unexpected tiers: %+v", byName)
	}
}

func TestOpsRiskTool_EmptyOps(t *testing.T) {
	tool := buildOpsRiskTool(Deps{})
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"operations":[]}`)); err == nil {
		t.Error("expected error for empty operations")
	}
}

func TestOpsRiskTool_BadInput(t *testing.T) {
	tool := buildOpsRiskTool(Deps{})
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
}
