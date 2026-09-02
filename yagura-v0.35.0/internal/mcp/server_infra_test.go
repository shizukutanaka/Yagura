// server_infra_test.go — tests for Server infrastructure methods and low-level helpers
// that have 0% or <70% coverage: AllToolStats, SetAudit/emitAudit, SetAlertStore/AlertStore,
// CacheStats nil branch, asJSONError code paths, secretScanSeverityRank default path.
package mcp

import (
	"errors"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/audit"
	"github.com/shizukutanaka/yagura/internal/logging"
	"github.com/shizukutanaka/yagura/internal/secretscan"
)

// errSink returns an error on every Append call (exercises emitAudit warning path).
type errSink struct{ err error }

func (f errSink) Append(_ audit.Record) error { return f.err }

// captureSink collects appended records.
type captureSink struct{ records []audit.Record }

func (c *captureSink) Append(r audit.Record) error {
	c.records = append(c.records, r)
	return nil
}

// ─── AllToolStats ────────────────────────────────────────────────

func TestAllToolStats_EmptyServer_ReturnsEmpty(t *testing.T) {
	s := New("", logging.Discard())
	stats := s.AllToolStats()
	if len(stats) != 0 {
		t.Errorf("fresh server: expected 0 stats, got %d", len(stats))
	}
}

func TestAllToolStats_AfterToolCall_RecordsStats(t *testing.T) {
	s, _ := newServerForTest(t, "")
	// Call yagura_list to accumulate stats for that tool.
	w := postJSON(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"yagura_list","arguments":{}}}`, "")
	if w.Code != 200 {
		t.Fatalf("tool call failed: %d %s", w.Code, w.Body.String())
	}

	stats := s.AllToolStats()
	var got *ToolStats
	for i := range stats {
		if stats[i].Name == "yagura_list" {
			got = &stats[i]
			break
		}
	}
	if got == nil {
		t.Fatal("yagura_list not found in AllToolStats")
	}
	if got.Calls == 0 {
		t.Error("Calls should be ≥1 after one HTTP tool call")
	}
	if got.ResponseBytes == 0 {
		t.Error("ResponseBytes should be >0 after tool call")
	}
}

// ─── SetAudit / emitAudit ────────────────────────────────────────

func TestSetAudit_NilSink_EmitIsNoOp(t *testing.T) {
	s := New("", logging.Discard())
	// Default: no sink — emitAudit must not panic.
	s.emitAudit(audit.Record{Kind: "test_noop"})
}

func TestSetAudit_WorkingSink_RecordsEvent(t *testing.T) {
	s := New("", logging.Discard())
	sink := &captureSink{}
	s.SetAudit(sink)
	s.emitAudit(audit.Record{Kind: "test_event", Actor: "cli"})
	if len(sink.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(sink.records))
	}
	if sink.records[0].Kind != "test_event" {
		t.Errorf("record Kind = %q, want test_event", sink.records[0].Kind)
	}
}

func TestSetAudit_FailingSink_DoesNotPanic(t *testing.T) {
	// Exercises the "audit append failed" warning branch without crashing.
	s := New("", logging.Discard())
	s.SetAudit(errSink{err: errors.New("disk full")})
	s.emitAudit(audit.Record{Kind: "test_fail"})
}

func TestSetAudit_NilClears(t *testing.T) {
	s := New("", logging.Discard())
	sink := &captureSink{}
	s.SetAudit(sink)
	s.SetAudit(nil) // clear the sink
	s.emitAudit(audit.Record{Kind: "should_not_appear"})
	if len(sink.records) != 0 {
		t.Errorf("expected 0 records after SetAudit(nil), got %d", len(sink.records))
	}
}

// ─── SetAlertStore / AlertStore ──────────────────────────────────

func TestSetAlertStore_RoundTrip(t *testing.T) {
	s := New("", logging.Discard())
	if s.AlertStore() != nil {
		t.Error("expected nil AlertStore on fresh server")
	}

	st, err := alertfix.NewStore(t.TempDir() + "/alerts.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	s.SetAlertStore(st)
	if s.AlertStore() != st {
		t.Error("AlertStore() did not return the value passed to SetAlertStore()")
	}

	s.SetAlertStore(nil)
	if s.AlertStore() != nil {
		t.Error("expected nil after SetAlertStore(nil)")
	}
}

// ─── CacheStats nil branch ───────────────────────────────────────

func TestCacheStats_NilCache_ReturnsZeroStats(t *testing.T) {
	s := New("", logging.Discard())
	s.cache = nil // force nil cache (same package access)
	stats := s.CacheStats()
	// Zero-value Stats must be returned without panic.
	if stats.Hits != 0 || stats.Misses != 0 {
		t.Errorf("expected zero stats for nil cache, got %+v", stats)
	}
}

// ─── asJSONError ─────────────────────────────────────────────────

func TestAsJSONError_NonToolError_MapsToInternal(t *testing.T) {
	// A plain error (not ToolError) must be wrapped as "internal" → -32603.
	b := asJSONError(errors.New("something unexpected"))
	if !strings.Contains(string(b), "-32603") {
		t.Errorf("expected -32603 for non-ToolError, got: %s", b)
	}
}

func TestAsJSONError_InvalidInput_Maps32602(t *testing.T) {
	b := asJSONError(&ToolError{Code: "invalid_input", Message: "bad param"})
	if !strings.Contains(string(b), "-32602") {
		t.Errorf("expected -32602 for invalid_input, got: %s", b)
	}
}

func TestAsJSONError_Internal_Maps32603(t *testing.T) {
	b := asJSONError(&ToolError{Code: "internal", Message: "oops"})
	if !strings.Contains(string(b), "-32603") {
		t.Errorf("expected -32603 for internal, got: %s", b)
	}
}

func TestAsJSONError_NotFound_Maps32001(t *testing.T) {
	b := asJSONError(&ToolError{Code: "not_found", Message: "gone"})
	if !strings.Contains(string(b), "-32001") {
		t.Errorf("expected -32001 for not_found, got: %s", b)
	}
}

func TestAsJSONError_OtherCode_Maps32000(t *testing.T) {
	// Any code not in the switch falls through to -32000 (application-specific).
	b := asJSONError(&ToolError{Code: "unavailable", Message: "feature off"})
	if !strings.Contains(string(b), "-32000") {
		t.Errorf("expected -32000 for other code, got: %s", b)
	}
}

// ─── secretScanSeverityRank ──────────────────────────────────────

func TestSecretScanSeverityRank_KnownValues(t *testing.T) {
	cases := []struct {
		sev  secretscan.Severity
		want int
	}{
		{secretscan.SeverityCritical, 4},
		{secretscan.SeverityHigh, 3},
		{secretscan.SeverityMedium, 2},
		{secretscan.SeverityLow, 1},
	}
	for _, c := range cases {
		if got := secretScanSeverityRank(c.sev); got != c.want {
			t.Errorf("rank(%s) = %d, want %d", c.sev, got, c.want)
		}
	}
}

func TestSecretScanSeverityRank_UnknownReturnsZero(t *testing.T) {
	if got := secretScanSeverityRank(secretscan.Severity("UNKNOWN")); got != 0 {
		t.Errorf("unknown severity: expected 0, got %d", got)
	}
}
