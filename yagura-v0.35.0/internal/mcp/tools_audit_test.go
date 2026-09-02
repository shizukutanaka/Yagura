package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestContentAuditTools_Basic(t *testing.T) {
	d := newDeps(t)
	cases := []struct {
		tool    *Tool
		content string
		wantKey string // a json key expected in the result
	}{
		{buildWorkflowAuditTool(d), `const r = await agent("x", {model:"opus", maxTokens:2000})`, "score"},
		{buildSettingsAuditTool(d), `{"permissions":{"deny":["Bash(rm -rf *)"]},"hooks":{"Stop":[]}}`, "score"},
		{buildAgentConfigAuditTool(d), `{"agents":{"defaults":{"model":{"primary":"p/m"}}},"models":{"providers":{"p":{"apiKey":"EMPTY","models":[{"id":"m","contextWindow":1000,"maxTokens":100}]}}}}`, "score"},
		{buildPluginAuditTool(d), `{"name":"yagura","version":"1.0.0"}`, "score"},
		{buildPublicityScanTool(d), "see /Users/hiroro/x", "summary"},
	}
	for _, c := range cases {
		res := mustCall(t, c.tool, map[string]any{"content": c.content})
		b, _ := json.Marshal(res)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: json %v", c.tool.Name, err)
		}
		if _, ok := m[c.wantKey]; !ok {
			t.Errorf("%s: expected key %q in result, got %s", c.tool.Name, c.wantKey, b)
		}
	}
}

func TestContentAuditTools_EmptyContentRejected(t *testing.T) {
	d := newDeps(t)
	for _, tool := range []*Tool{
		buildWorkflowAuditTool(d), buildSettingsAuditTool(d), buildAgentConfigAuditTool(d),
		buildPluginAuditTool(d), buildPublicityScanTool(d),
	} {
		b, _ := json.Marshal(map[string]any{"content": ""})
		if _, err := tool.Handler(tCtx(), b); err == nil {
			t.Errorf("%s: expected error on empty content", tool.Name)
		}
	}
}

func tCtx() context.Context { return context.Background() }
