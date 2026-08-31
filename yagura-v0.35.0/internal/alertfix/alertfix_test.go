package alertfix

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
}

func thresholds() Thresholds {
	t := DefaultThresholds()
	t.NowFn = fixedNow
	return t
}

// ─── 単一 alert 発火 ─────────────────────────────

func TestEvaluate_NoAlertsForHealthy(t *testing.T) {
	snap := ProjectSnapshot{
		Slug:           "clean",
		CIStatus:       "passing",
		ScorecardScore: 9.0,
		PlanIsHealthy:  true,
		HasPlanMd:      true,
		LatestActivity: fixedNow().Add(-1 * 24 * time.Hour),
	}
	as := Evaluate(snap, thresholds())
	if len(as) != 0 {
		t.Errorf("healthy project should yield 0 alerts: %v", as)
	}
}

func TestEvaluate_VulnCritical(t *testing.T) {
	snap := ProjectSnapshot{Slug: "x", VulnCritical: 3, CIStatus: "passing", PlanIsHealthy: true, HasPlanMd: true}
	as := Evaluate(snap, thresholds())
	if len(as) != 1 || as[0].Severity != SevCritical || as[0].Source != SourceVuln {
		t.Errorf("vuln critical: %v", as)
	}
	if as[0].SuggestedTool != "yagura_vulns" {
		t.Errorf("suggested tool: %s", as[0].SuggestedTool)
	}
	if as[0].MetricInt != 3 {
		t.Errorf("metric: %d", as[0].MetricInt)
	}
}

func TestEvaluate_VulnHighOnly(t *testing.T) {
	snap := ProjectSnapshot{Slug: "x", VulnHigh: 2, CIStatus: "passing", PlanIsHealthy: true, HasPlanMd: true}
	as := Evaluate(snap, thresholds())
	if len(as) != 1 || as[0].Severity != SevHigh {
		t.Errorf("vuln high: %v", as)
	}
}

func TestEvaluate_VulnCriticalSuppressesHighAlert(t *testing.T) {
	// CRIT 優先で出すので HIGH alert は別途出さない(現実装)
	snap := ProjectSnapshot{Slug: "x", VulnCritical: 1, VulnHigh: 5, CIStatus: "passing",
		PlanIsHealthy: true, HasPlanMd: true}
	as := Evaluate(snap, thresholds())
	for _, a := range as {
		if a.Source == SourceVuln && a.Severity == SevHigh {
			t.Errorf("HIGH should be suppressed when CRIT exists: %v", a)
		}
	}
}

func TestEvaluate_CIFailing(t *testing.T) {
	snap := ProjectSnapshot{Slug: "x", CIStatus: "failing", PlanIsHealthy: true, HasPlanMd: true}
	as := Evaluate(snap, thresholds())
	found := false
	for _, a := range as {
		if a.Source == SourceCI {
			found = true
			if a.Severity != SevHigh {
				t.Errorf("CI severity: %s", a.Severity)
			}
		}
	}
	if !found {
		t.Error("CI failing should produce alert")
	}
}

func TestEvaluate_PlanUnhealthy(t *testing.T) {
	snap := ProjectSnapshot{
		Slug: "x", CIStatus: "passing", HasPlanMd: true, PlanIsHealthy: false,
		PlanIssues: []string{"missing scope", "missing DoD"},
	}
	as := Evaluate(snap, thresholds())
	if !anyOfSource(as, SourcePlan) {
		t.Errorf("plan alert missing: %v", as)
	}
	for _, a := range as {
		if a.Source == SourcePlan {
			if !strings.Contains(a.Description, "missing scope") {
				t.Errorf("desc should include issue detail: %s", a.Description)
			}
		}
	}
}

func TestEvaluate_PlanIssuesIgnoredWhenNoPlanMd(t *testing.T) {
	// HasPlanMd=false なら plan alert は出さない (Plan.md ない project は別問題)
	snap := ProjectSnapshot{Slug: "x", CIStatus: "passing", HasPlanMd: false, PlanIsHealthy: false}
	as := Evaluate(snap, thresholds())
	if anyOfSource(as, SourcePlan) {
		t.Error("no Plan.md should not yield plan alert")
	}
}

func TestEvaluate_StaleByDays(t *testing.T) {
	snap := ProjectSnapshot{
		Slug:           "x",
		CIStatus:       "passing",
		PlanIsHealthy:  true,
		HasPlanMd:      true,
		LatestActivity: fixedNow().Add(-60 * 24 * time.Hour),
	}
	as := Evaluate(snap, thresholds())
	if !anyOfSource(as, SourceStale) {
		t.Errorf("60-day-old project should be stale: %v", as)
	}
	for _, a := range as {
		if a.Source == SourceStale && a.MetricInt < 30 {
			t.Errorf("stale days metric: %d", a.MetricInt)
		}
	}
}

func TestEvaluate_StaleNotTriggeredForRecent(t *testing.T) {
	snap := ProjectSnapshot{
		Slug:           "x",
		CIStatus:       "passing",
		PlanIsHealthy:  true,
		HasPlanMd:      true,
		LatestActivity: fixedNow().Add(-5 * 24 * time.Hour),
	}
	as := Evaluate(snap, thresholds())
	if anyOfSource(as, SourceStale) {
		t.Error("5-day-old project should not be stale")
	}
}

func TestEvaluate_ScorecardBelowThreshold(t *testing.T) {
	snap := ProjectSnapshot{Slug: "x", CIStatus: "passing", PlanIsHealthy: true, HasPlanMd: true,
		ScorecardScore: 3.0}
	as := Evaluate(snap, thresholds())
	if !anyOfSource(as, SourceScorecard) {
		t.Errorf("low scorecard should alert: %v", as)
	}
}

func TestEvaluate_ScorecardZeroDoesNotAlert(t *testing.T) {
	// 0 = not yet measured。誤った警告を避ける。
	snap := ProjectSnapshot{Slug: "x", CIStatus: "passing", PlanIsHealthy: true, HasPlanMd: true,
		ScorecardScore: 0}
	as := Evaluate(snap, thresholds())
	if anyOfSource(as, SourceScorecard) {
		t.Error("zero scorecard (unmeasured) should not alert")
	}
}

func TestEvaluate_OpenIssuesHigh(t *testing.T) {
	snap := ProjectSnapshot{Slug: "x", CIStatus: "passing", PlanIsHealthy: true, HasPlanMd: true,
		OpenIssues: 25}
	as := Evaluate(snap, thresholds())
	if !anyOfSource(as, SourceOpenIssues) {
		t.Errorf("25 open issues should alert: %v", as)
	}
}

// ─── 複数 alert 同時 ────────────────────────────

func TestEvaluate_MultipleAlertsRankedBySeverity(t *testing.T) {
	snap := ProjectSnapshot{
		Slug:           "messy",
		VulnCritical:   2,
		CIStatus:       "failing",
		ScorecardScore: 2.0,
		PlanIsHealthy:  false, HasPlanMd: true,
		PlanIssues:     []string{"missing DoD"},
		OpenIssues:     30,
		LatestActivity: fixedNow().Add(-100 * 24 * time.Hour),
	}
	as := Evaluate(snap, thresholds())
	if len(as) < 5 {
		t.Errorf("expected ≥5 alerts, got %d", len(as))
	}
	// CRITICAL が先頭
	if as[0].Severity != SevCritical {
		t.Errorf("first alert should be CRITICAL: %s", as[0].Severity)
	}
}

// ─── EvaluateAll ────────────────────────────────

func TestEvaluateAll_AggregatesAcrossProjects(t *testing.T) {
	snaps := []ProjectSnapshot{
		{Slug: "a", CIStatus: "passing", PlanIsHealthy: true, HasPlanMd: true},
		{Slug: "b", VulnCritical: 1, CIStatus: "passing", PlanIsHealthy: true, HasPlanMd: true},
		{Slug: "c", CIStatus: "failing", PlanIsHealthy: true, HasPlanMd: true},
	}
	r := EvaluateAll(snaps, thresholds())
	if r.Total != 2 {
		t.Errorf("total: got %d, want 2", r.Total)
	}
	if !r.HasCritical {
		t.Error("should report HasCritical")
	}
	if r.ByProject["b"] != 1 || r.ByProject["c"] != 1 {
		t.Errorf("by_project: %v", r.ByProject)
	}
	if r.BySeverity[SevCritical] != 1 || r.BySeverity[SevHigh] != 1 {
		t.Errorf("by_severity: %v", r.BySeverity)
	}
	if r.ProjectsScanned != 3 {
		t.Errorf("projects_scanned: %d", r.ProjectsScanned)
	}
}

func TestEvaluateAll_EmptyInput(t *testing.T) {
	r := EvaluateAll(nil, thresholds())
	if r.Total != 0 {
		t.Errorf("empty: total %d", r.Total)
	}
	if r.HasCritical {
		t.Error("empty should not have critical")
	}
}

// ─── ID stability ───────────────────────────────

func TestBuildID_StableForSameInputs(t *testing.T) {
	a := buildID("breeze", SourceVuln, "critical")
	b := buildID("breeze", SourceVuln, "critical")
	if a != b {
		t.Errorf("ID instability: %s vs %s", a, b)
	}
}

func TestBuildID_DifferQualifiers(t *testing.T) {
	if buildID("p", SourceVuln, "critical") == buildID("p", SourceVuln, "high") {
		t.Error("different qualifier should yield different ID")
	}
}

func TestBuildID_NoQualifierShorter(t *testing.T) {
	short := buildID("p", SourceCI, "")
	long := buildID("p", SourceCI, "x")
	if !strings.HasPrefix(long, short) {
		t.Errorf("short %q should prefix long %q", short, long)
	}
}

// ─── Summary ────────────────────────────────────

func TestReport_SummaryHealthy(t *testing.T) {
	r := EvaluateAll([]ProjectSnapshot{
		{Slug: "x", CIStatus: "passing", PlanIsHealthy: true, HasPlanMd: true},
	}, thresholds())
	if !strings.Contains(r.Summary(), "healthy") {
		t.Errorf("healthy summary: %s", r.Summary())
	}
}

func TestReport_SummaryWithCrit(t *testing.T) {
	r := EvaluateAll([]ProjectSnapshot{
		{Slug: "x", VulnCritical: 2},
	}, thresholds())
	s := r.Summary()
	if !strings.Contains(s, "CRIT=1") {
		t.Errorf("crit summary: %s", s)
	}
}

// ─── Ranking ────────────────────────────────────

func TestRankAlerts_SeverityFirst(t *testing.T) {
	as := []Alert{
		{Project: "z", Severity: SevLow, Source: SourceStale},
		{Project: "a", Severity: SevCritical, Source: SourceVuln},
		{Project: "m", Severity: SevHigh, Source: SourceCI},
	}
	rankAlerts(as)
	if as[0].Severity != SevCritical {
		t.Errorf("rank: %v", as)
	}
	if as[2].Severity != SevLow {
		t.Errorf("rank low: %v", as)
	}
}

// ─── helpers ───────────────────────────────────

func anyOfSource(as []Alert, src Source) bool {
	for _, a := range as {
		if a.Source == src {
			return true
		}
	}
	return false
}

// ─── nil NowFn defaults (covers Evaluate/EvaluateAll lines 155, 311) ──

func TestEvaluate_NilNowFnUsesWallClock(t *testing.T) {
	// Calling Evaluate without setting NowFn → covers the nil-guard branch.
	snap := ProjectSnapshot{Slug: "x", CIStatus: "passing", PlanIsHealthy: true, HasPlanMd: true}
	th := DefaultThresholds()
	th.NowFn = nil
	// Must not panic; returns empty slice for a healthy project.
	_ = Evaluate(snap, th)
}

func TestEvaluateAll_NilNowFnUsesWallClock(t *testing.T) {
	th := DefaultThresholds()
	th.NowFn = nil
	r := EvaluateAll(nil, th) // must not panic
	if r.ProjectsScanned != 0 {
		t.Errorf("empty input: %+v", r)
	}
}

// TestRankAlerts_SameSevertiyAndSource covers the project-name tiebreak in rankAlerts.
func TestRankAlerts_SameSevertiyAndSource(t *testing.T) {
	as := []Alert{
		{Project: "z-proj", Severity: SevHigh, Source: SourceVuln},
		{Project: "a-proj", Severity: SevHigh, Source: SourceVuln},
	}
	rankAlerts(as)
	if as[0].Project != "a-proj" || as[1].Project != "z-proj" {
		t.Errorf("project tiebreak: got %v %v", as[0].Project, as[1].Project)
	}
}

// TestReport_SummaryHighOnly covers the HIGH count in Summary (no CRIT).
func TestReport_SummaryHighOnly(t *testing.T) {
	r := EvaluateAll([]ProjectSnapshot{
		{Slug: "x", VulnHigh: 2},
	}, thresholds())
	s := r.Summary()
	if !strings.Contains(s, "HIGH=") {
		t.Errorf("high summary: %s", s)
	}
	if strings.Contains(s, "CRIT=") {
		t.Errorf("unexpected CRIT in high-only summary: %s", s)
	}
}

// 空のコレクションは JSON で `null` ではなく `[]` になること(v1.3.3)。
//
// なぜ重要か:
//
//	Go の nil スライスは `null` に marshal される。`alerts` が null だと、
//	client 側は `resp.alerts.length` で落ちるか、`!resp.alerts` で
//	**「アラート 0 件」と「アラートを計算していない」を区別できなくなる**。
//	これは v1.2.0 で起動時に直したのと同じ曖昧さ——「スキャンして 0 件」と
//	「スキャンしていない」が見分けられない——の応答側の再演である。
//	`[]` は「計算した。無かった」と言い切る。
func TestEvaluateAll_EmptyAlertsMarshalAsArrayNotNull(t *testing.T) {
	rep := EvaluateAll(nil, DefaultThresholds())
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(`"alerts":null`)) {
		t.Errorf("empty alerts marshalled as null — a client cannot tell 'none found' from "+
			"'not computed': %s", b)
	}
	if rep.Alerts == nil {
		t.Error("Alerts must be an empty slice, not nil")
	}
}

// フィルタも空配列を返すこと(v1.3.3)。
//
// EvaluateAll だけ直しても live tool は null を返し続けた——lifecycle filter と
// severity filter が Report を **作り直す** ため。不変条件は全ての生産点で守る。
func TestFilterAlerts_EmptyResultIsArrayNotNil(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "alerts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.FilterAlerts(nil); got == nil {
		t.Error("FilterAlerts must return an empty slice, not nil")
	}
	if got := s.FilterAlerts([]Alert{}); got == nil {
		t.Error("FilterAlerts on an empty input must return an empty slice, not nil")
	}
}
