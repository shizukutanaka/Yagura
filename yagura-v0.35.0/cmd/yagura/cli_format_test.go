// cli_format_test.go — tests for the human-readable CLI formatters. These were
// at 0% coverage; they are pure functions writing to an io.Writer, so each test
// renders into a bytes.Buffer and asserts on the emitted text.
package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/dashboard"
	"github.com/shizukutanaka/yagura/internal/harness"
	"github.com/shizukutanaka/yagura/internal/pindrift"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/sbom"
	"github.com/shizukutanaka/yagura/internal/secretscan"
)

func TestShortSHA(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"abc", "abc"},
		{"123456789012", "123456789012"},  // exactly 12 → unchanged
		{"1234567890123", "123456789012"}, // 13 → truncated to 12
		{"11bd71901bbe5b1630ceea73d27597364c", "11bd71901bbe"},
	}
	for _, c := range cases {
		if got := shortSHA(c.in); got != c.want {
			t.Errorf("shortSHA(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDash(t *testing.T) {
	if dash("") != "-" {
		t.Error("empty string should render as -")
	}
	if dash("go1.22") != "go1.22" {
		t.Error("non-empty string should pass through")
	}
}

func TestYesNo(t *testing.T) {
	if yesNo(true) != "yes" || yesNo(false) != "no" {
		t.Error("yesNo mapping wrong")
	}
}

func TestHumanStats(t *testing.T) {
	var b bytes.Buffer
	humanStats(&b, statsView{
		Total:            5,
		TotalOpenPRs:     2,
		TotalOpenIssues:  3,
		StaleActiveCount: 1,
		AvgPriority:      2.5,
		ByStage:          map[string]int{"active": 4, "archived": 1},
		ByLanguage:       map[string]int{"Go": 5},
	})
	out := b.String()
	for _, want := range []string{"total: 5", "open PRs: 2", "avg priority: 2.50", "by stage:", "active", "by language:", "Go"} {
		if !strings.Contains(out, want) {
			t.Errorf("humanStats output missing %q\n---\n%s", want, out)
		}
	}
}

func TestPrintCountMap_EmptyIsSilent(t *testing.T) {
	var b bytes.Buffer
	printCountMap(&b, "by stage", map[string]int{})
	if b.Len() != 0 {
		t.Errorf("empty map should print nothing, got %q", b.String())
	}
}

func TestPrintCountMap_SortedKeys(t *testing.T) {
	var b bytes.Buffer
	printCountMap(&b, "by lang", map[string]int{"zeta": 1, "alpha": 2})
	out := b.String()
	if strings.Index(out, "alpha") > strings.Index(out, "zeta") {
		t.Errorf("keys must be sorted (alpha before zeta): %q", out)
	}
}

func TestHumanSbom(t *testing.T) {
	var b bytes.Buffer
	bom := &sbom.Bom{
		SpecVersion: "1.5",
		Metadata:    sbom.Metadata{},
		Components: []sbom.Component{
			{Name: "stdlib", Version: "go1.22"},
		},
	}
	humanSbom(&b, bom)
	out := b.String()
	if !strings.Contains(out, "CycloneDX 1.5") {
		t.Errorf("expected spec line, got: %s", out)
	}
	if !strings.Contains(out, "components:") || !strings.Contains(out, "stdlib") {
		t.Errorf("expected component listing, got: %s", out)
	}
}

func TestHumanSbom_NoComponents(t *testing.T) {
	var b bytes.Buffer
	humanSbom(&b, &sbom.Bom{SpecVersion: "1.5"})
	// The summary line legitimately reads "components: 0"; what must be absent
	// is the standalone listing header line ("components:\n") and any rows.
	if strings.Contains(b.String(), "components:\n") {
		t.Errorf("no components should omit the components: listing header, got: %s", b.String())
	}
}

func TestHumanPinDrift(t *testing.T) {
	var b bytes.Buffer
	results := []pindrift.Result{
		{
			Pin:    pindrift.Pin{Owner: "actions", Repo: "checkout", PinnedSHA: "11bd71901bbe5b1630ceea73d27597364c9af683"},
			Status: pindrift.StatusOK,
			Detail: "ok",
		},
	}
	humanPinDrift(&b, results)
	out := b.String()
	for _, want := range []string{"total_pins: 1", "actions/checkout", "11bd71901bbe", "OK"} {
		if !strings.Contains(out, want) {
			t.Errorf("humanPinDrift output missing %q\n---\n%s", want, out)
		}
	}
	// SHA must be truncated, not full.
	if strings.Contains(out, "11bd71901bbe5b1630") {
		t.Errorf("SHA should be shortened in output: %s", out)
	}
}

func TestWorkflowShape(t *testing.T) {
	cases := []struct {
		r    harness.WorkflowAuditResult
		want string
	}{
		{harness.WorkflowAuditResult{}, "-"},
		{harness.WorkflowAuditResult{UsesParallel: true}, "parallel"},
		{harness.WorkflowAuditResult{UsesPipeline: true}, "pipeline"},
		{harness.WorkflowAuditResult{HasLoop: true}, "loop"},
		{harness.WorkflowAuditResult{UsesParallel: true, HasLoop: true}, "parallel+loop"},
		{harness.WorkflowAuditResult{UsesParallel: true, UsesPipeline: true, HasLoop: true}, "parallel+pipeline+loop"},
	}
	for _, c := range cases {
		if got := workflowShape(c.r); got != c.want {
			t.Errorf("workflowShape(%+v) = %q, want %q", c.r, got, c.want)
		}
	}
}

// ─── humanWorkflowAudit ──────────────────────────────────────

func TestHumanWorkflowAudit_Empty(t *testing.T) {
	var b bytes.Buffer
	humanWorkflowAudit(&b, nil, 3, 0)
	if !strings.Contains(b.String(), "scanned: 3") {
		t.Errorf("missing scanned count: %q", b.String())
	}
}

func TestHumanWorkflowAudit_WithEntries(t *testing.T) {
	var b bytes.Buffer
	entries := []workflowAuditEntry{
		{
			Path: "workflows/my.js",
			WorkflowAuditResult: harness.WorkflowAuditResult{
				Score: 80, AgentCalls: 2, UsesParallel: true,
				Issues: []string{"missing error handling"},
			},
		},
	}
	humanWorkflowAudit(&b, entries, 1, 1)
	out := b.String()
	if !strings.Contains(out, "workflows/my.js") {
		t.Errorf("missing path: %q", out)
	}
	if !strings.Contains(out, "missing error handling") {
		t.Errorf("missing issue: %q", out)
	}
}

// ─── humanSettingsAudit ──────────────────────────────────────

func TestHumanSettingsAudit_Empty(t *testing.T) {
	var b bytes.Buffer
	humanSettingsAudit(&b, nil, 2, 0)
	if !strings.Contains(b.String(), "scanned: 2") {
		t.Errorf("missing scanned count: %q", b.String())
	}
}

func TestHumanSettingsAudit_WithEntries(t *testing.T) {
	var b bytes.Buffer
	entries := []settingsAuditEntry{
		{
			Path: ".claude/settings.json",
			SettingsAuditResult: harness.SettingsAuditResult{
				Score: 70, HasDenyList: true, HasHooks: false,
				Issues: []string{"no deny for destructive ops"},
			},
		},
	}
	humanSettingsAudit(&b, entries, 1, 1)
	out := b.String()
	if !strings.Contains(out, "settings.json") {
		t.Errorf("missing path: %q", out)
	}
	if !strings.Contains(out, "no deny for destructive ops") {
		t.Errorf("missing issue: %q", out)
	}
}

// ─── sortedLabelCounts ───────────────────────────────────────

func TestSortedLabelCounts_Nil(t *testing.T) {
	if got := sortedLabelCounts(nil); got != nil {
		t.Errorf("nil map should return nil, got %v", got)
	}
}

func TestSortedLabelCounts_OrderedByCountDescThenLabel(t *testing.T) {
	m := map[string]int{"alpha": 3, "beta": 5, "gamma": 3}
	got := sortedLabelCounts(m)
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}
	if got[0].Label != "beta" || got[0].Count != 5 {
		t.Errorf("first should be beta:5, got %+v", got[0])
	}
	if got[1].Label != "alpha" {
		t.Errorf("tie-break: alpha before gamma, got %+v", got[1])
	}
}

// ─── writeJSONPretty ─────────────────────────────────────────

func TestWriteJSONPretty_Indented(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSONPretty(w, 200, map[string]string{"key": "value"})
	body := w.Body.String()
	if !strings.Contains(body, "\n") {
		t.Errorf("pretty print should add newlines, got %q", body)
	}
	if !strings.Contains(body, "key") {
		t.Errorf("missing key in pretty output: %q", body)
	}
}

// ─── severityRank ────────────────────────────────────────────

func TestSeverityRank_AllCases(t *testing.T) {
	cases := []struct {
		sev  secretscan.Severity
		want int
	}{
		{secretscan.SeverityCritical, 4},
		{secretscan.SeverityHigh, 3},
		{secretscan.SeverityMedium, 2},
		{secretscan.SeverityLow, 1},
		{"unknown", 0},
	}
	for _, tc := range cases {
		if got := severityRank(tc.sev); got != tc.want {
			t.Errorf("severityRank(%q) = %d, want %d", tc.sev, got, tc.want)
		}
	}
}

// ─── computeStats ────────────────────────────────────────────

func TestComputeStats_Empty(t *testing.T) {
	s := computeStats(nil, time.Now())
	if s.Total != 0 || s.AvgPriority != 0 {
		t.Errorf("empty projects should yield zero stats: %+v", s)
	}
}

func TestComputeStats_WithProjects(t *testing.T) {
	now := time.Now().UTC()
	staleTime := now.AddDate(0, 0, -20) // 20 days ago → stale active
	projects := []*project.Project{
		{
			Slug:           "stale",
			Stage:          project.StageActive,
			Language:       "go",
			Priority:       3,
			LatestActivity: staleTime,
		},
		{
			Slug:     "sprint-bearer",
			Stage:    project.StageActive,
			Priority: 2,
			Sprint:   &project.Sprint{Phase: project.PhaseBuild},
		},
		{
			Slug:  "archived",
			Stage: project.StageArchived,
		},
	}
	s := computeStats(projects, now)
	if s.Total != 3 {
		t.Errorf("Total = %d, want 3", s.Total)
	}
	if s.StaleActiveCount != 1 {
		t.Errorf("StaleActiveCount = %d, want 1", s.StaleActiveCount)
	}
	if s.WithActiveSprint != 1 {
		t.Errorf("WithActiveSprint = %d, want 1", s.WithActiveSprint)
	}
	if s.AvgPriority == 0 {
		t.Errorf("AvgPriority should be non-zero, got 0")
	}
	if s.ByStage["active"] != 2 || s.ByStage["archived"] != 1 {
		t.Errorf("ByStage = %v", s.ByStage)
	}
	if s.ByLanguage["go"] != 1 {
		t.Errorf("ByLanguage[go] = %d, want 1", s.ByLanguage["go"])
	}
}

// ─── humanProject with optional fields ──────────────────────

func TestHumanProject_OptionalFields(t *testing.T) {
	var b bytes.Buffer
	now := time.Now().UTC()
	p := &project.Project{
		Slug:           "demo",
		DisplayName:    "Demo",
		Repository:     "o/demo",
		LocalPath:      "/tmp/demo",
		Language:       "rust",
		Stage:          project.StageActive,
		Priority:       2,
		Tags:           []string{"a", "b"},
		DependsOn:      []string{"other"},
		CIStatus:       "green",
		LatestVersion:  "v1.0.0",
		Notes:          "some notes",
		LatestActivity: now.AddDate(0, 0, -1),
		CreatedAt:      now.AddDate(0, -1, 0),
		UpdatedAt:      now,
	}
	humanProject(&b, p)
	out := b.String()
	for _, want := range []string{"demo", "Demo", "o/demo", "/tmp/demo", "rust", "active", "a, b", "other", "green", "v1.0.0", "some notes"} {
		if !strings.Contains(out, want) {
			t.Errorf("humanProject missing %q in:\n%s", want, out)
		}
	}
}

// ─── dashboard.LabelCount (used by sortedLabelCounts) ────────

var _ = dashboard.LabelCount{} // compile-time reference keeps import
