package main

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/registry"
)

func TestHealthSweep_FlagsCriticalVuln(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&project.Project{
		Slug: "breeze", DisplayName: "Breeze", Repository: "github.com/o/breeze",
		Stage: project.StageActive,
	}); err != nil {
		t.Fatal(err)
	}
	// sensor fields are scanner-only in production (MCP can't set them), but the
	// registry layer persists the full struct — mirror what the scanner does.
	p, _ := reg.Get("breeze")
	p.VulnCritical = 3
	if err := reg.Update(p); err != nil {
		t.Fatalf("inject sensor data: %v", err)
	}

	report := healthSweep(reg, nil)
	if report.ProjectsScanned != 1 {
		t.Errorf("projects_scanned = %d, want 1", report.ProjectsScanned)
	}
	if !report.HasCritical {
		t.Errorf("expected a critical alert for 3 critical vulns, got %+v", report.BySeverity)
	}
	if report.BySeverity[alertfix.SevCritical] < 1 {
		t.Errorf("expected >=1 critical, got %d", report.BySeverity[alertfix.SevCritical])
	}
}

func TestHealthSweep_ExcludesResolvedAlerts(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(&project.Project{
		Slug: "breeze", DisplayName: "Breeze", Repository: "github.com/o/breeze", Stage: project.StageActive,
	}); err != nil {
		t.Fatal(err)
	}
	p, _ := reg.Get("breeze")
	p.VulnCritical = 3
	if err := reg.Update(p); err != nil {
		t.Fatal(err)
	}
	store, err := alertfix.NewStore("") // in-memory
	if err != nil {
		t.Fatal(err)
	}

	// unfiltered: the critical alert is present
	before := healthSweep(reg, store)
	if before.Total == 0 {
		t.Fatal("expected at least one alert before resolving")
	}
	// resolve every alert the sweep produced
	for _, a := range before.Alerts {
		if err := store.Resolve(a.ID, "handled"); err != nil {
			t.Fatal(err)
		}
	}
	// next sweep must exclude the resolved alerts
	after := healthSweep(reg, store)
	if after.Total != 0 {
		t.Errorf("resolved alerts should be filtered out, got %d (%+v)", after.Total, after.BySeverity)
	}
	if after.HasCritical {
		t.Error("has_critical should be false once the critical alert is resolved")
	}
}

func TestHealthState_ProviderReflectsLatestSweep(t *testing.T) {
	h := &healthState{}
	// before any sweep → not ready
	if _, ok := h.PortfolioHealth(); ok {
		t.Error("PortfolioHealth should be ok=false before the first sweep")
	}
	h.set(alertfix.Report{
		Total:       2,
		HasCritical: true,
		BySeverity:  map[alertfix.Severity]int{alertfix.SevCritical: 1, alertfix.SevHigh: 1},
	})
	hs, ok := h.PortfolioHealth()
	if !ok {
		t.Fatal("PortfolioHealth should be ok after a sweep")
	}
	if hs.Total != 2 || hs.Critical != 1 || hs.High != 1 || !hs.HasCritical {
		t.Errorf("summary mismatch: %+v", hs)
	}
	if hs.At.IsZero() {
		t.Error("expected a sweep timestamp")
	}
}

func TestHealthState_PortfolioAlertsMapsReport(t *testing.T) {
	h := &healthState{}
	if _, _, ok := h.PortfolioAlerts(); ok {
		t.Error("PortfolioAlerts should be ok=false before the first sweep")
	}
	h.set(alertfix.Report{
		Total: 1,
		Alerts: []alertfix.Alert{{
			Project: "breeze", Source: alertfix.SourceVuln, Severity: alertfix.SevCritical,
			Title: "critical CVE", Recommendation: "patch",
		}},
		BySeverity: map[alertfix.Severity]int{alertfix.SevCritical: 1},
	})
	alerts, _, ok := h.PortfolioAlerts()
	if !ok || len(alerts) != 1 {
		t.Fatalf("expected 1 alert after sweep, got ok=%v len=%d", ok, len(alerts))
	}
	a := alerts[0]
	if a.Project != "breeze" || a.Source != "vulns" || a.Severity != "critical" || a.Title != "critical CVE" {
		t.Errorf("alert mapping mismatch: %+v", a)
	}
}

func TestHealthSweep_CleanPortfolio(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = reg.Add(&project.Project{
		Slug: "calm", DisplayName: "Calm", Repository: "github.com/o/calm",
		Stage: project.StageActive,
	})
	report := healthSweep(reg, nil)
	if report.ProjectsScanned != 1 {
		t.Errorf("projects_scanned = %d, want 1", report.ProjectsScanned)
	}
	if report.HasCritical {
		t.Error("a project with no sensor findings should not be critical")
	}
}
