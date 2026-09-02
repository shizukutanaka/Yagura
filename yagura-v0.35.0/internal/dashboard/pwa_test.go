package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/registry"
)

func newPWAHandler(t *testing.T) *Handler {
	t.Helper()
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(t *testing.T, h *Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestPWA_Manifest(t *testing.T) {
	w := get(t, newPWAHandler(t), "/dashboard/manifest.webmanifest")
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "manifest") {
		t.Errorf("content-type = %q", ct)
	}
	var m struct {
		Name       string `json:"name"`
		StartURL   string `json:"start_url"`
		Display    string `json:"display"`
		ThemeColor string `json:"theme_color"`
		Icons      []struct {
			Src string `json:"src"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest not valid JSON: %v", err)
	}
	if m.StartURL != "/dashboard" || m.Display != "standalone" || len(m.Icons) == 0 {
		t.Errorf("manifest missing required PWA fields: %+v", m)
	}
	if m.Icons[0].Src != "/dashboard/icon.svg" {
		t.Errorf("icon src = %q", m.Icons[0].Src)
	}
}

func TestPWA_ServiceWorker(t *testing.T) {
	w := get(t, newPWAHandler(t), "/dashboard/sw.js")
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("content-type = %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "addEventListener('fetch'") {
		t.Error("service worker must have a fetch handler to be installable")
	}
}

func TestPWA_Icon(t *testing.T) {
	w := get(t, newPWAHandler(t), "/dashboard/icon.svg")
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "svg") {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.HasPrefix(strings.TrimSpace(w.Body.String()), "<svg") {
		t.Error("icon is not an SVG")
	}
}

func TestPWA_DashboardHeadHasManifest(t *testing.T) {
	// the main dashboard HTML must reference the manifest + register the SW
	w := get(t, newPWAHandler(t), "/dashboard")
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	html := w.Body.String()
	for _, want := range []string{
		`rel="manifest"`,
		"/dashboard/manifest.webmanifest",
		`name="theme-color"`,
		"serviceWorker",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard HTML missing %q", want)
		}
	}
}

func TestPWA_UnknownSubPathStillRendersDashboard(t *testing.T) {
	// an unknown /dashboard/... path must fall through to the normal HTML (additive, non-breaking)
	w := get(t, newPWAHandler(t), "/dashboard/")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Yagura") {
		t.Errorf("unknown subpath should render dashboard: code=%d", w.Code)
	}
}

func TestDashboard_AddProjectForm(t *testing.T) {
	// fresh (0 projects): the add-project form must be present and open,
	// and it must target the MCP yagura_register tool (state changes go via MCP).
	w := get(t, newPWAHandler(t), "/dashboard")
	html := w.Body.String()
	for _, want := range []string{
		`id="addproj-form"`,
		`class="addproj"`,
		"yagura_register",
		"/mcp",
		`name="slug"`,
		`name="repository"`,
		"+ Add a project", // empty-state points to the form, not the CLI
	} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard HTML missing %q", want)
		}
	}
	// the old CLI-only dead-end message must be gone
	if strings.Contains(html, "from Claude Code to add one") {
		t.Error("empty state still tells non-CLI users to use Claude Code")
	}
}

type fakeHookActivity struct {
	m map[string]HookActivity
	d map[string]ActivityDetail
}

func (f fakeHookActivity) ProjectActivity(slug string) (HookActivity, bool) {
	a, ok := f.m[slug]
	return a, ok
}

func (f fakeHookActivity) ProjectActivityDetail(slug string) (ActivityDetail, bool) {
	d, ok := f.d[slug]
	return d, ok
}

func TestDashboard_ActivityColumn(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = reg.Add(&project.Project{Slug: "breeze", DisplayName: "Breeze", Repository: "o/breeze", Stage: project.StageActive})
	_ = reg.Add(&project.Project{Slug: "quiet", DisplayName: "Quiet", Repository: "o/quiet", Stage: project.StageActive})
	h, err := New(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.SetHookActivityProvider(fakeHookActivity{m: map[string]HookActivity{
		"breeze": {Total: 12, Errors: 2, TopTool: "Bash"},
		// "quiet" has no activity → "—"
	}})
	w := get(t, h, "/dashboard")
	html := w.Body.String()
	if !strings.Contains(html, ">Activity<") {
		t.Error("Activity column header missing")
	}
	for _, want := range []string{"12", "2⚠", "Bash"} {
		if !strings.Contains(html, want) {
			t.Errorf("active project activity missing %q", want)
		}
	}
	// the inactive project should show a dash; ensure a dash cell exists
	if !strings.Contains(html, `class="activity"`) || !strings.Contains(html, "—") {
		t.Error("inactive project should render an em-dash in the activity cell")
	}
}
