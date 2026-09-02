package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/logging"
	"github.com/shizukutanaka/yagura/internal/registry"
)

func newServerForTest(t *testing.T, token string) (*Server, *registry.Registry) {
	t.Helper()
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(token, logging.Discard())
	RegisterDefaultTools(s, Deps{Registry: reg, Now: func() time.Time { return fixedNow }})
	return s, reg
}

func postJSON(t *testing.T, s http.Handler, body string, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func TestServer_GetIsNotAllowed(t *testing.T) {
	s, _ := newServerForTest(t, "")
	req := httptest.NewRequest("GET", "/mcp", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestServer_AuthRequired(t *testing.T) {
	s, _ := newServerForTest(t, "secret-token")
	w := postJSON(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestServer_AuthAccepted(t *testing.T) {
	s, _ := newServerForTest(t, "secret-token")
	w := postJSON(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "secret-token")
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestServer_NoAuthWhenTokenEmpty(t *testing.T) {
	s, _ := newServerForTest(t, "")
	w := postJSON(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestServer_Initialize(t *testing.T) {
	s, _ := newServerForTest(t, "")
	w := postJSON(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, "")
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["result"] == nil {
		t.Errorf("missing result: %s", w.Body.String())
	}
}

func TestServer_ToolsList(t *testing.T) {
	s, _ := newServerForTest(t, "")
	w := postJSON(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")
	body := w.Body.String()
	for _, name := range []string{
		"yagura_list", "yagura_get", "yagura_search", "yagura_today", "yagura_register",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("missing tool %s in: %s", name, body)
		}
	}
}

func TestServer_ToolCall_List(t *testing.T) {
	s, _ := newServerForTest(t, "")
	w := postJSON(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"yagura_list","arguments":{}}}`, "")
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != nil {
		t.Errorf("unexpected error: %v", resp["error"])
	}
	if resp["result"] == nil {
		t.Errorf("missing result: %s", w.Body.String())
	}
}

func TestServer_ToolCall_UnknownTool(t *testing.T) {
	s, _ := newServerForTest(t, "")
	w := postJSON(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"unknown_tool"}}`, "")
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestServer_UnknownMethod(t *testing.T) {
	s, _ := newServerForTest(t, "")
	w := postJSON(t, s, `{"jsonrpc":"2.0","id":1,"method":"nonsense"}`, "")
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == nil {
		t.Error("expected error for unknown method")
	}
}

func TestServer_BadJSON(t *testing.T) {
	s, _ := newServerForTest(t, "")
	w := postJSON(t, s, `{ not json`, "")
	if w.Code != http.StatusOK {
		// JSON-RPC parse errors still return 200 with error in body
		t.Logf("status: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "parse error") {
		t.Errorf("expected parse error response: %s", w.Body.String())
	}
}

func TestServer_ToolNames(t *testing.T) {
	s, _ := newServerForTest(t, "")
	names := s.ToolNames()
	// v0.28: hard-coded 数を avoid。最低数のみ保証(大量未登録の regression を捕捉)。
	// 正確な数の検証は cmd/yagura/integration_test.go の expectedTools list が担う。
	const minExpectedTools = 55
	if len(names) < minExpectedTools {
		t.Errorf("expected at least %d tools, got %d: %v", minExpectedTools, len(names), names)
	}
}

func TestServer_PanicRecovery(t *testing.T) {
	s, _ := newServerForTest(t, "")
	// Register a tool that always panics
	s.Register(&Tool{
		Name:        "panic_tool",
		Description: "always panics",
		InputSchema: map[string]any{"type": "object"},
		Handler: HandlerCtx(func(ctx context.Context, args json.RawMessage) (any, error) {
			panic("intentional test panic")
		}),
	})
	w := postJSON(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"panic_tool"}}`, "")
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == nil {
		t.Errorf("expected error after panic: %s", w.Body.String())
	}
}

// ─── v0.22.0: Compact mode for tools/list ─────────────────────

func TestHandleToolsList_CompactMode(t *testing.T) {
	srv := New("", nil)
	srv.Register(&Tool{
		Name:        "test_full",
		Description: "[G] Walk dependency graph from slug. Returns dependents.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":  map[string]any{"type": "string"},
				"depth": map[string]any{"type": "integer"},
			},
			"required": []string{"slug"},
		},
	})

	// non-compact mode (default)
	rec := httpRecorder()
	srv.handleToolsList(rec, []byte("1"))
	fullBody := rec.Body.String()

	// compact mode
	t.Setenv("YAGURA_MCP_COMPACT", "1")
	rec2 := httpRecorder()
	srv.handleToolsList(rec2, []byte("1"))
	compactBody := rec2.Body.String()

	if len(compactBody) >= len(fullBody) {
		t.Errorf("compact mode should produce smaller output: full=%d compact=%d",
			len(fullBody), len(compactBody))
	}

	// compactDescription should yield "[G]" only
	if !strings.Contains(compactBody, `"description":"[G]"`) {
		t.Errorf("compact mode should reduce description to [G]; body=%s", compactBody)
	}
	// schema should not have item-level description but should keep required
	if !strings.Contains(compactBody, `"required":["slug"]`) {
		t.Errorf("compact schema should retain required; body=%s", compactBody)
	}
}

func TestCompactDescription(t *testing.T) {
	cases := map[string]string{
		"[G] long description here": "[G]",
		"[S] another long one":      "[S]",
		"no prefix":                 "",
		"":                          "",
		"[G]":                       "[G]",
	}
	for in, want := range cases {
		got := compactDescription(in)
		if got != want {
			t.Errorf("compactDescription(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestCompactSchema_KeepsRequiredAndType(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"slug": map[string]any{
				"type":        "string",
				"description": "should be dropped",
				"enum":        []string{"a", "b"},
			},
		},
		"required": []string{"slug"},
	}
	out := compactSchema(in)
	if out["type"] != "object" {
		t.Error("compact schema should keep type")
	}
	req, _ := out["required"].([]string)
	if len(req) != 1 || req[0] != "slug" {
		t.Errorf("compact schema should keep required; got %v", out["required"])
	}
	props, _ := out["properties"].(map[string]any)
	if _, ok := props["slug"]; !ok {
		t.Error("compact schema should keep property keys")
	}
	// description should be dropped
	if slug, _ := props["slug"].(map[string]any); slug["description"] != nil {
		t.Error("compact schema should drop description")
	}
}

func httpRecorder() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}
