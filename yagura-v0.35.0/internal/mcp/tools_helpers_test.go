package mcp

import (
	"encoding/json"
	"testing"

	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/plantracker"
	"github.com/shizukutanaka/yagura/internal/project"
)

// ─── extractSection ──────────────────────────────────────────

func TestExtractSection(t *testing.T) {
	content := `# Plan
## Purpose
Build a thing.
Second line.

## Scope
In: A, B
Out: C
## Empty
## Next
done`
	cases := []struct {
		name    string
		headers []string
		want    string
	}{
		{"purpose multi-line", []string{"Purpose"}, "Build a thing.\nSecond line."},
		{"scope", []string{"Scope"}, "In: A, B\nOut: C"},
		{"case-insensitive", []string{"purpose"}, "Build a thing.\nSecond line."},
		{"first match wins among headers", []string{"Purpose", "Scope"}, "Build a thing.\nSecond line."},
		{"skips empty body, falls to next header in list", []string{"Empty", "Scope"}, "In: A, B\nOut: C"},
		{"missing header", []string{"Nonexistent"}, ""},
		{"empty headers", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractSection(content, tc.headers); got != tc.want {
				t.Errorf("extractSection(%v) = %q, want %q", tc.headers, got, tc.want)
			}
		})
	}
}

func TestExtractSection_EmptyContent(t *testing.T) {
	if got := extractSection("", []string{"Purpose"}); got != "" {
		t.Errorf("empty content should yield empty, got %q", got)
	}
}

// ─── extractDoDItems ─────────────────────────────────────────

func TestExtractDoDItems(t *testing.T) {
	content := `## 完了定義
- [ ] tests pass
- [x] docs written
- plain bullet
* star bullet
  not a bullet
## Other
- ignored`
	got := extractDoDItems(content)
	want := []string{"tests pass", "docs written", "plain bullet", "star bullet"}
	if len(got) != len(want) {
		t.Fatalf("got %d items %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtractDoDItems_EnglishHeader(t *testing.T) {
	content := "## Definition of Done\n- [ ] ship it\n"
	got := extractDoDItems(content)
	if len(got) != 1 || got[0] != "ship it" {
		t.Errorf("got %v, want [ship it]", got)
	}
}

func TestExtractDoDItems_NoSection(t *testing.T) {
	if got := extractDoDItems("## Scope\n- x\n"); got != nil {
		t.Errorf("no DoD section should yield nil, got %v", got)
	}
}

// ─── filterBySeverity ────────────────────────────────────────

func makeReport() alertfix.Report {
	return alertfix.Report{
		Alerts: []alertfix.Alert{
			{ID: "c", Severity: alertfix.SevCritical},
			{ID: "h", Severity: alertfix.SevHigh},
			{ID: "m", Severity: alertfix.SevMedium},
			{ID: "l", Severity: alertfix.SevLow},
		},
		Total: 4,
	}
}

func TestFilterBySeverity(t *testing.T) {
	cases := []struct {
		min     string
		wantIDs []string
		wantTot int
	}{
		{"critical", []string{"c"}, 1},
		{"high", []string{"c", "h"}, 2},
		{"medium", []string{"c", "h", "m"}, 3},
		{"low", []string{"c", "h", "m", "l"}, 4},
		{"HIGH", []string{"c", "h"}, 2}, // case-insensitive
	}
	for _, tc := range cases {
		t.Run(tc.min, func(t *testing.T) {
			r := filterBySeverity(makeReport(), tc.min)
			if r.Total != tc.wantTot {
				t.Errorf("Total = %d, want %d", r.Total, tc.wantTot)
			}
			if len(r.Alerts) != len(tc.wantIDs) {
				t.Fatalf("got %d alerts, want %d", len(r.Alerts), len(tc.wantIDs))
			}
			for i, id := range tc.wantIDs {
				if r.Alerts[i].ID != id {
					t.Errorf("alert %d = %q, want %q", i, r.Alerts[i].ID, id)
				}
			}
		})
	}
}

func TestFilterBySeverity_UnknownMinReturnsUnchanged(t *testing.T) {
	r := filterBySeverity(makeReport(), "bogus")
	if r.Total != 4 || len(r.Alerts) != 4 {
		t.Errorf("unknown min should pass through unchanged, got total=%d", r.Total)
	}
}

// ─── projectToSnapshot ───────────────────────────────────────

func TestProjectToSnapshot(t *testing.T) {
	p := project.Project{
		Slug:           "breeze",
		Repository:     "o/breeze",
		VulnCritical:   2,
		VulnHigh:       3,
		CIStatus:       project.CIStatus("failing"),
		ScorecardScore: 7.5,
		OpenIssues:     4,
		OpenPRs:        1,
		RepoPublic:     true,
		Tags:           []string{"internal"},
	}
	snap := ProjectToSnapshot(p)
	if snap.Slug != "breeze" || snap.Repository != "o/breeze" {
		t.Errorf("identity fields not carried: %+v", snap)
	}
	if !snap.RepoPublic {
		t.Error("RepoPublic not carried into snapshot")
	}
	if len(snap.Tags) != 1 || snap.Tags[0] != "internal" {
		t.Errorf("Tags not carried: %v", snap.Tags)
	}
	if snap.VulnCritical != 2 || snap.VulnHigh != 3 {
		t.Errorf("vuln fields wrong: %+v", snap)
	}
	if snap.CIStatus != "failing" {
		t.Errorf("CIStatus = %q, want failing", snap.CIStatus)
	}
	if snap.ScorecardScore != 7.5 {
		t.Errorf("ScorecardScore = %v, want 7.5", snap.ScorecardScore)
	}
	if snap.OpenIssues != 4 || snap.OpenPRs != 1 {
		t.Errorf("open counts wrong: %+v", snap)
	}
}

// ─── pickReason ──────────────────────────────────────────────

func TestPickReason(t *testing.T) {
	healthy := plantracker.PlanState{IsHealthy: true, ProgressPct: 100}
	unhealthy := plantracker.PlanState{IsHealthy: false, ProgressPct: 50}
	partial := plantracker.PlanState{IsHealthy: true, ProgressPct: 60}

	cases := []struct {
		name       string
		plan       plantracker.PlanState
		ci         string
		openCrit   int
		aiCritical bool
		aiRisk     int
		want       string
	}{
		{"ai critical beats all", unhealthy, "failing", 5, true, 9, "AI-generated critical risk (review required)"},
		{"open crit", healthy, "passing", 3, false, 0, "3 critical issues blocking"},
		{"ci failing", healthy, "failing", 0, false, 0, "CI failing"},
		{"ci failing case-insensitive", healthy, "FAILING", 0, false, 0, "CI failing"},
		{"unhealthy plan", unhealthy, "passing", 0, false, 0, "Plan.md missing required sections"},
		{"partial progress", partial, "passing", 0, false, 0, "plan 40% remaining"},
		{"ready", healthy, "passing", 0, false, 0, "ready to release"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickReason(tc.plan, tc.ci, tc.openCrit, tc.aiCritical, tc.aiRisk)
			if got != tc.want {
				t.Errorf("pickReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── planStateToFeatureInput ─────────────────────────────────

func TestPlanStateToFeatureInput(t *testing.T) {
	content := `# Plan
## Purpose
x
## Phase 1
- [x] design
- [ ] build
## フェーズ 2
- [ ] ship
## Notes
- [ ] not a phase task
## 完了定義
- [ ] all green`
	state := plantracker.PlanState{
		Phases: []plantracker.Phase{
			{Name: "Purpose", LineStart: 2},
			{Name: "Phase 1", LineStart: 4},
			{Name: "フェーズ 2", LineStart: 7},
			{Name: "Notes", LineStart: 9},
			{Name: "完了定義", LineStart: 11},
		},
	}
	pin := planStateToFeatureInput("breeze", content, state)

	if pin.Project != "breeze" {
		t.Errorf("Project = %q", pin.Project)
	}
	// Only the two phase-named sections become feature phases.
	if len(pin.Phases) != 2 {
		t.Fatalf("got %d phases, want 2: %+v", len(pin.Phases), pin.Phases)
	}
	if pin.Phases[0].Name != "Phase 1" || len(pin.Phases[0].Tasks) != 2 {
		t.Errorf("phase 1 wrong: %+v", pin.Phases[0])
	}
	if !pin.Phases[0].Tasks[0].Done || pin.Phases[0].Tasks[1].Done {
		t.Errorf("task done flags wrong: %+v", pin.Phases[0].Tasks)
	}
	if pin.Phases[1].Name != "フェーズ 2" || len(pin.Phases[1].Tasks) != 1 {
		t.Errorf("phase 2 wrong: %+v", pin.Phases[1])
	}
	// DoD pulled from 完了定義 section.
	if len(pin.DoD) != 1 || pin.DoD[0] != "all green" {
		t.Errorf("DoD = %v, want [all green]", pin.DoD)
	}
}

func TestPlanStateToFeatureInput_NoPhases(t *testing.T) {
	content := "## Purpose\nx\n"
	state := plantracker.PlanState{
		Phases: []plantracker.Phase{{Name: "Purpose", LineStart: 1}},
	}
	pin := planStateToFeatureInput("p", content, state)
	if len(pin.Phases) != 0 {
		t.Errorf("expected no feature phases, got %+v", pin.Phases)
	}
}

// ─── ToolStats averages ──────────────────────────────────────

func TestToolStatsAverages(t *testing.T) {
	s := &ToolStats{Calls: 4, RequestBytes: 400, ResponseBytes: 1000}
	if got := s.AvgReqBytes(); got != 100 {
		t.Errorf("AvgReqBytes = %v, want 100", got)
	}
	if got := s.AvgRespBytes(); got != 250 {
		t.Errorf("AvgRespBytes = %v, want 250", got)
	}
}

func TestToolStatsAverages_ZeroCalls(t *testing.T) {
	s := &ToolStats{Calls: 0, RequestBytes: 99, ResponseBytes: 99}
	if s.AvgReqBytes() != 0 || s.AvgRespBytes() != 0 {
		t.Errorf("zero calls must yield 0 averages, got req=%v resp=%v",
			s.AvgReqBytes(), s.AvgRespBytes())
	}
}

// ─── SetVersion / version ─────────────────────────────────────

func TestSetVersion_RoundTrip(t *testing.T) {
	orig := serverVersion
	t.Cleanup(func() { serverVersion = orig })

	SetVersion("v9.9.9-test")
	if got := version(); got != "v9.9.9-test" {
		t.Errorf("version() = %q, want v9.9.9-test", got)
	}
}

func TestSetVersion_Empty(t *testing.T) {
	orig := serverVersion
	t.Cleanup(func() { serverVersion = orig })

	SetVersion("")
	if got := version(); got != "" {
		t.Errorf("version() = %q, want empty", got)
	}
}

// ─── parseLimitArg ────────────────────────────────────────────

func TestParseLimitArg_Empty(t *testing.T) {
	n, err := parseLimitArg(nil)
	if err != nil || n != 0 {
		t.Errorf("empty args: got (%d, %v), want (0, nil)", n, err)
	}
}

func TestParseLimitArg_ZeroLength(t *testing.T) {
	n, err := parseLimitArg(json.RawMessage{})
	if err != nil || n != 0 {
		t.Errorf("zero-length: got (%d, %v), want (0, nil)", n, err)
	}
}

func TestParseLimitArg_Positive(t *testing.T) {
	n, err := parseLimitArg(json.RawMessage(`{"limit":5}`))
	if err != nil || n != 5 {
		t.Errorf("positive limit: got (%d, %v), want (5, nil)", n, err)
	}
}

func TestParseLimitArg_Negative_ReturnsZero(t *testing.T) {
	n, err := parseLimitArg(json.RawMessage(`{"limit":-3}`))
	if err != nil || n != 0 {
		t.Errorf("negative limit: got (%d, %v), want (0, nil)", n, err)
	}
}

func TestParseLimitArg_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := parseLimitArg(json.RawMessage(`not-json`))
	if err == nil {
		t.Error("invalid JSON should return error")
	}
}
