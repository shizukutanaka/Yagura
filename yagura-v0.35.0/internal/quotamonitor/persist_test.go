package quotamonitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── Persistence (v0.17.0) ──────────────────────────────────

func TestSetPersistPath_ThenReportAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	m := New()
	m.SetPersistPath(path)

	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return base }
	if err := m.Report(AgentClaudeCode, 80, "auto", time.Time{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// goroutine の persist 完了を待つ(短い sleep で実用的)
	time.Sleep(50 * time.Millisecond)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) == 0 {
		t.Error("file should have at least 1 line")
	}
}

func TestLoadHistory_RebuildsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")

	// "before restart": 3 reports
	m1 := New()
	m1.SetPersistPath(path)
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	for i, p := range []int{100, 75, 50} {
		when := base.Add(time.Duration(i*10) * time.Minute)
		m1.NowFn = func() time.Time { return when }
		_ = m1.Report(AgentClaudeCode, p, "auto", time.Time{}, time.Time{})
	}
	time.Sleep(80 * time.Millisecond) // persist 完了待ち

	// "after restart": 新 Monitor を作って load
	m2 := New()
	if err := m2.LoadHistory(path); err != nil {
		t.Fatal(err)
	}
	s := m2.UsageSummary(AgentClaudeCode)
	if s.TotalReports != 3 {
		t.Errorf("after restart: TotalReports got %d, want 3", s.TotalReports)
	}
	if len(s.Samples) != 3 {
		t.Errorf("after restart: samples got %d, want 3", len(s.Samples))
	}
	// 時系列順
	if s.Samples[0].RemainingPercent != 100 || s.Samples[2].RemainingPercent != 50 {
		t.Errorf("samples order broken: %v", s.Samples)
	}
}

func TestLoadHistory_MissingFileIsNotError(t *testing.T) {
	m := New()
	err := m.LoadHistory("/nonexistent/path/usage.jsonl")
	if err != nil {
		t.Errorf("missing file should not error, got %v", err)
	}
}

func TestLoadHistory_SkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	// 正しい line + ガベージ + 正しい line を書く
	content := `{"agent":"claude_code","at":"2026-05-13T12:00:00Z","remaining_percent":100}
THIS IS NOT JSON
{"agent":"claude_code","at":"2026-05-13T12:10:00Z","remaining_percent":80}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadHistory(path); err != nil {
		t.Fatal(err)
	}
	s := m.UsageSummary(AgentClaudeCode)
	if s.TotalReports != 2 {
		t.Errorf("corrupt line skipped: TotalReports got %d, want 2", s.TotalReports)
	}
}

func TestLoadHistory_CapsAtForecastWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	// 30 line(ForecastWindowSize=10 を超える)
	var content []byte
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		t := base.Add(time.Duration(i) * time.Minute)
		line := []byte(`{"agent":"claude_code","at":"` + t.UTC().Format(time.RFC3339Nano) + `","remaining_percent":` + intToString(100-i) + "}\n")
		content = append(content, line...)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadHistory(path); err != nil {
		t.Fatal(err)
	}
	s := m.UsageSummary(AgentClaudeCode)
	if s.TotalReports != ForecastWindowSize {
		t.Errorf("expected cap at %d, got %d", ForecastWindowSize, s.TotalReports)
	}
	// 最新が保持されている(70, 30 件目)
	if s.Samples[len(s.Samples)-1].RemainingPercent != 71 {
		t.Errorf("last sample should be 71 (newest), got %d", s.Samples[len(s.Samples)-1].RemainingPercent)
	}
}

func TestPersist_NoPathIsNoOp(t *testing.T) {
	m := New()
	// persistPath を設定せずに Report
	m.NowFn = func() time.Time { return time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC) }
	if err := m.Report(AgentClaudeCode, 80, "auto", time.Time{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// persist goroutine が走るが、path 無いので no-op
	time.Sleep(30 * time.Millisecond)
	// crash しなければ OK
}

// intToString は test helper(strconv 依存削減のため)
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}

func TestLoadHistory_RestoresAgentStatus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	// 100% → 40% で終わる history
	content := `{"agent":"claude_code","at":"2026-05-13T12:00:00Z","remaining_percent":100,"source":"auto"}
{"agent":"claude_code","at":"2026-05-13T12:30:00Z","remaining_percent":40,"source":"auto"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadHistory(path); err != nil {
		t.Fatal(err)
	}
	st, _ := m.Status(AgentClaudeCode)
	if st.RemainingPercent != 40 {
		t.Errorf("restored remaining: got %d, want 40", st.RemainingPercent)
	}
	if st.LastReportSource != "auto" {
		t.Errorf("restored source: got %q, want 'auto'", st.LastReportSource)
	}
	// 40% < WarnThreshold(20%)?20%以上なので ACTIVE
	if st.State != StateActive {
		t.Errorf("state: got %s, want ACTIVE", st.State)
	}
}

func TestLoadHistory_RestoresWarnState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	// 10% で終わる → WARN
	content := `{"agent":"claude_code","at":"2026-05-13T12:00:00Z","remaining_percent":10,"source":"auto"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	_ = m.LoadHistory(path)
	st, _ := m.Status(AgentClaudeCode)
	if st.State != StateWarn {
		t.Errorf("10%% should be WARN, got %s", st.State)
	}
}

func TestLoadHistory_RestoresExhausted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	// 0% で終わる → EXHAUSTED
	content := `{"agent":"claude_code","at":"2026-05-13T12:00:00Z","remaining_percent":0,"source":"auto"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	_ = m.LoadHistory(path)
	st, _ := m.Status(AgentClaudeCode)
	if st.State != StateExhausted {
		t.Errorf("0%% should be EXHAUSTED, got %s", st.State)
	}
}

// ─── v0.22.0: compact JSONL format ────────────────────────────

func TestPersist_WritesCompactFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "u.jsonl")
	m := New()
	m.SetPersistPath(path)
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return base }
	_ = m.Report(AgentClaudeCode, 80, "auto", time.Time{}, time.Time{})
	time.Sleep(50 * time.Millisecond)

	data, _ := os.ReadFile(path)
	line := string(data)
	// 新 format check
	if !strings.Contains(line, `"a":"cc"`) {
		t.Errorf("compact format expected (a:cc); got: %s", line)
	}
	if !strings.Contains(line, `"r":80`) {
		t.Errorf("compact format expected (r:80); got: %s", line)
	}
	if !strings.Contains(line, `"t":`) {
		t.Errorf("compact format expected (t:unix); got: %s", line)
	}
	// 旧 format field が漏れていないか
	if strings.Contains(line, `"agent":`) {
		t.Errorf("legacy field should not appear in new writes: %s", line)
	}
	// byte size: 旧 format ~95 byte → 新 format ~40 byte
	if len(line) >= 80 {
		t.Errorf("compact line should be < 80 bytes (legacy was ~95); got %d", len(line))
	}
}

func TestLoadHistory_ReadsLegacyFormat(t *testing.T) {
	// v0.17-v0.21 で書込まれた旧 line を新コードが読めるか
	dir := t.TempDir()
	path := filepath.Join(dir, "u.jsonl")
	legacy := `{"agent":"claude_code","at":"2026-05-13T12:00:00Z","remaining_percent":75,"source":"auto"}
{"agent":"claude_code","at":"2026-05-13T12:30:00Z","remaining_percent":40,"source":"auto"}
`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadHistory(path); err != nil {
		t.Fatal(err)
	}
	s := m.UsageSummary(AgentClaudeCode)
	if s.TotalReports != 2 {
		t.Errorf("legacy lines should load: got %d, want 2", s.TotalReports)
	}
	st, _ := m.Status(AgentClaudeCode)
	if st.RemainingPercent != 40 {
		t.Errorf("legacy → AgentStatus: got %d, want 40", st.RemainingPercent)
	}
}

func TestLoadHistory_ReadsMixedFormat(t *testing.T) {
	// 旧 line と 新 line が混在(migration 過程)
	dir := t.TempDir()
	path := filepath.Join(dir, "u.jsonl")
	mixed := `{"agent":"claude_code","at":"2026-05-13T12:00:00Z","remaining_percent":100,"source":"auto"}
{"a":"cc","t":1715602800,"r":60,"s":"auto"}
`
	if err := os.WriteFile(path, []byte(mixed), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadHistory(path); err != nil {
		t.Fatal(err)
	}
	s := m.UsageSummary(AgentClaudeCode)
	if s.TotalReports != 2 {
		t.Errorf("mixed: got %d, want 2", s.TotalReports)
	}
}

func TestCompactExpandAgent_Roundtrip(t *testing.T) {
	cases := []Agent{AgentClaudeCode, AgentWindsurf}
	for _, a := range cases {
		c := compactAgent(a)
		back := expandAgent(c)
		if back != a {
			t.Errorf("roundtrip %s → %s → %s", a, c, back)
		}
	}
	// legacy full name 経由も
	if expandAgent("claude_code") != AgentClaudeCode {
		t.Error("legacy full name should expand")
	}
	if expandAgent("windsurf") != AgentWindsurf {
		t.Error("legacy full name should expand")
	}
}

// TestCompactAgent_UnknownAgent covers the default case in compactAgent:
// unrecognised Agent values are returned as-is (string passthrough).
func TestCompactAgent_UnknownAgent(t *testing.T) {
	got := compactAgent(Agent("custom_ai"))
	if got != "custom_ai" {
		t.Errorf("compactAgent(custom_ai) = %q, want %q", got, "custom_ai")
	}
}

// TestExpandAgent_UnknownString covers the default case in expandAgent:
// unrecognised strings are wrapped as-is in Agent().
func TestExpandAgent_UnknownString(t *testing.T) {
	got := expandAgent("unknown_code")
	if got != Agent("unknown_code") {
		t.Errorf("expandAgent(unknown_code) = %q, want %q", got, "unknown_code")
	}
}

// TestLoadHistory_OpenError covers the non-ErrNotExist open failure in
// LoadHistory. Passing "regularfile/ghost" triggers ENOTDIR (not ErrNotExist).
func TestLoadHistory_OpenError(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	err := m.LoadHistory(filepath.Join(blocker, "ghost"))
	if err == nil {
		t.Error("expected error for ENOTDIR open failure, got nil")
	}
}
