package mcp

import (
	"encoding/json"
	"sort"
	"testing"
)

// These tests pin the spec that the tools/list response and ToolNames() are
// emitted in a deterministic (name-sorted) order.
//
// Why it matters: an MCP server's tools/list response is the cacheable prefix
// the client (e.g. Claude Code) sends on every turn. If the order changes
// between calls, the client's prompt/KV cache for the whole tools block is
// invalidated — a silent per-turn token-cost regression. It also violates
// yagura's own "Deterministic output" rule (CLAUDE.md).

// registerScrambled registers n tools with names in reverse-sorted order so a
// raw Go-map iteration (randomized) is overwhelmingly unlikely to come out
// sorted by luck.
func registerScrambled(s *Server) []string {
	names := []string{
		"yagura_zeta", "yagura_yota", "yagura_xray", "yagura_whisky",
		"yagura_victor", "yagura_uniform", "yagura_tango", "yagura_sierra",
		"yagura_romeo", "yagura_quebec",
	}
	for _, n := range names {
		s.Register(&Tool{Name: n, Description: "[S] " + n})
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return sorted
}

func toolsListNames(t *testing.T, s *Server) []string {
	t.Helper()
	rec := httpRecorder()
	s.handleToolsList(rec, []byte("1"))
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal tools/list: %v; body=%s", err, rec.Body.String())
	}
	out := make([]string, len(resp.Result.Tools))
	for i, tl := range resp.Result.Tools {
		out[i] = tl.Name
	}
	return out
}

func TestToolsList_DeterministicSortedOrder(t *testing.T) {
	s := New("", nil)
	want := registerScrambled(s)

	got := toolsListNames(t, s)
	if len(got) != len(want) {
		t.Fatalf("got %d tools, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tools/list not name-sorted at %d: got %v, want %v", i, got, want)
		}
	}
}

func TestToolsList_StableAcrossCalls(t *testing.T) {
	s := New("", nil)
	registerScrambled(s)

	first := toolsListNames(t, s)
	for i := 0; i < 20; i++ {
		next := toolsListNames(t, s)
		for j := range first {
			if next[j] != first[j] {
				t.Fatalf("tools/list order changed between calls (cache-busting): "+
					"call0=%v callN=%v", first, next)
			}
		}
	}
}

func TestToolNames_SortedAndStable(t *testing.T) {
	s := New("", nil)
	want := registerScrambled(s)

	for i := 0; i < 20; i++ {
		got := s.ToolNames()
		if len(got) != len(want) {
			t.Fatalf("ToolNames len %d, want %d", len(got), len(want))
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("ToolNames not sorted/stable: got %v, want %v", got, want)
			}
		}
	}
}
