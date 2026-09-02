package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAgentEventTool(t *testing.T) {
	tool := buildAgentEventTool(Deps{})
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"event":{"hook_event_name":"PreToolUse","tool_name":"Bash","session_id":"s1","cwd":"/x"}}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	b, _ := json.Marshal(out)
	var r struct {
		SourceFormat string `json:"source_format"`
		Normalized   struct {
			Operation string `json:"operation"`
			Phase     string `json:"phase"`
			Tool      string `json:"tool"`
			Agent     string `json:"agent"`
		} `json:"normalized"`
		OTel map[string]any `json:"otel"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.SourceFormat != "claude_code" || r.Normalized.Operation != "execute_tool" ||
		r.Normalized.Phase != "start" || r.Normalized.Tool != "Bash" || r.Normalized.Agent != "claude_code" {
		t.Errorf("unexpected normalization: %s", b)
	}
	if r.OTel["gen_ai.operation.name"] != "execute_tool" || r.OTel["gen_ai.tool.name"] != "Bash" {
		t.Errorf("otel bag: %+v", r.OTel)
	}
}

func TestAgentEventTool_OTelInput(t *testing.T) {
	tool := buildAgentEventTool(Deps{})
	out, _ := tool.Handler(context.Background(), json.RawMessage(`{"event":{"gen_ai.operation.name":"invoke_agent","gen_ai.agent.name":"gemini"}}`))
	b, _ := json.Marshal(out)
	if !json.Valid(b) {
		t.Fatal("invalid output")
	}
	var r struct {
		SourceFormat string `json:"source_format"`
	}
	_ = json.Unmarshal(b, &r)
	if r.SourceFormat != "otel" {
		t.Errorf("expected otel source, got %q", r.SourceFormat)
	}
}

func TestAgentEventTool_EmptyEvent(t *testing.T) {
	tool := buildAgentEventTool(Deps{})
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"event":{}}`)); err == nil {
		t.Error("expected error for empty event")
	}
}

func TestAgentEventTool_BadInput(t *testing.T) {
	tool := buildAgentEventTool(Deps{})
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
}
