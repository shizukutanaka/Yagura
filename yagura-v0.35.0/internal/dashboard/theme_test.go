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

// TestDashboard_ManualThemeToggle verifies the user can override the OS
// preference: forced-theme CSS overrides, a persisted pre-paint script, and a
// visible toggle control are all present.
func TestDashboard_ManualThemeToggle(t *testing.T) {
	body := renderDashboard(t)
	for _, want := range []string{
		`:root[data-theme="light"]`, // forced light overrides OS/media
		`:root[data-theme="dark"]`,  // forced dark overrides OS/media
		"data-theme",                // pre-paint + toggle set the attribute
		"yagura-theme",              // localStorage key persists the choice
		`id="theme-toggle"`,         // visible control
	} {
		if !strings.Contains(body, want) {
			t.Errorf("theme toggle piece missing: %q", want)
		}
	}
	// the pre-paint script must set data-theme from localStorage before body paint
	if !strings.Contains(body, "localStorage.getItem('yagura-theme')") {
		t.Errorf("pre-paint theme script missing")
	}
	// no stray hardcoded structural hex should remain inline on the header timestamp
	if strings.Contains(body, "color:#8b949e;font-size:12px") {
		t.Errorf("header timestamp still uses a hardcoded muted hex instead of var(--muted)")
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
