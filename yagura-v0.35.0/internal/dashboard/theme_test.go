package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/logging"
	"github.com/shizukutanaka/yagura/internal/registry"
)

// renderDashboard renders the main dashboard HTML for theme assertions.
func renderDashboard(t *testing.T) string {
	t.Helper()
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(reg, logging.Discard())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status %d", rec.Code)
	}
	return rec.Body.String()
}

// TestDashboard_ThemeAware verifies the dashboard is no longer dark-only: it
// declares both light and dark color schemes and provides a
// prefers-color-scheme:light override, driven by CSS custom properties.
func TestDashboard_ThemeAware(t *testing.T) {
	body := renderDashboard(t)
	for _, want := range []string{
		"color-scheme:light dark", // native controls adapt to OS preference
		"prefers-color-scheme:light",
		"--bg:", "--text:", "--border:", // palette is variable-driven
		"var(--bg)", "var(--text)", // structural colors consume the variables
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard CSS missing %q (theme-aware conversion incomplete)", want)
		}
	}
	// dark defaults must be preserved exactly (dark users see no change)
	if !strings.Contains(body, "--bg:#0d1117") {
		t.Errorf("dark default --bg should stay #0d1117")
	}
	// agent-panel straggler colors are variable-driven too (no half-converted panel)
	for _, want := range []string{"--stripe:", "--ok-bg:", "--neutral-bg:", "var(--stripe)"} {
		if !strings.Contains(body, want) {
			t.Errorf("agent-panel theme var missing: %q", want)
		}
	}
	// browser chrome follows the scheme (media-qualified theme-color pair)
	if !strings.Contains(body, `content="#ffffff" media="(prefers-color-scheme: light)"`) {
		t.Errorf("light theme-color meta missing")
	}
}

// TestDashboard_NoBareStructuralHexInBody guards against a half-converted
// theme: the body/table structural backgrounds must go through variables, not
// a hardcoded dark hex that would stay dark under a light preference.
func TestDashboard_StructuralColorsUseVariables(t *testing.T) {
	body := renderDashboard(t)
	// the <body> rule must use var(--bg)/var(--text), not the literal dark hex
	if strings.Contains(body, "background: #0d1117; color: #e6edf3") {
		t.Errorf("body still uses hardcoded dark colors instead of variables")
	}
}
