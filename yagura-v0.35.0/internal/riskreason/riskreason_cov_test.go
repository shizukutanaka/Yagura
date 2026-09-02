package riskreason

import (
	"strings"
	"testing"
)

// ─── ScoreWith branch coverage ───────────────────────────────────

// TestScore_NoSeverityAtAll covers the severity `default` arm: with neither a
// CVSS number nor a severity string, severityBucket returns "" and the bucket
// is surfaced as an unknown.
func TestScore_NoSeverityAtAll(t *testing.T) {
	r := Score(Input{CVE: "CVE-2026-1000"})
	found := false
	for _, u := range r.Unknowns {
		if u == "severity (no CVSS or severity string provided)" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected severity unknown when no CVSS/severity, got %v", r.Unknowns)
	}
}

// TestScore_StagePausedAndMaintenance covers the "paused" and "maintenance"
// stage arms (archived is already covered elsewhere).
func TestScore_StagePausedAndMaintenance(t *testing.T) {
	paused := Score(Input{CVE: "a", CVSS: 7.0, Stage: "paused"})
	maint := Score(Input{CVE: "b", CVSS: 7.0, Stage: "maintenance"})
	if !hasFactor(paused, "stage") {
		t.Errorf("paused stage should add a 'stage' factor: %+v", paused.Factors)
	}
	if !hasFactor(maint, "stage") {
		t.Errorf("maintenance stage should add a 'stage' factor: %+v", maint.Factors)
	}
}

// TestScore_EPSSMedium covers the EPSS >= 0.1 (elevated) arm.
func TestScore_EPSSMedium(t *testing.T) {
	r := Score(Input{CVE: "a", CVSS: 7.0, EPSS: 0.2})
	if !hasFactor(r, "epss") {
		t.Errorf("EPSS 0.2 should add an 'epss' factor: %+v", r.Factors)
	}
}

// TestScore_BlastRadiusCapped covers the d > BlastRadiusCap clamp: a very large
// dependent count is capped.
func TestScore_BlastRadiusCapped(t *testing.T) {
	w := DefaultWeights()
	r := ScoreWith(Input{CVE: "a", CVSS: 7.0, Dependents: 100000}, w)
	for _, f := range r.Factors {
		if f.Name == "blast_radius" {
			if f.Delta > w.BlastRadiusCap {
				t.Errorf("blast_radius delta %d exceeds cap %d", f.Delta, w.BlastRadiusCap)
			}
			return
		}
	}
	t.Error("expected a blast_radius factor for a high dependent count")
}

// TestScoreAll_TieBreakByCVE covers the CVE tie-break in ScoreAllWith when two
// results share the same score.
func TestScoreAll_TieBreakByCVE(t *testing.T) {
	in := []Input{
		{CVE: "CVE-2026-0002", CVSS: 5.0},
		{CVE: "CVE-2026-0001", CVSS: 5.0}, // identical score, earlier CVE
	}
	rs := ScoreAll(in)
	if rs[0].Score != rs[1].Score {
		t.Fatalf("test premise broken: scores differ (%d vs %d)", rs[0].Score, rs[1].Score)
	}
	if rs[0].CVE != "CVE-2026-0001" {
		t.Errorf("equal scores should tie-break by CVE ascending, got %s first", rs[0].CVE)
	}
}

// ─── recommend / ssvcDecide direct branch coverage ───────────────

// TestRecommend_SoonWithPatchImpact covers the PrioritySoon arm where the patch
// has business impact (deferral exception clause).
func TestRecommend_SoonWithPatchImpact(t *testing.T) {
	got := recommend(PrioritySoon, Input{PatchBlocksBusiness: b(true)}, nil)
	if !strings.Contains(got, "risk-acceptance exception") {
		t.Errorf("PrioritySoon + patch impact should mention a deferral exception, got %q", got)
	}
}

// TestSSVCDecide_PocAttend covers the poc → (high||open||auto) Attend arm:
// a PoC exploit that is automatable but neither high-impact nor open-exposure.
func TestSSVCDecide_PocAttend(t *testing.T) {
	if got := ssvcDecide("poc", "controlled", "yes", "medium"); got != SSVCAttend {
		t.Errorf("poc + automatable (not high/open) should be Attend, got %v", got)
	}
}
