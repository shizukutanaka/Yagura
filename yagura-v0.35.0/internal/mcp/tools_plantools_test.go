package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/dedupe"
	"github.com/shizukutanaka/yagura/internal/plantracker"
	"github.com/shizukutanaka/yagura/internal/project"
)

// callErr invokes a tool and returns (result, error) without fataling.
func callErr(t *testing.T, tool *Tool, args any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	return tool.Handler(context.Background(), b)
}

// newAlertFixStore creates a real alertfix store backed by a temp file.
func newAlertFixStore(t *testing.T) *alertfix.Store {
	t.Helper()
	st, err := alertfix.NewStore(filepath.Join(t.TempDir(), "alerts.jsonl"))
	if err != nil {
		t.Fatalf("alertfix.NewStore: %v", err)
	}
	return st
}

// writePlanMd creates a minimal Plan.md in dir.
func writePlanMd(t *testing.T, dir string) {
	t.Helper()
	content := `# Plan
## Purpose
Test project purpose.
## Scope
In: everything
## Phase 1
- [x] design
- [x] prototype
## フェーズ 2
- [ ] build
- [ ] ship
## 完了定義
- [ ] all tests green
`
	if err := os.WriteFile(filepath.Join(dir, "Plan.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("writePlanMd: %v", err)
	}
}

// ─── yagura_harness_coverage ──────────────────────────────────

func TestHarnessCoverage_ReturnsMatrix(t *testing.T) {
	d := newDeps(t)
	tool := buildHarnessCoverageTool(d)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	matrix, ok := r["matrix"].(map[string]map[string][]string)
	if !ok {
		t.Fatalf("matrix missing or wrong type: %T", r["matrix"])
	}
	if _, hasGuide := matrix["guide"]; !hasGuide {
		t.Error("matrix should have 'guide' axis")
	}
	if _, hasSensor := matrix["sensor"]; !hasSensor {
		t.Error("matrix should have 'sensor' axis")
	}
	counts, ok := r["counts"].(map[string]int)
	if !ok {
		t.Fatalf("counts missing or wrong type: %T", r["counts"])
	}
	if counts["guide_computational"] == 0 {
		t.Error("guide_computational count should be > 0")
	}
}

func TestHarnessCoverage_JSONMarshalable(t *testing.T) {
	d := newDeps(t)
	tool := buildHarnessCoverageTool(d)
	r := mustCall(t, tool, struct{}{})
	if _, err := json.Marshal(r); err != nil {
		t.Errorf("result must be JSON-marshalable: %v", err)
	}
}

// ─── yagura_test_audit ───────────────────────────────────────

func TestTestAudit_Basic(t *testing.T) {
	d := newDeps(t)
	tool := buildTestAuditTool(d)
	files := map[string]string{
		"pkg/foo.go":      "package pkg\nfunc Foo() {}",
		"pkg/foo_test.go": "package pkg\nimport \"testing\"\nfunc TestFoo(t *testing.T) {}",
		"pkg/bar.go":      "package pkg\nfunc Bar() {}",
	}
	r := mustCall(t, tool, map[string]any{"files": files}).(map[string]any)
	if r["source_files"].(int) < 2 {
		t.Errorf("source_files = %v, want >= 2", r["source_files"])
	}
	if r["test_files"].(int) < 1 {
		t.Errorf("test_files = %v, want >= 1", r["test_files"])
	}
}

func TestTestAudit_EmptyFilesError(t *testing.T) {
	d := newDeps(t)
	tool := buildTestAuditTool(d)
	_, err := callErr(t, tool, map[string]any{"files": map[string]string{}})
	if err == nil {
		t.Error("empty files should return an error")
	}
}

func TestTestAudit_MissingFilesError(t *testing.T) {
	d := newDeps(t)
	tool := buildTestAuditTool(d)
	_, err := callErr(t, tool, struct{}{})
	if err == nil {
		t.Error("missing files field should return an error")
	}
}

func TestTestAudit_UntestedOnlyFlag(t *testing.T) {
	d := newDeps(t)
	tool := buildTestAuditTool(d)
	files := map[string]string{
		"a.go": "package a",
		"b.go": "package b",
	}
	r := mustCall(t, tool, map[string]any{"files": files, "untested_only": true}).(map[string]any)
	// untested_only filters result; coverage_ratio should be accessible
	if _, ok := r["coverage_ratio"]; !ok {
		t.Error("coverage_ratio field missing")
	}
}

// ─── yagura_feature_list ─────────────────────────────────────

func TestFeatureList_Basic(t *testing.T) {
	dir := t.TempDir()
	writePlanMd(t, dir)
	d := newDeps(t)
	p := sampleProject("demo", func(p *project.Project) { p.LocalPath = dir })
	_ = d.Registry.Add(p)

	tool := buildFeatureListTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "demo"}).(map[string]any)
	if r["slug"] != "demo" {
		t.Errorf("slug = %q, want demo", r["slug"])
	}
	if r["feature_list"] == nil {
		t.Error("feature_list should not be nil")
	}
	stats, ok := r["stats"]
	if !ok || stats == nil {
		t.Error("stats field missing")
	}
}

func TestFeatureList_Write(t *testing.T) {
	dir := t.TempDir()
	writePlanMd(t, dir)
	d := newDeps(t)
	p := sampleProject("writeproj", func(p *project.Project) { p.LocalPath = dir })
	_ = d.Registry.Add(p)

	tool := buildFeatureListTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "writeproj", "write": true}).(map[string]any)
	if r["written_to"] == nil {
		t.Error("written_to should be set when write=true")
	}
	if _, err := os.Stat(filepath.Join(dir, "feature-list.json")); err != nil {
		t.Errorf("feature-list.json not created: %v", err)
	}
}

func TestFeatureList_UnknownSlug(t *testing.T) {
	d := newDeps(t)
	tool := buildFeatureListTool(d)
	_, err := callErr(t, tool, map[string]any{"slug": "nope"})
	if err == nil {
		t.Error("unknown slug should return error")
	}
}

func TestFeatureList_NoLocalPath(t *testing.T) {
	d := newDeps(t)
	p := sampleProject("nolp")
	_ = d.Registry.Add(p)
	tool := buildFeatureListTool(d)
	_, err := callErr(t, tool, map[string]any{"slug": "nolp"})
	if err == nil {
		t.Error("missing local_path should return error")
	}
}

func TestFeatureList_NoPlanMd(t *testing.T) {
	dir := t.TempDir() // empty dir — no Plan.md
	d := newDeps(t)
	p := sampleProject("noplan", func(p *project.Project) { p.LocalPath = dir })
	_ = d.Registry.Add(p)
	tool := buildFeatureListTool(d)
	_, err := callErr(t, tool, map[string]any{"slug": "noplan"})
	if err == nil {
		t.Error("missing Plan.md should return error")
	}
}

func TestFeatureList_EmptySlug(t *testing.T) {
	d := newDeps(t)
	tool := buildFeatureListTool(d)
	_, err := callErr(t, tool, map[string]any{"slug": ""})
	if err == nil {
		t.Error("empty slug should return error")
	}
}

// ─── yagura_agents_md ────────────────────────────────────────

func TestAgentsMd_Basic(t *testing.T) {
	d := newDeps(t)
	p := sampleProject("myproj", func(p *project.Project) { p.Language = "Go" })
	_ = d.Registry.Add(p)

	tool := buildAgentsMdTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "myproj"}).(map[string]any)
	if r["slug"] != "myproj" {
		t.Errorf("slug = %q, want myproj", r["slug"])
	}
	body, _ := r["body"].(string)
	if body == "" {
		t.Error("body should not be empty")
	}
	if r["filename"] != "AGENTS.md" {
		t.Errorf("filename = %q, want AGENTS.md", r["filename"])
	}
}

func TestAgentsMd_WithPlanMd(t *testing.T) {
	dir := t.TempDir()
	writePlanMd(t, dir)
	d := newDeps(t)
	p := sampleProject("withplan", func(p *project.Project) { p.LocalPath = dir })
	_ = d.Registry.Add(p)

	tool := buildAgentsMdTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "withplan"}).(map[string]any)
	body, _ := r["body"].(string)
	if body == "" {
		t.Error("body should not be empty when Plan.md exists")
	}
}

func TestAgentsMd_Write(t *testing.T) {
	dir := t.TempDir()
	d := newDeps(t)
	p := sampleProject("writeagent", func(p *project.Project) { p.LocalPath = dir })
	_ = d.Registry.Add(p)

	tool := buildAgentsMdTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "writeagent", "write": true}).(map[string]any)
	if r["written_to"] == nil {
		t.Error("written_to should be set when write=true")
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Errorf("AGENTS.md not created: %v", err)
	}
}

func TestAgentsMd_UnknownSlug(t *testing.T) {
	d := newDeps(t)
	tool := buildAgentsMdTool(d)
	_, err := callErr(t, tool, map[string]any{"slug": "ghost"})
	if err == nil {
		t.Error("unknown slug should return error")
	}
}

func TestAgentsMd_EmptySlug(t *testing.T) {
	d := newDeps(t)
	tool := buildAgentsMdTool(d)
	_, err := callErr(t, tool, map[string]any{"slug": ""})
	if err == nil {
		t.Error("empty slug should return error")
	}
}

// ─── yagura_init_sh ──────────────────────────────────────────

func TestInitSh_Posix(t *testing.T) {
	d := newDeps(t)
	p := sampleProject("goapp", func(p *project.Project) { p.Language = "Go" })
	_ = d.Registry.Add(p)

	tool := buildInitShTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "goapp"}).(map[string]any)
	if r["filename"] != "init.sh" {
		t.Errorf("filename = %q, want init.sh", r["filename"])
	}
	body, _ := r["body"].(string)
	if !strings.Contains(body, "go") && !strings.Contains(body, "#!/") {
		t.Error("posix init script should reference go or have shebang")
	}
}

func TestInitSh_PowerShell(t *testing.T) {
	d := newDeps(t)
	p := sampleProject("winapp", func(p *project.Project) { p.Language = "python" })
	_ = d.Registry.Add(p)

	tool := buildInitShTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "winapp", "target": "powershell"}).(map[string]any)
	if r["filename"] != "init.ps1" {
		t.Errorf("filename = %q, want init.ps1", r["filename"])
	}
}

func TestInitSh_Write(t *testing.T) {
	dir := t.TempDir()
	d := newDeps(t)
	p := sampleProject("writeinit", func(p *project.Project) {
		p.Language = "rust"
		p.LocalPath = dir
	})
	_ = d.Registry.Add(p)

	tool := buildInitShTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "writeinit", "write": true}).(map[string]any)
	if r["written_to"] == nil {
		t.Error("written_to should be set when write=true and local_path set")
	}
	if _, err := os.Stat(filepath.Join(dir, "init.sh")); err != nil {
		t.Errorf("init.sh not created: %v", err)
	}
}

func TestInitSh_UnknownTarget(t *testing.T) {
	d := newDeps(t)
	p := sampleProject("tp")
	_ = d.Registry.Add(p)
	tool := buildInitShTool(d)
	_, err := callErr(t, tool, map[string]any{"slug": "tp", "target": "zsh-please"})
	if err == nil {
		t.Error("unknown target should return error")
	}
}

func TestInitSh_UnknownSlug(t *testing.T) {
	d := newDeps(t)
	tool := buildInitShTool(d)
	_, err := callErr(t, tool, map[string]any{"slug": "ghost"})
	if err == nil {
		t.Error("unknown slug should return error")
	}
}

func TestInitSh_EmptySlug(t *testing.T) {
	d := newDeps(t)
	tool := buildInitShTool(d)
	_, err := callErr(t, tool, map[string]any{"slug": ""})
	if err == nil {
		t.Error("empty slug should return error")
	}
}

// ─── yagura_alert_fix ────────────────────────────────────────

func TestAlertFix_EmptyRegistry(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	store := newAlertFixStore(t)
	tool := buildAlertFixTool(d, cache, store)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	if r["total"].(int) != 0 {
		t.Errorf("empty registry should have 0 alerts, got %v", r["total"])
	}
	if r["projects_scanned"].(int) != 0 {
		t.Errorf("empty registry: projects_scanned = %v, want 0", r["projects_scanned"])
	}
}

func TestAlertFix_VulnerableProject(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	store := newAlertFixStore(t)
	p := sampleProject("vulnproj", func(p *project.Project) {
		p.VulnCritical = 5
		p.CIStatus = project.CIStatus("failing")
	})
	_ = d.Registry.Add(p)

	tool := buildAlertFixTool(d, cache, store)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	if r["total"].(int) == 0 {
		t.Error("vulnerable project should produce alerts")
	}
	if !r["has_critical"].(bool) {
		t.Error("critical vuln should set has_critical=true")
	}
}

func TestAlertFix_SlugFilter(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	store := newAlertFixStore(t)
	for _, slug := range []string{"a", "b"} {
		p := sampleProject(slug, func(p *project.Project) {
			p.VulnCritical = 1
		})
		_ = d.Registry.Add(p)
	}

	tool := buildAlertFixTool(d, cache, store)
	r := mustCall(t, tool, map[string]any{"slug": "a"}).(map[string]any)
	if r["projects_scanned"].(int) != 1 {
		t.Errorf("slug filter: projects_scanned = %v, want 1", r["projects_scanned"])
	}
}

func TestAlertFix_SlugNotFound(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	store := newAlertFixStore(t)
	tool := buildAlertFixTool(d, cache, store)
	_, err := callErr(t, tool, map[string]any{"slug": "ghost"})
	if err == nil {
		t.Error("unknown slug should return error")
	}
}

func TestAlertFix_SeverityMinFilter(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	store := newAlertFixStore(t)
	p := sampleProject("sp", func(p *project.Project) {
		p.VulnCritical = 1
		p.VulnHigh = 2
	})
	_ = d.Registry.Add(p)

	tool := buildAlertFixTool(d, cache, store)
	rAll := mustCall(t, tool, struct{}{}).(map[string]any)
	rCrit := mustCall(t, tool, map[string]any{"severity_min": "critical"}).(map[string]any)
	if rCrit["total"].(int) > rAll["total"].(int) {
		t.Error("critical filter should not return more alerts than unfiltered")
	}
}

func TestAlertFix_NilStore(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	tool := buildAlertFixTool(d, cache, nil)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	if _, hasLifecycle := r["lifecycle_stats"]; hasLifecycle {
		t.Error("lifecycle_stats should not appear when store is nil")
	}
}

func TestAlertFix_CustomThresholds(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	store := newAlertFixStore(t)
	p := sampleProject("th", func(p *project.Project) {
		p.OpenIssues = 25
		p.ScorecardScore = 4.0
	})
	_ = d.Registry.Add(p)

	tool := buildAlertFixTool(d, cache, store)
	// All three overrides exercised in one call: stale_days, scorecard_min,
	// open_issues_high. With open_issues_high=20, 25 issues should alert; with
	// the default (50) it would not.
	r := mustCall(t, tool, map[string]any{
		"stale_days":       7,
		"scorecard_min":    5.0,
		"open_issues_high": 20,
	}).(map[string]any)
	if r["total"].(int) == 0 {
		t.Error("lowered thresholds should produce alerts for 25 issues / scorecard 4.0")
	}
}

func TestAlertFix_PlanMdPath(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	store := newAlertFixStore(t)
	dir := t.TempDir()
	// A Plan.md missing required sections → PlanIsHealthy=false → plan alert.
	if err := os.WriteFile(filepath.Join(dir, "Plan.md"), []byte("# just a title\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := sampleProject("planned", func(p *project.Project) {
		p.LocalPath = dir
	})
	_ = d.Registry.Add(p)

	tool := buildAlertFixTool(d, cache, store)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	bySource := r["by_source"].(map[alertfix.Source]int)
	if bySource[alertfix.SourcePlan] == 0 {
		t.Errorf("unhealthy Plan.md should produce a plan-source alert, got %v", bySource)
	}
}

func TestAlertFix_FilteredInactive(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	store := newAlertFixStore(t)
	p := sampleProject("res", func(p *project.Project) {
		p.VulnCritical = 1
	})
	_ = d.Registry.Add(p)

	tool := buildAlertFixTool(d, cache, store)
	// First sweep: capture an alert ID.
	r1 := mustCall(t, tool, struct{}{}).(map[string]any)
	alerts := r1["alerts"].([]alertfix.Alert)
	if len(alerts) == 0 {
		t.Fatal("expected at least one alert to resolve")
	}
	if err := store.Resolve(alerts[0].ID, "fixed in test"); err != nil {
		t.Fatal(err)
	}
	// Second sweep: the resolved alert is filtered → filtered_inactive appears.
	r2 := mustCall(t, tool, struct{}{}).(map[string]any)
	fi, ok := r2["filtered_inactive"]
	if !ok {
		t.Fatal("filtered_inactive should be present after resolving an alert")
	}
	if fi.(int) < 1 {
		t.Errorf("filtered_inactive = %v, want >= 1", fi)
	}
	// include_inactive=true skips the filter → no filtered_inactive key.
	r3 := mustCall(t, tool, map[string]any{"include_inactive": true}).(map[string]any)
	if _, has := r3["filtered_inactive"]; has {
		t.Error("include_inactive=true should bypass the lifecycle filter")
	}
}

// ─── yagura_release_radar ────────────────────────────────────

func TestReleaseRadar_EmptyRegistry(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	tool := buildReleaseRadarTool(d, cache)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	if r["total_projects"].(int) != 0 {
		t.Errorf("empty registry: total_projects = %v, want 0", r["total_projects"])
	}
}

func TestReleaseRadar_SkipsNoLocalPath(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	// Projects without local_path are skipped (no Plan.md to parse).
	for _, s := range []string{"a", "b", "c"} {
		_ = d.Registry.Add(sampleProject(s))
	}
	tool := buildReleaseRadarTool(d, cache)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	if r["total_projects"].(int) != 3 {
		t.Errorf("total_projects = %v, want 3", r["total_projects"])
	}
	if r["projects_scored"].(int) != 0 {
		t.Errorf("projects without local_path should not be scored, got %v", r["projects_scored"])
	}
}

func TestReleaseRadar_WithPlanMd(t *testing.T) {
	dir := t.TempDir()
	writePlanMd(t, dir)
	d := newDeps(t)
	p := sampleProject("ready", func(p *project.Project) {
		p.LocalPath = dir
		p.CIStatus = project.CIStatus("passing")
	})
	_ = d.Registry.Add(p)

	cache := dedupe.New(0, 0)
	tool := buildReleaseRadarTool(d, cache)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	if r["projects_scored"].(int) != 1 {
		t.Errorf("project with Plan.md should be scored, got %v", r["projects_scored"])
	}
	ranked, _ := r["ranked"].([]plantracker.RankedProject)
	if len(ranked) != 1 {
		t.Errorf("ranked len = %d, want 1", len(ranked))
	}
}

func TestReleaseRadar_LimitApplied(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	for i := 0; i < 5; i++ {
		dir := t.TempDir()
		writePlanMd(t, dir)
		slug := []string{"aa", "bb", "cc", "dd", "ee"}[i]
		p := sampleProject(slug, func(p *project.Project) { p.LocalPath = dir })
		_ = d.Registry.Add(p)
	}
	tool := buildReleaseRadarTool(d, cache)
	r := mustCall(t, tool, map[string]any{"limit": 2}).(map[string]any)
	ranked, _ := r["ranked"].([]plantracker.RankedProject)
	if len(ranked) > 2 {
		t.Errorf("limit=2 should cap results, got %d", len(ranked))
	}
}

func TestReleaseRadar_DefaultLimit(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	// limit=0 should default to 10 (doesn't panic on empty)
	tool := buildReleaseRadarTool(d, cache)
	r := mustCall(t, tool, map[string]any{"limit": 0}).(map[string]any)
	if r["total_projects"] == nil {
		t.Error("total_projects should be present even with limit=0 (defaults to 10)")
	}
}

// ─── yagura_plan_status ──────────────────────────────────────

func TestPlanStatus_EmptySlug(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	tool := buildPlanStatusTool(d, cache)
	_, err := callErr(t, tool, map[string]any{"slug": ""})
	if err == nil {
		t.Error("empty slug should return error")
	}
}

func TestPlanStatus_UnknownSlug(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	tool := buildPlanStatusTool(d, cache)
	_, err := callErr(t, tool, map[string]any{"slug": "ghost"})
	if err == nil {
		t.Error("unknown slug should return error")
	}
}

func TestPlanStatus_NoLocalPath_ReturnsError(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	p := sampleProject("nolp")
	_ = d.Registry.Add(p)
	tool := buildPlanStatusTool(d, cache)
	// no local_path → returns map with "error" key, not a Go error
	r := mustCall(t, tool, map[string]any{"slug": "nolp"}).(map[string]any)
	if r["error"] == nil || r["error"] == "" {
		t.Errorf("no local_path should set error field: %+v", r)
	}
}

func TestPlanStatus_NoPlanMd_ReturnsError(t *testing.T) {
	dir := t.TempDir() // empty dir, no Plan.md
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	p := sampleProject("noplan", func(p *project.Project) { p.LocalPath = dir })
	_ = d.Registry.Add(p)
	tool := buildPlanStatusTool(d, cache)
	r := mustCall(t, tool, map[string]any{"slug": "noplan"}).(map[string]any)
	if r["error"] == nil || r["error"] == "" {
		t.Errorf("missing Plan.md should set error field: %+v", r)
	}
}

func TestPlanStatus_WithPlanMd(t *testing.T) {
	dir := t.TempDir()
	writePlanMd(t, dir)
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	p := sampleProject("planproj", func(p *project.Project) { p.LocalPath = dir })
	_ = d.Registry.Add(p)
	tool := buildPlanStatusTool(d, cache)
	r := mustCall(t, tool, map[string]any{"slug": "planproj"}).(map[string]any)
	if r["slug"] != "planproj" {
		t.Errorf("slug = %v, want planproj", r["slug"])
	}
	if r["plan_md"] == nil || r["plan_md"] == "" {
		t.Error("plan_md path should be set")
	}
	if r["state"] == nil {
		t.Error("state should be present")
	}
	if r["summary"] == nil {
		t.Error("summary should be present")
	}
}

func TestPlanStatus_InvalidJSON(t *testing.T) {
	d := newDeps(t)
	cache := dedupe.New(0, 0)
	tool := buildPlanStatusTool(d, cache)
	b := []byte(`not json`)
	_, err := tool.Handler(nil, b)
	if err == nil {
		t.Error("invalid JSON args should return error")
	}
}

// ─── yagura_progress_file ────────────────────────────────────

func TestProgressFile_EmptySlug(t *testing.T) {
	d := newDeps(t)
	s := newServerForProgressTest(t)
	tool := buildProgressFileTool(d, s)
	_, err := callErr(t, tool, map[string]any{"slug": ""})
	if err == nil {
		t.Error("empty slug should return error")
	}
}

func TestProgressFile_UnknownSlug(t *testing.T) {
	d := newDeps(t)
	s := newServerForProgressTest(t)
	tool := buildProgressFileTool(d, s)
	_, err := callErr(t, tool, map[string]any{"slug": "ghost"})
	if err == nil {
		t.Error("unknown slug should return error")
	}
}

func TestProgressFile_NoLocalPath(t *testing.T) {
	d := newDeps(t)
	s := newServerForProgressTest(t)
	p := sampleProject("nolp")
	_ = d.Registry.Add(p)
	tool := buildProgressFileTool(d, s)
	r := mustCall(t, tool, map[string]any{"slug": "nolp"}).(map[string]any)
	if r["body"] == nil {
		t.Error("body should be present even without local_path")
	}
	if r["slug"] != "nolp" {
		t.Errorf("slug = %v, want nolp", r["slug"])
	}
}

func TestProgressFile_WithPlanMd(t *testing.T) {
	dir := t.TempDir()
	writePlanMd(t, dir)
	d := newDeps(t)
	s := newServerForProgressTest(t)
	p := sampleProject("progproj", func(p *project.Project) { p.LocalPath = dir })
	_ = d.Registry.Add(p)
	tool := buildProgressFileTool(d, s)
	r := mustCall(t, tool, map[string]any{"slug": "progproj"}).(map[string]any)
	if r["slug"] != "progproj" {
		t.Errorf("slug = %v, want progproj", r["slug"])
	}
	body, _ := r["body"].(string)
	if body == "" {
		t.Error("body should not be empty when Plan.md exists")
	}
	if r["filename"] != "claude-progress.txt" {
		t.Errorf("filename = %v, want claude-progress.txt", r["filename"])
	}
}

func TestProgressFile_WriteFlag(t *testing.T) {
	dir := t.TempDir()
	writePlanMd(t, dir)
	d := newDeps(t)
	s := newServerForProgressTest(t)
	p := sampleProject("writeprog", func(p *project.Project) { p.LocalPath = dir })
	_ = d.Registry.Add(p)
	tool := buildProgressFileTool(d, s)
	r := mustCall(t, tool, map[string]any{"slug": "writeprog", "write": true}).(map[string]any)
	if r["written_to"] == nil {
		t.Error("written_to should be set when write=true and local_path set")
	}
	if _, err := os.Stat(filepath.Join(dir, "claude-progress.txt")); err != nil {
		t.Errorf("claude-progress.txt not created: %v", err)
	}
}

func TestProgressFile_InvalidJSON(t *testing.T) {
	d := newDeps(t)
	s := newServerForProgressTest(t)
	tool := buildProgressFileTool(d, s)
	b := []byte(`bad json`)
	_, err := tool.Handler(nil, b)
	if err == nil {
		t.Error("invalid JSON args should return error")
	}
}

func TestProgressFile_WithHookActivity(t *testing.T) {
	// serverWithHooks feeds Bash/Read tool events for "breeze" — the
	// HookReceiver branch (sessions, error count, top tools) must run.
	s := serverWithHooks(t)
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("breeze"))
	tool := buildProgressFileTool(d, s)
	r := mustCall(t, tool, map[string]any{"slug": "breeze"}).(map[string]any)
	body, _ := r["body"].(string)
	if !strings.Contains(body, "Bash") {
		t.Errorf("body should list top tool Bash from hook activity:\n%s", body)
	}
}

func TestProgressFile_WithActiveAlert(t *testing.T) {
	s := newServerForProgressTest(t)
	store := newAlertFixStore(t)
	// Resolve→Reopen leaves the alert in StatusActive in Snapshot().
	if err := store.Resolve("vuln:demo:critical", "x"); err != nil {
		t.Fatal(err)
	}
	if err := store.Reopen("vuln:demo:critical", "back"); err != nil {
		t.Fatal(err)
	}
	s.SetAlertStore(store)
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("alerted"))
	tool := buildProgressFileTool(d, s)
	r := mustCall(t, tool, map[string]any{"slug": "alerted"}).(map[string]any)
	body, _ := r["body"].(string)
	if !strings.Contains(body, "vuln:demo:critical") {
		t.Errorf("body should mention the active alert ID:\n%s", body)
	}
}

func TestProgressFile_WriteFails(t *testing.T) {
	dir := t.TempDir()
	writePlanMd(t, dir)
	// Block the write target: claude-progress.txt as a non-empty directory.
	target := filepath.Join(dir, "claude-progress.txt")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDeps(t)
	s := newServerForProgressTest(t)
	_ = d.Registry.Add(sampleProject("wfail", func(p *project.Project) { p.LocalPath = dir }))
	tool := buildProgressFileTool(d, s)
	_, err := callErr(t, tool, map[string]any{"slug": "wfail", "write": true})
	if err == nil {
		t.Fatal("expected write_failed when target path is a directory")
	}
	if te, ok := err.(*ToolError); !ok || te.Code != "write_failed" {
		t.Errorf("expected ToolError write_failed, got %v", err)
	}
}

// newServerForProgressTest returns a minimal *Server for progress file tests
// (no hook receiver, no alert store — exercises nil-guard branches).
func newServerForProgressTest(t *testing.T) *Server {
	t.Helper()
	s, _ := newServerForTest(t, "")
	return s
}
