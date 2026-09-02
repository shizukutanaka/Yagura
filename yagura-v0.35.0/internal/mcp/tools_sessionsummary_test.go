package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/hookreceiver"
)

// fixedLookup resolves every cwd to one slug (test helper).
type fixedLookup struct{ slug string }

func (f fixedLookup) ResolveByPath(string) (string, bool) { return f.slug, true }

func TestRecordedSummary(t *testing.T) {
	hr, err := hookreceiver.NewReceiver(filepath.Join(t.TempDir(), "hooks.jsonl"), fixedLookup{slug: "breeze"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/x"}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","cwd":"/x"}`,
		`{"agent":"gemini_cli","event":"beforeToolCall","tool":"Read","cwd":"/x"}`,
	} {
		hr.Handle(httptest.NewRecorder(), httptest.NewRequest("POST", "/hooks/agent", strings.NewReader(payload)))
	}

	srv := New("", nil)
	srv.SetHookReceiver(hr)

	sum, ok := srv.RecordedSummary("breeze", "", 0)
	if !ok {
		t.Fatal("expected ok for a project with recorded events")
	}
	if sum.ByTool["Bash"] != 1 || sum.ByTool["Read"] != 1 {
		t.Errorf("by_tool = %+v", sum.ByTool)
	}
	if sum.ToolInvocations != 2 {
		t.Errorf("tool_invocations = %d, want 2", sum.ToolInvocations)
	}

	// unknown project → ok=false, empty summary
	if _, ok := srv.RecordedSummary("ghost", "", 0); ok {
		t.Error("expected ok=false for a project with no recorded events")
	}
}

func TestRecordedSummary_NoReceiver(t *testing.T) {
	srv := New("", nil)
	if _, ok := srv.RecordedSummary("breeze", "", 0); ok {
		t.Error("expected ok=false when no hook receiver is configured")
	}
}

func TestSessionSummaryTool(t *testing.T) {
	tool := buildSessionSummaryTool(Deps{}, nil)
	// raw events from mixed agents (Claude Code hook + generic), normalized then summarized
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"events":[
		{"hook_event_name":"PreToolUse","tool_name":"Bash","session_id":"s"},
		{"hook_event_name":"PostToolUse","tool_name":"Bash","session_id":"s"},
		{"agent":"gemini_cli","event":"beforeToolCall","tool":"Read"},
		{"agent":"gemini_cli","event":"toolError","tool":"Read","error":{"type":"timeout"}}
	]}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	b, _ := json.Marshal(out)
	var r struct {
		Events          int            `json:"events"`
		ToolInvocations int            `json:"tool_invocations"`
		ByTool          map[string]int `json:"by_tool"`
		Errors          []struct {
			Tool string `json:"tool"`
		} `json:"errors"`
		Agents []string `json:"agents"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Events != 4 || r.ToolInvocations != 2 {
		t.Errorf("events/invocations: %d/%d (%s)", r.Events, r.ToolInvocations, b)
	}
	if r.ByTool["Bash"] != 1 || r.ByTool["Read"] != 1 {
		t.Errorf("by_tool: %+v", r.ByTool)
	}
	if len(r.Errors) != 1 || r.Errors[0].Tool != "Read" {
		t.Errorf("errors: %+v", r.Errors)
	}
	if len(r.Agents) != 2 {
		t.Errorf("expected 2 agents, got %+v", r.Agents)
	}
}

func TestSessionSummaryTool_EmptyEvents(t *testing.T) {
	tool := buildSessionSummaryTool(Deps{}, nil)
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"events":[]}`)); err == nil {
		t.Error("expected error for empty events")
	}
}

func TestSessionSummaryTool_BadInput(t *testing.T) {
	tool := buildSessionSummaryTool(Deps{}, nil)
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestRecordedToEvents(t *testing.T) {
	// Timeline returns newest-first; recordedToEvents must restore chronological order,
	// map fields via agentevent.Normalize, and honor the session filter.
	recorded := []hookreceiver.Event{
		// newest first:
		{HookEventName: "PostToolUseFailure", ToolName: "Bash", SessionID: "s1", IsError: true},
		{HookEventName: "PreToolUse", ToolName: "Bash", SessionID: "s1"},
		{HookEventName: "PreToolUse", ToolName: "Read", SessionID: "other"},
	}
	got := recordedToEvents(recorded, "s1")
	if len(got) != 2 {
		t.Fatalf("session filter failed: got %d events", len(got))
	}
	// chronological: PreToolUse(start) before PostToolUseFailure(error)
	if got[0].Operation != "execute_tool" || got[0].Phase != "start" {
		t.Errorf("first should be execute_tool/start, got %+v", got[0])
	}
	if got[1].Phase != "error" {
		t.Errorf("second should be error phase, got %+v", got[1])
	}
	// no session filter → all 3
	if len(recordedToEvents(recorded, "")) != 3 {
		t.Error("no-filter should include all events")
	}
}
