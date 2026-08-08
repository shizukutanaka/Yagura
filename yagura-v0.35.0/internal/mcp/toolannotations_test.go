package mcp

import (
	"encoding/json"
	"net/http"
	"testing"
)

// ─── ToolAnnotations + Title on tools/list (v0.113.0) ─────────────

// toolsListEntries drives tools/list through the HTTP handler and returns the
// per-tool entry objects keyed by name.
func toolsListEntries(t *testing.T, s http.Handler) map[string]map[string]any {
	t.Helper()
	w := postJSON(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list: code=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	byName := make(map[string]map[string]any, len(resp.Result.Tools))
	for _, e := range resp.Result.Tools {
		name, _ := e["name"].(string)
		byName[name] = e
	}
	return byName
}

func annotationsOf(t *testing.T, entry map[string]any, tool string) map[string]any {
	t.Helper()
	a, ok := entry["annotations"].(map[string]any)
	if !ok {
		t.Fatalf("%s: annotations missing or not an object: %v", tool, entry["annotations"])
	}
	return a
}

// TestToolsList_AnnotationsForKnownTools pins the four behavioral-hint
// booleans for a representative spread of tools, covering every non-default
// classification the verified table produced.
func TestToolsList_AnnotationsForKnownTools(t *testing.T) {
	s, _ := newServerForTest(t, "")
	entries := toolsListEntries(t, s)

	cases := []struct {
		tool                                   string
		readOnly, destructive, idem, openWorld bool
	}{
		// pure read-only sensor
		{"yagura_list", true, false, true, false},
		// destructive registry mutation (verify-pass corrected)
		{"yagura_unregister", false, true, true, false},
		{"yagura_update", false, true, true, false},
		// additive, non-destructive write
		{"yagura_register", false, false, true, false},
		// external-API read-only (open world)
		{"yagura_vulns", true, false, true, true},
		{"yagura_scorecard", true, false, true, true},
		{"yagura_pin_drift", true, false, true, true},
		// conditional audit-log append: not read-only, not idempotent
		{"yagura_self_improve", false, false, false, false},
		// external subprocess spawn + unconditional overwrite
		{"yagura_handoff", false, true, false, true},
	}
	for _, c := range cases {
		entry, ok := entries[c.tool]
		if !ok {
			t.Errorf("%s: not present in tools/list", c.tool)
			continue
		}
		a := annotationsOf(t, entry, c.tool)
		if got := a["readOnlyHint"]; got != c.readOnly {
			t.Errorf("%s readOnlyHint = %v, want %v", c.tool, got, c.readOnly)
		}
		if got := a["destructiveHint"]; got != c.destructive {
			t.Errorf("%s destructiveHint = %v, want %v", c.tool, got, c.destructive)
		}
		if got := a["idempotentHint"]; got != c.idem {
			t.Errorf("%s idempotentHint = %v, want %v", c.tool, got, c.idem)
		}
		if got := a["openWorldHint"]; got != c.openWorld {
			t.Errorf("%s openWorldHint = %v, want %v", c.tool, got, c.openWorld)
		}
	}
}

// TestToolsList_TitleForKnownTool proves the BaseMetadata title reaches the wire.
func TestToolsList_TitleForKnownTool(t *testing.T) {
	s, _ := newServerForTest(t, "")
	entries := toolsListEntries(t, s)
	if got := entries["yagura_list"]["title"]; got != "List Projects" {
		t.Errorf("yagura_list title = %v, want \"List Projects\"", got)
	}
}

// TestToolsList_CompactModeStillIncludesAnnotationsAndTitle proves compact
// mode strips only the verbose description/schema, never the structured hints.
func TestToolsList_CompactModeStillIncludesAnnotationsAndTitle(t *testing.T) {
	t.Setenv("YAGURA_MCP_COMPACT", "1")
	s, _ := newServerForTest(t, "")
	entries := toolsListEntries(t, s)
	entry, ok := entries["yagura_list"]
	if !ok {
		t.Fatal("yagura_list missing in compact tools/list")
	}
	if entry["title"] != "List Projects" {
		t.Errorf("compact: title = %v, want \"List Projects\"", entry["title"])
	}
	a := annotationsOf(t, entry, "yagura_list")
	if a["readOnlyHint"] != true {
		t.Errorf("compact: yagura_list readOnlyHint = %v, want true", a["readOnlyHint"])
	}
}

// TestAllRegisteredTools_HaveAnnotationsAndTitle is a completeness guard: every
// default-registered tool must declare both a non-empty Title and non-nil
// Annotations, so a future new tool cannot silently ship without them.
func TestAllRegisteredTools_HaveAnnotationsAndTitle(t *testing.T) {
	s, _ := newServerForTest(t, "")
	s.mu.RLock()
	defer s.mu.RUnlock()
	for name, tool := range s.tools {
		if tool.Title == "" {
			t.Errorf("%s: Title is empty (every registered tool must declare one)", name)
		}
		if tool.Annotations == nil {
			t.Errorf("%s: Annotations is nil (every registered tool must declare them)", name)
		}
	}
}
