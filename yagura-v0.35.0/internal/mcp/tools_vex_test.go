package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func vexDeps() Deps {
	return Deps{Now: func() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }}
}

func callVEX(t *testing.T, args string) map[string]any {
	t.Helper()
	tool := buildVEXTool(vexDeps())
	out, err := tool.Handler(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output not a map: %T", out)
	}
	return m
}

func TestVEXTool_BuildAndValidate(t *testing.T) {
	m := callVEX(t, `{"author":"acme","statements":[
		{"cve":"CVE-2025-0001","status":"not_affected","justification":"component_not_present"},
		{"cve":"CVE-2025-0002","status":"affected","action":"upgrade to 2.0","product":"pkg:golang/x"}
	]}`)
	issues, _ := m["issues"].([]string)
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
	if m["document"] == nil {
		t.Error("expected document in output")
	}
}

func TestVEXTool_SurfacesIssues(t *testing.T) {
	m := callVEX(t, `{"statements":[{"cve":"CVE-2025-0001","status":"not_affected"}]}`)
	issues, _ := m["issues"].([]string)
	if len(issues) == 0 {
		t.Error("expected a lint issue for not_affected without justification")
	}
}

func TestVEXTool_MergeIntoBase(t *testing.T) {
	// base has CVE-0001 fixed; merge adds a new CVE-0002 without clobbering it.
	m := callVEX(t, `{"base":{
		"@context":"https://openvex.dev/ns/v0.2.0","@id":"urn:yagura:vex:deadbeef",
		"author":"acme","timestamp":"2026-06-06T12:00:00Z","version":1,
		"statements":[{"vulnerability":{"name":"CVE-2025-0001"},"status":"fixed"}]
	},"statements":[{"cve":"CVE-2025-0002","status":"affected","action":"upgrade"}]}`)

	// document is a typed vex.Document; round-trip through JSON like an MCP client.
	b, err := json.Marshal(m["document"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc struct {
		Version    int `json:"version"`
		Statements []struct {
			Vulnerability struct {
				Name string `json:"name"`
			} `json:"vulnerability"`
			Status string `json:"status"`
		} `json:"statements"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Version != 2 {
		t.Errorf("merged version = %d, want 2", doc.Version)
	}
	if len(doc.Statements) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(doc.Statements))
	}
	if doc.Statements[0].Vulnerability.Name != "CVE-2025-0001" || doc.Statements[0].Status != "fixed" {
		t.Errorf("base verdict not preserved: %+v", doc.Statements[0])
	}
}

func TestVEXTool_EmptyStatements(t *testing.T) {
	tool := buildVEXTool(vexDeps())
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"statements":[]}`)); err == nil {
		t.Error("expected error for empty statements")
	}
}

func TestVEXTool_BadInput(t *testing.T) {
	tool := buildVEXTool(vexDeps())
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
}
