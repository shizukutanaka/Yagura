package dashboard

import (
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/registry"
)

func activityHandler(t *testing.T) *Handler {
	t.Helper()
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = reg.Add(&project.Project{Slug: "breeze", DisplayName: "Breeze", Repository: "o/breeze", Stage: project.StageActive})
	h, err := New(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// the Activity cell links to the per-project drill-down.
func TestActivity_ColumnLinksToDetail(t *testing.T) {
	h := activityHandler(t)
	h.SetHookActivityProvider(fakeHookActivity{m: map[string]HookActivity{
		"breeze": {Total: 12, Errors: 2, TopTool: "Bash"},
	}})
	html := get(t, h, "/dashboard").Body.String()
	if !strings.Contains(html, `href="/dashboard/activity?slug=breeze"`) {
		t.Errorf("Activity cell should link to the drill-down:\n%s", html)
	}
}

func TestActivity_DetailRendersSummary(t *testing.T) {
	h := activityHandler(t)
	h.SetHookActivityProvider(fakeHookActivity{
		m: map[string]HookActivity{"breeze": {Total: 5}},
		d: map[string]ActivityDetail{"breeze": {
			Slug:            "breeze",
			Summary:         "5 tool calls across 2 tools",
			ToolInvocations: 5,
			DistinctTools:   2,
			ErrorRate:       0.2,
			Agents:          []string{"claude_code", "gemini_cli"},
			ByTool:          []LabelCount{{Label: "Bash", Count: 3}, {Label: "Read", Count: 2}},
			ByOperation:     []LabelCount{{Label: "execute_tool", Count: 5}},
			ToolSequence:    []string{"Bash", "Read", "Bash"},
			Errors:          []ActivityError{{Tool: "Bash", ErrorType: "timeout", Agent: "claude_code"}},
			Anomalies:       []string{"3 consecutive Bash errors"},
		}},
	})
	w := get(t, h, "/dashboard/activity?slug=breeze")
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q", ct)
	}
	html := w.Body.String()
	for _, want := range []string{
		"Breeze", "5 tool calls across 2 tools", "Bash", "Read",
		"execute_tool", "20%", "timeout", "claude_code", "gemini_cli",
		"3 consecutive Bash errors", `href="/dashboard"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
}

// unknown slug (no recorded activity) must not 404 — it shows a guidance page.
func TestActivity_UnknownSlugShowsEmptyState(t *testing.T) {
	h := activityHandler(t)
	h.SetHookActivityProvider(fakeHookActivity{d: map[string]ActivityDetail{}})
	w := get(t, h, "/dashboard/activity?slug=ghost")
	if w.Code != 200 {
		t.Fatalf("code = %d, want 200 (no dead-ends)", w.Code)
	}
	html := w.Body.String()
	if !strings.Contains(html, "No recorded agent activity") {
		t.Errorf("expected empty-state guidance, got:\n%s", html)
	}
	if !strings.Contains(html, `href="/dashboard"`) {
		t.Error("empty state should still offer a way back")
	}
}

// no provider at all → still renders (empty state), never panics.
func TestActivity_NoProvider(t *testing.T) {
	h := activityHandler(t)
	w := get(t, h, "/dashboard/activity?slug=breeze")
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "No recorded agent activity") {
		t.Error("expected empty state without a provider")
	}
}
