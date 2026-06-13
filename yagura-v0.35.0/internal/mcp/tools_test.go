package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/osv"
	"github.com/shizukutanaka/yagura/internal/scorecard"
	"github.com/shizukutanaka/yagura/internal/secretscan"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/registry"
)

// fixedNow returns a deterministic timestamp for score/staleness tests.
var fixedNow = time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)

func newDeps(t *testing.T) Deps {
	t.Helper()
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Deps{Registry: reg, Now: func() time.Time { return fixedNow }}
}

func sampleProject(slug string, mods ...func(*project.Project)) *project.Project {
	p := &project.Project{
		Slug:        slug,
		DisplayName: slug + " display",
		Repository:  "github.com/o/" + slug,
		Language:    "Go",
		Stage:       project.StageActive,
		Priority:    3,
	}
	for _, m := range mods {
		m(p)
	}
	return p
}

func mustCall(t *testing.T, tool *Tool, args any) any {
	t.Helper()
	b, _ := json.Marshal(args)
	result, err := tool.Handler(context.Background(), b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return result
}

// ─── yagura_list ─────────────────────────────────────────────

func TestList_EmptyRegistry(t *testing.T) {
	d := newDeps(t)
	tool := buildListTool(d)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	if r["count"].(int) != 0 {
		t.Errorf("expected 0, got %v", r["count"])
	}
}

func TestList_ReturnsAllProjects(t *testing.T) {
	d := newDeps(t)
	for _, s := range []string{"alpha", "bravo", "charlie"} {
		_ = d.Registry.Add(sampleProject(s))
	}
	tool := buildListTool(d)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	if r["count"].(int) != 3 {
		t.Fatalf("expected 3, got %v", r["count"])
	}
	out := r["projects"].([]listOut)
	if out[0].Slug != "alpha" || out[2].Slug != "charlie" {
		t.Errorf("expected sorted: %v", out)
	}
}

func TestList_Limit(t *testing.T) {
	d := newDeps(t)
	for _, s := range []string{"alpha", "bravo", "charlie", "delta"} {
		_ = d.Registry.Add(sampleProject(s))
	}
	tool := buildListTool(d)
	r := mustCall(t, tool, map[string]any{"limit": 2}).(map[string]any)
	if r["count"].(int) != 2 {
		t.Fatalf("limit=2 expected count 2, got %v", r["count"])
	}
	if r["total"].(int) != 4 || r["truncated"] != true {
		t.Errorf("expected total=4 truncated=true, got total=%v truncated=%v", r["total"], r["truncated"])
	}
	out := r["projects"].([]listOut)
	if len(out) != 2 || out[0].Slug != "alpha" {
		t.Errorf("expected first 2 sorted, got %v", out)
	}
	// no limit → all, no truncated key
	full := mustCall(t, tool, struct{}{}).(map[string]any)
	if full["count"].(int) != 4 {
		t.Errorf("no-limit expected 4, got %v", full["count"])
	}
	if _, ok := full["truncated"]; ok {
		t.Errorf("no-limit should not set truncated")
	}
}

// ─── yagura_get ──────────────────────────────────────────────

func TestGet_Existing(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("foo"))
	tool := buildGetTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "foo"})
	p := r.(*project.Project)
	if p.Slug != "foo" {
		t.Errorf("got %s", p.Slug)
	}
}

func TestGet_NotFound(t *testing.T) {
	d := newDeps(t)
	tool := buildGetTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "ghost"})
	_, err := tool.Handler(context.Background(), b)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsCode(err, "not_found") {
		t.Errorf("expected not_found, got %v", err)
	}
}

func TestGet_MissingSlug(t *testing.T) {
	d := newDeps(t)
	tool := buildGetTool(d)
	b, _ := json.Marshal(map[string]any{})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

// ─── yagura_search ───────────────────────────────────────────

func TestSearch_FilterByTag(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("a", func(p *project.Project) { p.Tags = []string{"mcp", "daemon"} }))
	_ = d.Registry.Add(sampleProject("b", func(p *project.Project) { p.Tags = []string{"web"} }))

	tool := buildSearchTool(d)
	r := mustCall(t, tool, map[string]any{"tag": "MCP"}).(map[string]any)
	if r["count"].(int) != 1 {
		t.Errorf("expected 1, got %v", r["count"])
	}
}

func TestSearch_FilterByLanguage(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("a", func(p *project.Project) { p.Language = "Go" }))
	_ = d.Registry.Add(sampleProject("b", func(p *project.Project) { p.Language = "Rust" }))

	tool := buildSearchTool(d)
	r := mustCall(t, tool, map[string]any{"language": "rust"}).(map[string]any)
	if r["count"].(int) != 1 {
		t.Errorf("expected 1, got %v", r["count"])
	}
}

func TestSearch_FilterByStage(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("a"))
	_ = d.Registry.Add(sampleProject("b", func(p *project.Project) { p.Stage = project.StageArchived }))

	tool := buildSearchTool(d)
	r := mustCall(t, tool, map[string]any{"stage": "archived"}).(map[string]any)
	if r["count"].(int) != 1 {
		t.Errorf("expected 1, got %v", r["count"])
	}
}

func TestSearch_FreeQuery(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("alpha", func(p *project.Project) { p.Notes = "P2P engine" }))
	_ = d.Registry.Add(sampleProject("bravo", func(p *project.Project) { p.Notes = "CLI utility" }))

	tool := buildSearchTool(d)
	r := mustCall(t, tool, map[string]any{"query": "p2p"}).(map[string]any)
	if r["count"].(int) != 1 {
		t.Errorf("expected 1, got %v", r["count"])
	}
}

func TestSearch_AllConditionsAND(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("a", func(p *project.Project) {
		p.Tags = []string{"daemon"}
		p.Language = "Go"
	}))
	_ = d.Registry.Add(sampleProject("b", func(p *project.Project) {
		p.Tags = []string{"daemon"}
		p.Language = "Rust"
	}))

	tool := buildSearchTool(d)
	r := mustCall(t, tool, map[string]any{"tag": "daemon", "language": "Go"}).(map[string]any)
	if r["count"].(int) != 1 {
		t.Errorf("expected 1 (AND of conditions), got %v", r["count"])
	}
}

func TestSearch_InvalidStage(t *testing.T) {
	d := newDeps(t)
	tool := buildSearchTool(d)
	b, _ := json.Marshal(map[string]any{"stage": "garbage"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

// ─── yagura_today ────────────────────────────────────────────

func TestToday_ScoringByPriority(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("hi", func(p *project.Project) { p.Priority = 5 }))
	_ = d.Registry.Add(sampleProject("lo", func(p *project.Project) { p.Priority = 1 }))

	tool := buildTodayTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	items := r["items"].([]todayItem)
	if items[0].Slug != "hi" {
		t.Errorf("highest priority should top: %v", items)
	}
}

func TestToday_FailingCIBoosts(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("noci", func(p *project.Project) { p.Priority = 3 }))
	_ = d.Registry.Add(sampleProject("failci", func(p *project.Project) {
		p.Priority = 3
		p.CIStatus = project.CIStatusFailing
	}))
	tool := buildTodayTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	items := r["items"].([]todayItem)
	if items[0].Slug != "failci" {
		t.Errorf("failing CI should boost: %v", items)
	}
}

func TestToday_LimitParam(t *testing.T) {
	d := newDeps(t)
	for i := 0; i < 10; i++ {
		_ = d.Registry.Add(sampleProject("p" + string(rune('a'+i))))
	}
	tool := buildTodayTool(d)
	r := mustCall(t, tool, map[string]any{"limit": 3}).(map[string]any)
	if r["count"].(int) != 3 {
		t.Errorf("expected limit=3, got %v", r["count"])
	}
}

func TestToday_LimitClamped(t *testing.T) {
	d := newDeps(t)
	for i := 0; i < 5; i++ {
		_ = d.Registry.Add(sampleProject("p" + string(rune('a'+i))))
	}
	tool := buildTodayTool(d)
	r := mustCall(t, tool, map[string]any{"limit": 999}).(map[string]any)
	if r["count"].(int) != 5 {
		t.Errorf("expected 5 (all), got %v", r["count"])
	}
}

func TestToday_ExcludesArchived(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("a"))
	_ = d.Registry.Add(sampleProject("b", func(p *project.Project) { p.Stage = project.StageArchived }))
	tool := buildTodayTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	if r["count"].(int) != 1 {
		t.Errorf("archived should be excluded, got %v", r["count"])
	}
}

// ─── yagura_register ─────────────────────────────────────────

func TestRegister_Success(t *testing.T) {
	d := newDeps(t)
	tool := buildRegisterTool(d)
	r := mustCall(t, tool, map[string]any{
		"slug":       "new1",
		"repository": "github.com/o/new1",
		"language":   "Go",
		"priority":   3,
	}).(map[string]any)
	if r["created"].(bool) != true {
		t.Errorf("expected created=true: %v", r)
	}
	if d.Registry.List()[0].Slug != "new1" {
		t.Error("project not persisted")
	}
}

func TestRegister_MissingRepository(t *testing.T) {
	d := newDeps(t)
	tool := buildRegisterTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "abc"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestRegister_Duplicate(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("dup"))
	tool := buildRegisterTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "dup", "repository": "github.com/o/dup"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' message, got %v", err)
	}
}

// ─── ToolError ───────────────────────────────────────────────

func TestToolError_Format(t *testing.T) {
	e := &ToolError{Code: "internal", Message: "boom", Cause: errors.New("root")}
	if !strings.Contains(e.Error(), "boom") || !strings.Contains(e.Error(), "root") {
		t.Errorf("error string missing parts: %s", e.Error())
	}
	if !errors.Is(e.Unwrap(), e.Cause) {
		t.Error("Unwrap should chain")
	}
}

func TestIsCode(t *testing.T) {
	e := &ToolError{Code: "not_found"}
	if !IsCode(e, "not_found") {
		t.Error("should match")
	}
	if IsCode(e, "internal") {
		t.Error("should not match")
	}
	if IsCode(errors.New("plain"), "not_found") {
		t.Error("plain error should not match")
	}
}

// ─── yagura_unregister ───────────────────────────────────────

func TestUnregister_Success(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("byebye"))
	tool := buildUnregisterTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "byebye"}).(map[string]any)
	if r["deleted"].(bool) != true {
		t.Errorf("expected deleted=true: %v", r)
	}
	if _, err := d.Registry.Get("byebye"); err == nil {
		t.Error("should be deleted from registry")
	}
}

func TestUnregister_NotFound(t *testing.T) {
	d := newDeps(t)
	tool := buildUnregisterTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "ghost"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "not_found") {
		t.Errorf("expected not_found, got %v", err)
	}
}

func TestUnregister_MissingSlug(t *testing.T) {
	d := newDeps(t)
	tool := buildUnregisterTool(d)
	b, _ := json.Marshal(map[string]any{})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

// ─── yagura_update ───────────────────────────────────────────

// Trust base: repo_public is a scanner-only sensor field. yagura_update must
// not let an MCP client forge it (the visibility-mismatch alert relies on it
// reflecting observed reality, not client claims).
func TestUpdate_CannotForgeRepoPublic(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("sens"))
	tool := buildUpdateTool(d)
	// Slip an extra repo_public:true into the payload alongside a legit field.
	_ = mustCall(t, tool, map[string]any{
		"slug":        "sens",
		"notes":       "touch",
		"repo_public": true,
	})
	got, _ := d.Registry.Get("sens")
	if got.RepoPublic {
		t.Error("yagura_update must not be able to set the repo_public sensor field")
	}
}

func TestUpdate_PartialFields(t *testing.T) {
	d := newDeps(t)
	p := sampleProject("upd")
	p.Notes = "original notes"
	p.Tags = []string{"old"}
	_ = d.Registry.Add(p)

	tool := buildUpdateTool(d)
	r := mustCall(t, tool, map[string]any{
		"slug":     "upd",
		"priority": 5,
		// notes/tags omitted intentionally to verify they're preserved
	}).(map[string]any)
	if r["updated"].(bool) != true {
		t.Errorf("expected updated=true")
	}
	got, _ := d.Registry.Get("upd")
	if got.Priority != 5 {
		t.Errorf("priority not updated: %d", got.Priority)
	}
	if got.Notes != "original notes" {
		t.Errorf("notes should be preserved: %q", got.Notes)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "old" {
		t.Errorf("tags should be preserved: %v", got.Tags)
	}
}

func TestUpdate_StageChange(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("stg"))
	tool := buildUpdateTool(d)
	_ = mustCall(t, tool, map[string]any{"slug": "stg", "stage": "archived"})
	got, _ := d.Registry.Get("stg")
	if got.Stage != project.StageArchived {
		t.Errorf("stage should be archived: %s", got.Stage)
	}
}

func TestUpdate_InvalidStage(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("stg2"))
	tool := buildUpdateTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "stg2", "stage": "garbage"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestUpdate_InvalidPriority(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("p"))
	tool := buildUpdateTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "p", "priority": 99})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	d := newDeps(t)
	tool := buildUpdateTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "ghost", "priority": 1})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "not_found") {
		t.Errorf("expected not_found, got %v", err)
	}
}

// ─── yagura_stats ────────────────────────────────────────────

func TestStats_EmptyRegistry(t *testing.T) {
	d := newDeps(t)
	tool := buildStatsTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	if r["total"].(int) != 0 {
		t.Errorf("expected total=0, got %v", r["total"])
	}
}

func TestStats_Aggregates(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("a", func(p *project.Project) {
		p.Stage = project.StageActive
		p.Priority = 5
		p.Language = "Go"
		p.OpenPRs = 2
		p.CIStatus = project.CIStatusPassing
	}))
	_ = d.Registry.Add(sampleProject("b", func(p *project.Project) {
		p.Stage = project.StageMaintenance
		p.Priority = 1
		p.Language = "Rust"
		p.OpenPRs = 3
		p.CIStatus = project.CIStatusFailing
	}))
	_ = d.Registry.Add(sampleProject("c", func(p *project.Project) {
		p.Stage = project.StageArchived
		p.Language = "Go"
	}))

	tool := buildStatsTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	if r["total"].(int) != 3 {
		t.Errorf("total: %v", r["total"])
	}
	stage := r["by_stage"].(map[string]int)
	if stage["active"] != 1 || stage["maintenance"] != 1 || stage["archived"] != 1 {
		t.Errorf("by_stage: %v", stage)
	}
	lang := r["by_language"].(map[string]int)
	if lang["Go"] != 2 || lang["Rust"] != 1 {
		t.Errorf("by_language: %v", lang)
	}
	if r["total_open_prs"].(int) != 5 {
		t.Errorf("total_open_prs: %v", r["total_open_prs"])
	}
	avg := r["avg_priority"].(float64)
	if avg != 3.0 {
		t.Errorf("avg_priority: %v (expected 3.0 from (5+1)/2)", avg)
	}
}

// ─── yagura_vulns ────────────────────────────────────────────

// mockOSV implements OSVQuerier for testing.
type mockOSV struct {
	vulns []osv.Vuln
	err   error
	calls int

	gotEcosystem string
	gotPackage   string
	gotVersion   string
}

func (m *mockOSV) Query(_ context.Context, ecosystem, pkg, version string) ([]osv.Vuln, error) {
	m.calls++
	m.gotEcosystem = ecosystem
	m.gotPackage = pkg
	m.gotVersion = version
	if m.err != nil {
		return nil, m.err
	}
	return m.vulns, nil
}

func TestVulns_RequiresOSV(t *testing.T) {
	d := newDeps(t)
	// OSV is nil
	tool := buildVulnsTool(d)
	b, _ := json.Marshal(map[string]any{"package": "x", "ecosystem": "Go"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "unavailable") {
		t.Errorf("expected unavailable, got %v", err)
	}
}

func TestVulns_DirectQuery(t *testing.T) {
	d := newDeps(t)
	d.OSV = &mockOSV{
		vulns: []osv.Vuln{
			{ID: "X-1", Severity: osv.SeverityHigh, CVSSScore: 7.5},
		},
	}
	tool := buildVulnsTool(d)
	r := mustCall(t, tool, map[string]any{
		"package":   "github.com/example/x",
		"ecosystem": "Go",
		"version":   "v1.0.0",
	}).(map[string]any)
	if r["total"].(int) != 1 {
		t.Errorf("total: %v", r["total"])
	}
	bySev := r["by_severity"].(map[string]int)
	if bySev["HIGH"] != 1 {
		t.Errorf("by_severity: %v", bySev)
	}
}

func TestVulns_SlugResolves(t *testing.T) {
	d := newDeps(t)
	p := sampleProject("vp")
	p.Language = "Go"
	p.Repository = "github.com/example/vp"
	p.LatestVersion = "v1.2.3"
	_ = d.Registry.Add(p)

	mock := &mockOSV{vulns: []osv.Vuln{{ID: "V1", Severity: osv.SeverityMedium}}}
	d.OSV = mock
	tool := buildVulnsTool(d)
	_ = mustCall(t, tool, map[string]any{"slug": "vp"})

	if mock.gotEcosystem != "Go" {
		t.Errorf("ecosystem inferred: got %s", mock.gotEcosystem)
	}
	if mock.gotPackage != "github.com/example/vp" {
		t.Errorf("package from Repository: got %s", mock.gotPackage)
	}
	if mock.gotVersion != "v1.2.3" {
		t.Errorf("version from LatestVersion: got %s", mock.gotVersion)
	}
}

func TestVulns_SlugNotFound(t *testing.T) {
	d := newDeps(t)
	d.OSV = &mockOSV{}
	tool := buildVulnsTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "ghost"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "not_found") {
		t.Errorf("expected not_found, got %v", err)
	}
}

func TestVulns_MissingPackage(t *testing.T) {
	d := newDeps(t)
	d.OSV = &mockOSV{}
	tool := buildVulnsTool(d)
	b, _ := json.Marshal(map[string]any{"ecosystem": "Go"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestVulns_MissingEcosystem(t *testing.T) {
	d := newDeps(t)
	d.OSV = &mockOSV{}
	// slug 経由で Language が空のプロジェクト
	p := sampleProject("nolang")
	p.Language = ""
	p.Repository = "github.com/example/nolang"
	_ = d.Registry.Add(p)
	tool := buildVulnsTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "nolang"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestVulns_MinSeverityFilter(t *testing.T) {
	d := newDeps(t)
	d.OSV = &mockOSV{
		vulns: []osv.Vuln{
			{ID: "C", Severity: osv.SeverityCritical, CVSSScore: 9.8},
			{ID: "H", Severity: osv.SeverityHigh, CVSSScore: 7.5},
			{ID: "M", Severity: osv.SeverityMedium, CVSSScore: 5.0},
			{ID: "L", Severity: osv.SeverityLow, CVSSScore: 2.0},
		},
	}
	tool := buildVulnsTool(d)
	r := mustCall(t, tool, map[string]any{
		"package":      "x",
		"ecosystem":    "Go",
		"min_severity": "HIGH",
	}).(map[string]any)
	if r["total"].(int) != 2 {
		t.Errorf("expected 2 (C+H), got %v", r["total"])
	}
}

func TestVulns_InvalidMinSeverity(t *testing.T) {
	d := newDeps(t)
	d.OSV = &mockOSV{}
	tool := buildVulnsTool(d)
	b, _ := json.Marshal(map[string]any{
		"package": "x", "ecosystem": "Go", "min_severity": "EXTREME",
	})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestVulns_UpstreamError(t *testing.T) {
	d := newDeps(t)
	d.OSV = &mockOSV{err: errors.New("network down")}
	tool := buildVulnsTool(d)
	b, _ := json.Marshal(map[string]any{"package": "x", "ecosystem": "Go"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "osv_query_failed") {
		t.Errorf("expected osv_query_failed, got %v", err)
	}
}

// ─── yagura_scorecard ────────────────────────────────────────

type mockScorecard struct {
	score *scorecard.Score
	err   error
	calls int

	gotRepo string
}

func (m *mockScorecard) Fetch(_ context.Context, repo string) (*scorecard.Score, error) {
	m.calls++
	m.gotRepo = repo
	if m.err != nil {
		return nil, m.err
	}
	return m.score, nil
}

func TestScorecard_RequiresClient(t *testing.T) {
	d := newDeps(t)
	tool := buildScorecardTool(d)
	b, _ := json.Marshal(map[string]any{"repo": "x/y"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "unavailable") {
		t.Errorf("expected unavailable, got %v", err)
	}
}

func TestScorecard_DirectRepo(t *testing.T) {
	d := newDeps(t)
	d.Scorecard = &mockScorecard{
		score: &scorecard.Score{
			Repo:  "github.com/x/y",
			Score: 8.5,
			Checks: []scorecard.Check{
				{Name: "Branch-Protection", Score: 10},
				{Name: "Pinned-Dependencies", Score: 9},
			},
		},
	}
	tool := buildScorecardTool(d)
	r := mustCall(t, tool, map[string]any{"repo": "x/y"}).(map[string]any)
	if r["score"].(float64) != 8.5 {
		t.Errorf("score: %v", r["score"])
	}
	if r["category"].(string) != "excellent" {
		t.Errorf("category: %v", r["category"])
	}
	if r["check_count"].(int) != 2 {
		t.Errorf("check_count: %v", r["check_count"])
	}
}

func TestScorecard_SlugResolves(t *testing.T) {
	d := newDeps(t)
	p := sampleProject("scp")
	p.Repository = "github.com/example/scp"
	_ = d.Registry.Add(p)
	mock := &mockScorecard{score: &scorecard.Score{Repo: "github.com/example/scp", Score: 6.0}}
	d.Scorecard = mock
	tool := buildScorecardTool(d)
	_ = mustCall(t, tool, map[string]any{"slug": "scp"})
	if mock.gotRepo != "github.com/example/scp" {
		t.Errorf("repo from registry: got %s", mock.gotRepo)
	}
}

func TestScorecard_NotScored(t *testing.T) {
	d := newDeps(t)
	d.Scorecard = &mockScorecard{err: scorecard.ErrNotScored}
	tool := buildScorecardTool(d)
	b, _ := json.Marshal(map[string]any{"repo": "x/y"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "not_scored") {
		t.Errorf("expected not_scored, got %v", err)
	}
}

func TestScorecard_FetchError(t *testing.T) {
	d := newDeps(t)
	d.Scorecard = &mockScorecard{err: errors.New("api down")}
	tool := buildScorecardTool(d)
	b, _ := json.Marshal(map[string]any{"repo": "x/y"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "scorecard_fetch_failed") {
		t.Errorf("expected scorecard_fetch_failed, got %v", err)
	}
}

func TestScorecard_PriorityOnly(t *testing.T) {
	d := newDeps(t)
	d.Scorecard = &mockScorecard{
		score: &scorecard.Score{
			Repo: "github.com/x/y",
			Score: 7.0,
			Checks: []scorecard.Check{
				{Name: "Branch-Protection", Score: 10},
				{Name: "Binary-Artifacts", Score: 10},
				{Name: "Code-Review", Score: 8},
				{Name: "License", Score: 10},
				{Name: "Pinned-Dependencies", Score: 9},
			},
		},
	}
	tool := buildScorecardTool(d)
	r := mustCall(t, tool, map[string]any{
		"repo":          "x/y",
		"priority_only": true,
	}).(map[string]any)
	// Should only contain Branch-Protection, Code-Review, Pinned-Dependencies
	checks := r["checks"].([]scorecard.Check)
	if len(checks) != 3 {
		t.Errorf("expected 3 priority checks, got %d", len(checks))
	}
	for _, c := range checks {
		name := c.Name
		if name != "Branch-Protection" && name != "Code-Review" && name != "Pinned-Dependencies" {
			t.Errorf("unexpected non-priority check: %s", name)
		}
	}
}

func TestScorecard_MissingInput(t *testing.T) {
	d := newDeps(t)
	d.Scorecard = &mockScorecard{}
	tool := buildScorecardTool(d)
	b, _ := json.Marshal(map[string]any{})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestScorecard_SlugNotFound(t *testing.T) {
	d := newDeps(t)
	d.Scorecard = &mockScorecard{}
	tool := buildScorecardTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "ghost"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "not_found") {
		t.Errorf("expected not_found, got %v", err)
	}
}

// ─── yagura_health ───────────────────────────────────────────

func TestHealth_EmptyPortfolio(t *testing.T) {
	d := newDeps(t)
	tool := buildHealthTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	if r["total_active"].(int) != 0 {
		t.Errorf("expected 0, got %v", r["total_active"])
	}
	if r["needs_attention_count"].(int) != 0 {
		t.Errorf("expected 0 needs_attention, got %v", r["needs_attention_count"])
	}
}

func TestHealth_Aggregates(t *testing.T) {
	d := newDeps(t)
	now := time.Now()

	healthy := sampleProject("healthy", func(p *project.Project) {
		p.ScorecardScore = 9.0
		p.ScorecardAt = now
		p.VulnScanAt = now
	})
	risky := sampleProject("risky", func(p *project.Project) {
		p.ScorecardScore = 3.0
		p.ScorecardAt = now
		p.VulnCritical = 2
		p.VulnHigh = 1
		p.VulnScanAt = now
	})
	notScanned := sampleProject("notscan", func(p *project.Project) {
		// no security data
	})
	archived := sampleProject("old", func(p *project.Project) {
		p.Stage = project.StageArchived
		p.VulnCritical = 99 // should be excluded
	})
	_ = d.Registry.Add(healthy)
	_ = d.Registry.Add(risky)
	_ = d.Registry.Add(notScanned)
	_ = d.Registry.Add(archived)

	tool := buildHealthTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)

	if r["scorecard_scanned"].(int) != 2 {
		t.Errorf("scorecard_scanned: got %v", r["scorecard_scanned"])
	}
	if r["needs_attention_count"].(int) != 1 {
		t.Errorf("needs_attention_count: got %v", r["needs_attention_count"])
	}
	vulns := r["total_vulns"].(map[string]int)
	// archived should be excluded
	if vulns["critical"] != 2 || vulns["high"] != 1 {
		t.Errorf("vulns aggregation: %v", vulns)
	}
	avg := r["avg_scorecard"].(float64)
	expected := (9.0 + 3.0) / 2
	if avg != expected {
		t.Errorf("avg_scorecard: got %g want %g", avg, expected)
	}
}

func TestHealth_Individual(t *testing.T) {
	d := newDeps(t)
	now := time.Now()
	p := sampleProject("ind", func(p *project.Project) {
		p.ScorecardScore = 7.5
		p.ScorecardAt = now
		p.VulnHigh = 1
		p.VulnScanAt = now
	})
	_ = d.Registry.Add(p)

	tool := buildHealthTool(d)
	r := mustCall(t, tool, map[string]any{
		"slug":       "ind",
		"individual": true,
	}).(map[string]any)
	if r["slug"].(string) != "ind" {
		t.Errorf("slug: %v", r["slug"])
	}
	if r["scorecard_score"].(float64) != 7.5 {
		t.Errorf("score: %v", r["scorecard_score"])
	}
	if r["scorecard_category"].(string) != "good" {
		t.Errorf("category: %v", r["scorecard_category"])
	}
	if r["needs_attention"].(bool) != true {
		t.Errorf("should need attention (high vuln): %v", r["needs_attention"])
	}
}

func TestHealth_IndividualWithoutSlug(t *testing.T) {
	d := newDeps(t)
	tool := buildHealthTool(d)
	b, _ := json.Marshal(map[string]any{"individual": true})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestHealth_IndividualNotFound(t *testing.T) {
	d := newDeps(t)
	tool := buildHealthTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "ghost", "individual": true})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "not_found") {
		t.Errorf("expected not_found, got %v", err)
	}
}

func TestHealth_NeedsAttentionList(t *testing.T) {
	d := newDeps(t)
	now := time.Now()

	// 3 different reasons for "needs attention"
	_ = d.Registry.Add(sampleProject("crit", func(p *project.Project) {
		p.VulnCritical = 1
		p.VulnScanAt = now
	}))
	_ = d.Registry.Add(sampleProject("highvuln", func(p *project.Project) {
		p.VulnHigh = 5
		p.VulnScanAt = now
	}))
	_ = d.Registry.Add(sampleProject("poorscore", func(p *project.Project) {
		p.ScorecardScore = 2.5
		p.ScorecardAt = now
	}))
	_ = d.Registry.Add(sampleProject("ok", func(p *project.Project) {
		p.ScorecardScore = 8.0
		p.ScorecardAt = now
	}))

	tool := buildHealthTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	list := r["needs_attention"].([]map[string]any)
	if len(list) != 3 {
		t.Errorf("expected 3 in needs_attention, got %d", len(list))
	}
}

// ─── yagura_secretscan ───────────────────────────────────────

func TestSecretScan_RequiresClient(t *testing.T) {
	d := newDeps(t)
	tool := buildSecretScanTool(d)
	b, _ := json.Marshal(map[string]any{})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "unavailable") {
		t.Errorf("expected unavailable, got %v", err)
	}
}

func TestSecretScan_PortfolioWide(t *testing.T) {
	d := newDeps(t)
	d.SecretScanner = secretscan.New()

	// Setup: 2 projects, one with secret in notes, one clean
	p1 := sampleProject("badp", func(p *project.Project) {
		p.Notes = `Don't forget the token: ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ`
	})
	p2 := sampleProject("cleanp", func(p *project.Project) {
		p.Notes = "Just plain notes about the project"
	})
	_ = d.Registry.Add(p1)
	_ = d.Registry.Add(p2)

	tool := buildSecretScanTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)

	if r["scanned_projects"].(int) != 2 {
		t.Errorf("scanned_projects: %v", r["scanned_projects"])
	}
	if r["total_findings"].(int) < 1 {
		t.Errorf("expected ≥1 finding (the github token): %v", r["total_findings"])
	}
}

// TestSecretScan_CustomRules verifies a caller-supplied rule flags an org-specific
// token the default rule set would miss.
func TestSecretScan_CustomRules(t *testing.T) {
	d := newDeps(t)
	d.SecretScanner = secretscan.New()
	_ = d.Registry.Add(sampleProject("acme", func(p *project.Project) {
		p.Notes = "internal token acme_abcd1234efgh5678 do not commit"
	}))

	tool := buildSecretScanTool(d)
	r := mustCall(t, tool, map[string]any{
		"custom_rules": []map[string]any{
			{"id": "acme-token", "pattern": `acme_[A-Za-z0-9]{16}`, "severity": "HIGH"},
		},
	}).(map[string]any)
	if r["total_findings"].(int) < 1 {
		t.Errorf("custom rule should flag the acme_ token, got %v", r["total_findings"])
	}
}

// TestSecretScan_BadCustomRule surfaces a compile error as invalid_input.
func TestSecretScan_BadCustomRule(t *testing.T) {
	d := newDeps(t)
	d.SecretScanner = secretscan.New()
	tool := buildSecretScanTool(d)
	b, _ := json.Marshal(map[string]any{
		"custom_rules": []map[string]any{{"id": "bad", "pattern": "[invalid", "severity": "HIGH"}},
	})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input for bad regex, got %v", err)
	}
}

func TestSecretScan_SingleProject(t *testing.T) {
	d := newDeps(t)
	d.SecretScanner = secretscan.New()

	p := sampleProject("targetp", func(p *project.Project) {
		p.Notes = `aws=AKIAQRSTUVWXYZ012345`
	})
	_ = d.Registry.Add(p)

	tool := buildSecretScanTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "targetp"}).(map[string]any)
	if r["scanned_projects"].(int) != 1 {
		t.Errorf("expected 1 scanned, got %v", r["scanned_projects"])
	}
	if r["total_findings"].(int) < 1 {
		t.Errorf("AKIA key should be detected: %+v", r)
	}
}

func TestSecretScan_SlugNotFound(t *testing.T) {
	d := newDeps(t)
	d.SecretScanner = secretscan.New()
	tool := buildSecretScanTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "ghost"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "not_found") {
		t.Errorf("expected not_found, got %v", err)
	}
}

func TestSecretScan_MinSeverity(t *testing.T) {
	d := newDeps(t)
	d.SecretScanner = secretscan.New()
	p := sampleProject("multi", func(p *project.Project) {
		// Database URL (HIGH) + JWT (MEDIUM)
		p.Notes = `db: postgres://admin:s3cretP4ss@db.example.com:5432/mydb
jwt: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3OD.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c`
	})
	_ = d.Registry.Add(p)

	tool := buildSecretScanTool(d)
	// HIGH only → DB URL のみ
	r := mustCall(t, tool, map[string]any{"min_severity": "HIGH"}).(map[string]any)
	totals := r["total_findings"].(int)
	if totals < 1 {
		t.Errorf("expected ≥1 high+ finding: %v", r)
	}
}

func TestSecretScan_InvalidSeverity(t *testing.T) {
	d := newDeps(t)
	d.SecretScanner = secretscan.New()
	tool := buildSecretScanTool(d)
	b, _ := json.Marshal(map[string]any{"min_severity": "GARBAGE"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestSecretScan_EmptyPortfolio(t *testing.T) {
	d := newDeps(t)
	d.SecretScanner = secretscan.New()
	tool := buildSecretScanTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	if r["total_findings"].(int) != 0 {
		t.Errorf("empty portfolio: %v", r["total_findings"])
	}
}

func TestSecretScan_SkipsArchived(t *testing.T) {
	d := newDeps(t)
	d.SecretScanner = secretscan.New()
	p := sampleProject("oldp", func(p *project.Project) {
		p.Stage = project.StageArchived
		p.Notes = `ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ`
	})
	_ = d.Registry.Add(p)
	tool := buildSecretScanTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	if r["total_findings"].(int) != 0 {
		t.Errorf("archived should be skipped: %v", r["total_findings"])
	}
}

func TestProjectFieldsAsScanItems_SkipsEmpty(t *testing.T) {
	p := &project.Project{
		Slug:        "p",
		DisplayName: "",
		Notes:       "  ",
		Tags:        []string{},
	}
	items := projectFieldsAsScanItems(p)
	if len(items) != 0 {
		t.Errorf("empty fields should yield no items, got %d", len(items))
	}
}

func TestProjectFieldsAsScanItems_IncludesSprint(t *testing.T) {
	p := &project.Project{
		Slug: "p",
		Notes: "hello",
		Sprint: &project.Sprint{
			Goal: "ship v1",
			Milestones: []project.Milestone{
				{Title: "design done"},
				{Title: "tests done"},
			},
		},
	}
	items := projectFieldsAsScanItems(p)
	// Notes + sprint.goal + 2 milestones = 4
	if len(items) != 4 {
		t.Errorf("expected 4 items, got %d: %+v", len(items), items)
	}
}
