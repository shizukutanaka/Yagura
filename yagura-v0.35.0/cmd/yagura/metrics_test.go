package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/hookreceiver"
	"github.com/shizukutanaka/yagura/internal/mcp"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/promexport"
	"github.com/shizukutanaka/yagura/internal/registry"
)

func TestCollectYaguraMetrics_CacheStats(t *testing.T) {
	hr, err := hookreceiver.NewReceiver(filepath.Join(t.TempDir(), "hooks.jsonl"), nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	srv := mcp.New("", nil)
	// Prime the cache with a hit and a miss to satisfy cs.Hits+cs.Misses > 0.
	cache := srv.Cache()
	cache.Set("k", []byte("v"))
	_, _ = cache.Get("k") // hit
	_, _ = cache.Get("missing") // miss

	var buf bytes.Buffer
	if err := promexport.Render(&buf, collectYaguraMetrics(srv, hr, &healthState{})); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "yagura_cache_hits_total") {
		t.Errorf("expected cache hits metric, got:\n%s", out)
	}
	if !strings.Contains(out, "yagura_cache_misses_total") {
		t.Errorf("expected cache misses metric, got:\n%s", out)
	}
}

func TestCollectYaguraMetrics_AlertStore(t *testing.T) {
	hr, err := hookreceiver.NewReceiver(filepath.Join(t.TempDir(), "hooks.jsonl"), nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	srv := mcp.New("", nil)
	st, err := alertfix.NewStore(filepath.Join(t.TempDir(), "alerts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	srv.SetAlertStore(st)

	var buf bytes.Buffer
	if err := promexport.Render(&buf, collectYaguraMetrics(srv, hr, &healthState{})); err != nil {
		t.Fatal(err)
	}
	// Store is empty — Stats() returns an empty map; the range emits nothing.
	// The test just verifies no panic and valid Prometheus output.
	out := buf.String()
	// mcp tool stats should still be present (empty server → 0 tools → empty samples OK).
	_ = out
}

func TestCollectYaguraMetrics_HookToolCalls(t *testing.T) {
	dir := t.TempDir()
	reg, err := registry.New(filepath.Join(dir, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "breeze")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&project.Project{
		Slug: "breeze", DisplayName: "Breeze", Repository: "o/breeze",
		LocalPath: proj, Stage: project.StageActive,
	}); err != nil {
		t.Fatal(err)
	}
	hr, err := hookreceiver.NewReceiver(filepath.Join(dir, "hooks.jsonl"), &registryLookup{reg: reg}, 100)
	if err != nil {
		t.Fatal(err)
	}
	// a Claude Code tool event and a foreign-agent (Gemini) one, both in the project cwd
	for _, payload := range []string{
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"` + proj + `"}`,
		`{"agent":"gemini_cli","event":"beforeToolCall","tool":"Bash","cwd":"` + proj + `"}`,
	} {
		req := httptest.NewRequest("POST", "/hooks/agent", strings.NewReader(payload))
		hr.Handle(httptest.NewRecorder(), req)
	}

	cols := collectYaguraMetrics(mcp.New("", nil), hr, &healthState{})
	var buf bytes.Buffer
	if err := promexport.Render(&buf, cols); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "yagura_hook_tool_calls_total") {
		t.Fatalf("per-tool hook metric missing:\n%s", out)
	}
	if !strings.Contains(out, `project="breeze"`) || !strings.Contains(out, `tool="Bash"`) {
		t.Errorf("expected project/tool labels:\n%s", out)
	}
	// both agents counted under the same tool → value 2
	if !strings.Contains(out, `tool="Bash"} 2`) {
		t.Errorf("expected 2 Bash calls across agents:\n%s", out)
	}
}

func TestCollectYaguraMetrics_PortfolioAlerts(t *testing.T) {
	hr, err := hookreceiver.NewReceiver(filepath.Join(t.TempDir(), "hooks.jsonl"), nil, 10)
	if err != nil {
		t.Fatal(err)
	}

	// before any sweep → no portfolio gauge
	h := &healthState{}
	var buf bytes.Buffer
	_ = promexport.Render(&buf, collectYaguraMetrics(mcp.New("", nil), hr, h))
	if strings.Contains(buf.String(), "yagura_portfolio_alerts") {
		t.Error("portfolio gauge should be absent before the first sweep")
	}

	// after a sweep with a critical + a high alert
	h.set(alertfix.Report{
		Total:       2,
		HasCritical: true,
		BySeverity:  map[alertfix.Severity]int{alertfix.SevCritical: 1, alertfix.SevHigh: 1},
	})
	buf.Reset()
	if err := promexport.Render(&buf, collectYaguraMetrics(mcp.New("", nil), hr, h)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "yagura_portfolio_alerts") {
		t.Fatalf("portfolio gauge missing after sweep:\n%s", out)
	}
	if !strings.Contains(out, `severity="critical"} 1`) {
		t.Errorf("expected critical=1 gauge:\n%s", out)
	}
	if !strings.Contains(out, `severity="high"} 1`) {
		t.Errorf("expected high=1 gauge:\n%s", out)
	}
	if !strings.Contains(out, `severity="medium"} 0`) {
		t.Errorf("expected medium=0 gauge:\n%s", out)
	}
}
