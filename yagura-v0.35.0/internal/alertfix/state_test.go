package alertfix

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixedNowState() time.Time {
	return time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
}

func newStoreT(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.jsonl")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.NowFn = fixedNowState
	return s, path
}

// ─── basic CRUD ──────────────────────────────────

func TestStore_Resolve(t *testing.T) {
	s, _ := newStoreT(t)
	if err := s.Resolve("breeze:vulns:critical", "upgraded openssl"); err != nil {
		t.Fatal(err)
	}
	st, ok := s.Get("breeze:vulns:critical")
	if !ok {
		t.Fatal("expected entry")
	}
	if st.Status != StatusResolved {
		t.Errorf("status: %s", st.Status)
	}
	if st.Note != "upgraded openssl" {
		t.Errorf("note: %q", st.Note)
	}
}

func TestStore_Snooze(t *testing.T) {
	s, _ := newStoreT(t)
	until := fixedNowState().Add(7 * 24 * time.Hour)
	if err := s.Snooze("breeze:plan", until, "fix next sprint"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Get("breeze:plan")
	if st.Status != StatusSnoozed {
		t.Errorf("status: %s", st.Status)
	}
	if st.SnoozeUntil == nil || !st.SnoozeUntil.Equal(until) {
		t.Errorf("snooze_until: %v", st.SnoozeUntil)
	}
}

func TestStore_SnoozePastFails(t *testing.T) {
	s, _ := newStoreT(t)
	past := fixedNowState().Add(-1 * time.Hour)
	err := s.Snooze("x", past, "")
	if err == nil {
		t.Error("expected error for past snooze")
	}
}

func TestStore_SnoozeExpiredAutoActive(t *testing.T) {
	s, _ := newStoreT(t)
	until := fixedNowState().Add(1 * time.Hour)
	_ = s.Snooze("x", until, "")
	// 期限後に Now を進める
	s.NowFn = func() time.Time { return fixedNowState().Add(2 * time.Hour) }
	st, _ := s.Get("x")
	if st.Status != StatusActive {
		t.Errorf("expected lazy revival to active, got %s", st.Status)
	}
}

func TestStore_Reopen(t *testing.T) {
	s, _ := newStoreT(t)
	_ = s.Resolve("x", "")
	_ = s.Reopen("x", "still happening")
	st, _ := s.Get("x")
	if st.Status != StatusActive {
		t.Errorf("reopen: %s", st.Status)
	}
}

func TestStore_GetUnknown(t *testing.T) {
	s, _ := newStoreT(t)
	if _, ok := s.Get("nope"); ok {
		t.Error("expected false")
	}
}

// ─── persistence ─────────────────────────────────

func TestStore_PersistsAcrossReopen(t *testing.T) {
	s, path := newStoreT(t)
	_ = s.Resolve("a", "done")
	until := fixedNowState().Add(time.Hour)
	_ = s.Snooze("b", until, "later")

	// 再 open
	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s2.NowFn = fixedNowState
	st, _ := s2.Get("a")
	if st.Status != StatusResolved {
		t.Errorf("replay a: %s", st.Status)
	}
	st, _ = s2.Get("b")
	if st.Status != StatusSnoozed {
		t.Errorf("replay b: %s", st.Status)
	}
}

func TestStore_CorruptLineSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.jsonl")
	// 1 行目: 壊れた JSON、2 行目: 正常
	good := `{"alert_id":"x","action":"resolve","status":"resolved","timestamp":"2026-05-13T12:00:00Z"}`
	content := "{not_json\n" + good + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.NowFn = fixedNowState
	if _, ok := s.Get("x"); !ok {
		t.Error("good entry should be preserved despite corrupt line")
	}
}

func TestStore_EmptyMode(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve("x", ""); err != nil {
		t.Fatal(err)
	}
	st, ok := s.Get("x")
	if !ok || st.Status != StatusResolved {
		t.Errorf("in-memory mode: %v %+v", ok, st)
	}
}

func TestStore_MissingFileNotError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.jsonl")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.curr) != 0 {
		t.Errorf("expected empty state, got %d", len(s.curr))
	}
}

func TestStore_LatestEntryWins(t *testing.T) {
	s, _ := newStoreT(t)
	_ = s.Resolve("x", "first")
	_ = s.Reopen("x", "actually still broken")
	st, _ := s.Get("x")
	if st.Status != StatusActive {
		t.Errorf("latest should win: %s", st.Status)
	}
}

// ─── FilterAlerts ────────────────────────────────

func TestStore_FilterAlerts_RemovesResolved(t *testing.T) {
	s, _ := newStoreT(t)
	_ = s.Resolve("a:vulns:critical", "")
	in := []Alert{
		{ID: "a:vulns:critical", Project: "a", Severity: SevCritical, Source: SourceVuln},
		{ID: "b:vulns:critical", Project: "b", Severity: SevCritical, Source: SourceVuln},
	}
	out := s.FilterAlerts(in)
	if len(out) != 1 || out[0].Project != "b" {
		t.Errorf("filter: %v", out)
	}
}

func TestStore_FilterAlerts_RemovesSnoozed(t *testing.T) {
	s, _ := newStoreT(t)
	until := fixedNowState().Add(24 * time.Hour)
	_ = s.Snooze("a:plan", until, "")
	in := []Alert{
		{ID: "a:plan", Severity: SevMedium, Source: SourcePlan},
		{ID: "b:plan", Severity: SevMedium, Source: SourcePlan},
	}
	out := s.FilterAlerts(in)
	if len(out) != 1 || out[0].ID != "b:plan" {
		t.Errorf("filter snoozed: %v", out)
	}
}

func TestStore_FilterAlerts_SnoozeExpiredIncluded(t *testing.T) {
	s, _ := newStoreT(t)
	until := fixedNowState().Add(time.Hour)
	_ = s.Snooze("x", until, "")
	s.NowFn = func() time.Time { return fixedNowState().Add(2 * time.Hour) }
	in := []Alert{{ID: "x", Severity: SevLow, Source: SourceStale}}
	out := s.FilterAlerts(in)
	if len(out) != 1 {
		t.Errorf("expired snooze should re-include alert: %v", out)
	}
}

// ─── Stats ──────────────────────────────────────

func TestStore_Stats(t *testing.T) {
	s, _ := newStoreT(t)
	_ = s.Resolve("a", "")
	_ = s.Resolve("b", "")
	_ = s.Snooze("c", fixedNowState().Add(24*time.Hour), "")
	_ = s.Reopen("d", "")
	st := s.Stats()
	if st[StatusResolved] != 2 {
		t.Errorf("resolved: %d", st[StatusResolved])
	}
	if st[StatusSnoozed] != 1 {
		t.Errorf("snoozed: %d", st[StatusSnoozed])
	}
	if st[StatusActive] != 1 {
		t.Errorf("active: %d", st[StatusActive])
	}
}

// ─── Snapshot ───────────────────────────────────

func TestStore_Snapshot(t *testing.T) {
	s, _ := newStoreT(t)
	_ = s.Resolve("a", "x")
	_ = s.Resolve("b", "y")
	snap := s.Snapshot()
	if len(snap) != 2 {
		t.Errorf("snapshot len: %d", len(snap))
	}
}

// ─── FilterReport (v0.35) ────────────────────────

func TestStore_FilterReport_ExcludesResolvedAndRecomputes(t *testing.T) {
	s, _ := newStoreT(t)
	report := Report{
		Alerts: []Alert{
			{ID: "a1", Project: "p1", Source: SourceVuln, Severity: SevCritical},
			{ID: "a2", Project: "p1", Source: SourceVuln, Severity: SevHigh},
			{ID: "a3", Project: "p2", Source: SourcePlan, Severity: SevLow},
		},
		Total:       3,
		HasCritical: true,
	}
	if err := s.Resolve("a1", "done"); err != nil { // remove the only critical
		t.Fatal(err)
	}
	got := s.FilterReport(report)
	if got.Total != 2 {
		t.Errorf("total = %d, want 2 after resolving a1", got.Total)
	}
	if got.HasCritical {
		t.Error("has_critical should be recomputed to false")
	}
	if got.BySeverity[SevCritical] != 0 || got.BySeverity[SevHigh] != 1 || got.BySeverity[SevLow] != 1 {
		t.Errorf("by_severity recompute mismatch: %+v", got.BySeverity)
	}
	if got.ByProject["p1"] != 1 || got.ByProject["p2"] != 1 {
		t.Errorf("by_project recompute mismatch: %+v", got.ByProject)
	}
}

// TestStore_FilterReport_HasCritical covers the HasCritical=true path in FilterReport.
func TestStore_FilterReport_HasCritical(t *testing.T) {
	s, _ := newStoreT(t)
	report := Report{
		Alerts: []Alert{
			{ID: "crit1", Project: "p", Source: SourceVuln, Severity: SevCritical},
		},
		Total: 1,
	}
	got := s.FilterReport(report)
	if !got.HasCritical {
		t.Error("HasCritical should be true when a critical alert passes through")
	}
}

// TestStore_Stats_ExpiredSnoozeCountsAsActive covers the snooze-expired→active path in Stats.
func TestStore_Stats_ExpiredSnoozeCountsAsActive(t *testing.T) {
	s, _ := newStoreT(t)
	// Snooze for 1 hour from now, then advance the clock past it.
	until := fixedNowState().Add(time.Hour)
	if err := s.Snooze("x", until, ""); err != nil {
		t.Fatal(err)
	}
	// Advance NowFn to 2 hours in the future (past the snooze deadline).
	s.NowFn = func() time.Time { return fixedNowState().Add(2 * time.Hour) }
	st := s.Stats()
	// Expired snooze must be counted as active (not snoozed).
	if st[StatusSnoozed] != 0 {
		t.Errorf("expired snooze should not count as snoozed, got snoozed=%d", st[StatusSnoozed])
	}
	if st[StatusActive] != 1 {
		t.Errorf("expired snooze should count as active, got active=%d", st[StatusActive])
	}
}

// TestStore_Replay_BlankAndEmptyAlertID covers the blank-line and empty alertID
// continue branches in replay().
func TestStore_Replay_BlankAndEmptyAlertID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.jsonl")

	// Write lines: blank, valid, one with alertID="", blank at end.
	lines := ""
	lines += "\n"                                                    // blank → continue
	lines += `{"alert_id":"real","status":"active"}` + "\n"          // valid entry
	lines += fmt.Sprintf(`{"alert_id":"","status":"active"}`) + "\n" // alertID=="" → continue
	lines += "  \n"                                                  // whitespace-only → continue

	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Only the "real" entry should be in the store; blank + empty-alertID ones skipped.
	_, ok := s.Get("real")
	if !ok {
		t.Error("expected entry 'real' to be loaded from replay")
	}
}
