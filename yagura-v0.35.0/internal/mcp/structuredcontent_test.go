package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// ─── structuredContent on tools/call responses (v0.111.0) ─────────

// callResult drives a tools/call through the HTTP handler and returns the
// unmarshalled JSON-RPC `result` object.
func callResult(t *testing.T, s http.Handler, name string) map[string]any {
	t.Helper()
	w := postJSON(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":{}}}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call %s: code=%d body=%s", name, w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["error"] != nil {
		t.Fatalf("unexpected error for %s: %v", name, resp["error"])
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %s", w.Body.String())
	}
	return res
}

// TestStructuredContent_ObjectResultCarriesBothEnvelopes verifies an
// object-returning tool response keeps the back-compat content text block
// AND adds a structuredContent object equal to the parsed text (MCP
// 2025-06-18).
func TestStructuredContent_ObjectResultCarriesBothEnvelopes(t *testing.T) {
	s, _ := newServerForTest(t, "")
	res := callResult(t, s, "yagura_list") // returns {count, projects}

	// back-compat: content:[{type:text, text:<json>}] unchanged
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("content block missing/empty: %v", res)
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Errorf("content[0].type: want text, got %v", block["type"])
	}
	textStr, _ := block["text"].(string)

	// new: structuredContent present, an object, equal to the parsed text
	sc, ok := res["structuredContent"]
	if !ok {
		t.Fatalf("structuredContent missing for an object-returning tool: %v", res)
	}
	if _, isObj := sc.(map[string]any); !isObj {
		t.Fatalf("structuredContent must be a JSON object, got %T", sc)
	}
	var fromText any
	if err := json.Unmarshal([]byte(textStr), &fromText); err != nil {
		t.Fatalf("content text not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(sc, fromText) {
		t.Errorf("structuredContent != parsed content text:\n sc=%v\n txt=%v", sc, fromText)
	}
}

// TestStructuredContent_ArrayResultOmitsIt verifies a tool whose handler
// returns a non-object (array) does NOT get a structuredContent field
// (the spec requires it to be an object), while the content text block is
// still emitted.
func TestStructuredContent_ArrayResultOmitsIt(t *testing.T) {
	s, _ := newServerForTest(t, "")
	s.Register(&Tool{
		Name:        "test_array_tool",
		Description: "returns a bare array",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return []int{1, 2, 3}, nil
		},
	})
	res := callResult(t, s, "test_array_tool")

	if _, ok := res["content"]; !ok {
		t.Errorf("content block should still be present for array results")
	}
	if _, ok := res["structuredContent"]; ok {
		t.Errorf("structuredContent must be absent for a non-object (array) result: %v", res["structuredContent"])
	}
}

// TestStructuredContent_ScalarResultOmitsIt is the same guard for a bare
// scalar handler return.
func TestStructuredContent_ScalarResultOmitsIt(t *testing.T) {
	s, _ := newServerForTest(t, "")
	s.Register(&Tool{
		Name:        "test_scalar_tool",
		Description: "returns a bare string",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return "just a string", nil
		},
	})
	res := callResult(t, s, "test_scalar_tool")
	if _, ok := res["structuredContent"]; ok {
		t.Errorf("structuredContent must be absent for a scalar result: %v", res["structuredContent"])
	}
}

// ─── initialize handshake honesty (v0.111.0) ──────────────────────

// TestInitialize_ReportsRealVersionAndCurrentProtocol proves the handshake
// no longer hardcodes "0.1.0" / "2024-11-05": it echoes the injected
// version and advertises the 2025-06-18 protocol whose structuredContent
// feature we implement.
func TestInitialize_ReportsRealVersionAndCurrentProtocol(t *testing.T) {
	orig := serverVersion
	SetVersion("9.9.9-test")
	defer SetVersion(orig)

	s, _ := newServerForTest(t, "")
	w := postJSON(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, "")
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	res := resp["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion: want 2025-06-18, got %v", res["protocolVersion"])
	}
	si, _ := res["serverInfo"].(map[string]any)
	if si["version"] != "9.9.9-test" {
		t.Errorf("serverInfo.version: want injected 9.9.9-test (not hardcoded 0.1.0), got %v", si["version"])
	}
	if si["name"] != "yagura" {
		t.Errorf("serverInfo.name: want yagura, got %v", si["name"])
	}
}
