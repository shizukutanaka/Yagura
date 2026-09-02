package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/registry"
)

type fakeHealth struct {
	hs     HealthSummary
	ok     bool
	alerts []AlertItem
}

func (f fakeHealth) PortfolioHealth() (HealthSummary, bool) { return f.hs, f.ok }

func (f fakeHealth) PortfolioAlerts() ([]AlertItem, time.Time, bool) {
	return f.alerts, time.Now(), f.ok
}

func healthDash(t *testing.T) *Handler {
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

func TestHealthBanner_ShownWithAlerts(t *testing.T) {
	h := healthDash(t)
	h.SetPortfolioHealthProvider(fakeHealth{ok: true, hs: HealthSummary{
		Total: 3, Critical: 1, High: 2, HasCritical: true, At: time.Now(),
	}})
	html := get(t, h, "/dashboard").Body.String()
	if !strings.Contains(html, `class="health-banner`) {
		t.Fatal("expected a health banner when there are alerts")
	}
	for _, want := range []string{"Portfolio health", "3 alert", "1 critical", "2 high"} {
		if !strings.Contains(html, want) {
			t.Errorf("banner missing %q", want)
		}
	}
	if !strings.Contains(html, "crit") {
		t.Error("a critical sweep should use the crit banner style")
	}
}

func TestHealthBanner_NoOrphanSeparator(t *testing.T) {
	// medium-only (no critical/high) must not render a leading " · " separator.
	h := healthDash(t)
	h.SetPortfolioHealthProvider(fakeHealth{ok: true, hs: HealthSummary{
		Total: 5, Medium: 5, At: time.Now(),
	}})
	html := get(t, h, "/dashboard").Body.String()
	if !strings.Contains(html, "5 medium") {
		t.Fatal("expected medium count in banner")
	}
	// the breakdown span should start straight into "5 medium", not " · 5 medium"
	if strings.Contains(html, `<span class="hb-breakdown"> · `) {
		t.Errorf("orphan leading separator in breakdown:\n%s", html)
	}
}

func TestHealthBanner_HiddenWhenClean(t *testing.T) {
	h := healthDash(t)
	h.SetPortfolioHealthProvider(fakeHealth{ok: true, hs: HealthSummary{Total: 0}})
	if strings.Contains(get(t, h, "/dashboard").Body.String(), "Portfolio health:") {
		t.Error("no banner should show when there are 0 alerts")
	}
}

func TestHealthBanner_HiddenWithoutProvider(t *testing.T) {
	h := healthDash(t)
	if strings.Contains(get(t, h, "/dashboard").Body.String(), "Portfolio health:") {
		t.Error("no banner should show without a provider")
	}
}

func TestHealthBanner_HiddenBeforeFirstSweep(t *testing.T) {
	h := healthDash(t)
	h.SetPortfolioHealthProvider(fakeHealth{ok: false}) // no sweep yet
	if strings.Contains(get(t, h, "/dashboard").Body.String(), "Portfolio health:") {
		t.Error("no banner should show before the first sweep (ok=false)")
	}
}

func TestHealthBanner_LinksToAlertDetail(t *testing.T) {
	h := healthDash(t)
	h.SetPortfolioHealthProvider(fakeHealth{ok: true, hs: HealthSummary{Total: 1, Critical: 1, HasCritical: true}})
	if !strings.Contains(get(t, h, "/dashboard").Body.String(), `href="/dashboard/alerts"`) {
		t.Error("banner should link to the alert drill-down")
	}
}

func TestAlertDetail_RendersAlerts(t *testing.T) {
	h := healthDash(t)
	h.SetPortfolioHealthProvider(fakeHealth{ok: true, alerts: []AlertItem{
		{ID: "breeze:vulns:critical", Project: "breeze", Source: "vulns", Severity: "critical", Title: "3 critical CVEs", Recommendation: "patch now"},
		{ID: "tessera:ci:high", Project: "tessera", Source: "ci", Severity: "high", Title: "CI failing", Recommendation: "fix the build"},
	}})
	w := get(t, h, "/dashboard/alerts")
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	html := w.Body.String()
	for _, want := range []string{"Portfolio alerts", "breeze", "3 critical CVEs", "patch now", "tessera", "CI failing", `href="/dashboard"`} {
		if !strings.Contains(html, want) {
			t.Errorf("alert page missing %q", want)
		}
	}
}

func TestAlertDetail_HasResolveSnoozeActions(t *testing.T) {
	h := healthDash(t)
	h.SetPortfolioHealthProvider(fakeHealth{ok: true, alerts: []AlertItem{
		{ID: "breeze:vulns:critical", Project: "breeze", Source: "vulns", Severity: "critical", Title: "CVE"},
	}})
	html := get(t, h, "/dashboard/alerts").Body.String()
	for _, want := range []string{
		`data-alert-id="breeze:vulns:critical"`,   // row carries the id
		`data-act="resolve"`, `data-act="snooze"`, // both action buttons
		"yagura_alert_resolve",    // JS posts the audited lifecycle tool
		"/mcp",                    // …to the MCP endpoint (same audited path as register)
		`class="snz"`,             // snooze-duration selector
		`value="1"`, `value="30"`, // 1d / 30d options beyond the 7d default
		`value="7" selected`, // 7d remains the default
	} {
		if !strings.Contains(html, want) {
			t.Errorf("alert actions missing %q", want)
		}
	}
}

func TestAlertDetail_EmptyStateNoProvider(t *testing.T) {
	h := healthDash(t)
	w := get(t, h, "/dashboard/alerts")
	if w.Code != 200 {
		t.Fatalf("code = %d, want 200 (no dead-ends)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "No alerts") {
		t.Error("expected empty-state copy without a provider")
	}
}

func TestAlertDetail_EmptyStateBeforeSweep(t *testing.T) {
	h := healthDash(t)
	h.SetPortfolioHealthProvider(fakeHealth{ok: false})
	if !strings.Contains(get(t, h, "/dashboard/alerts").Body.String(), "No alerts") {
		t.Error("expected empty-state before the first sweep")
	}
}
