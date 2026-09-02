package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shizukutanaka/yagura/internal/project"
)

// callRawErr invokes a handler with raw (possibly malformed) JSON and returns
// the error, asserting one was produced.
func callRawErr(t *testing.T, tool *Tool, raw string) error {
	t.Helper()
	_, err := tool.Handler(context.Background(), json.RawMessage(raw))
	if err == nil {
		t.Fatalf("%s: expected an error for raw args %q", tool.Name, raw)
	}
	return err
}

// ─── invalid-args (json.Unmarshal failure) branches ──────────────
// A JSON array decoded into each handler's struct fails, exercising the
// "invalid args" guard in list/get/search/today/register/unregister/update.

func TestInventory_InvalidArgs(t *testing.T) {
	d := newDeps(t)
	tools := []*Tool{
		buildListTool(d),
		buildGetTool(d),
		buildSearchTool(d),
		buildTodayTool(d),
		buildRegisterTool(d),
		buildUnregisterTool(d),
		buildUpdateTool(d),
	}
	for _, tool := range tools {
		err := callRawErr(t, tool, "[1,2,3]")
		if !IsCode(err, "invalid_input") {
			t.Errorf("%s: expected invalid_input, got %v", tool.Name, err)
		}
	}
}

// ─── search: negative limit normalises to 0 (unbounded) ──────────

func TestSearch_NegativeLimitUnbounded(t *testing.T) {
	d := newDeps(t)
	for _, s := range []string{"alpha", "bravo", "charlie"} {
		_ = d.Registry.Add(sampleProject(s))
	}
	tool := buildSearchTool(d)
	r := mustCall(t, tool, map[string]any{"limit": -5}).(map[string]any)
	if r["count"].(int) != 3 {
		t.Errorf("negative limit should be treated as unbounded, got %v", r["count"])
	}
	if _, ok := r["truncated"]; ok {
		t.Error("negative limit should not truncate")
	}
}

// ─── today: open-PR boost and stale-active-idle reason ───────────

func TestToday_OpenPRsAndStaleReasons(t *testing.T) {
	d := newDeps(t)
	// open PRs → score boost + "open PRs" reason
	_ = d.Registry.Add(sampleProject("prs", func(p *project.Project) {
		p.Priority = 1
		p.OpenPRs = 4
	}))
	// active but idle ≥14 days → "stale (active but idle)" reason
	_ = d.Registry.Add(sampleProject("idle", func(p *project.Project) {
		p.Priority = 1
		p.Stage = project.StageActive
		p.LatestActivity = fixedNow.AddDate(0, 0, -20)
	}))
	tool := buildTodayTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	items := r["items"].([]todayItem)

	reasons := map[string][]string{}
	for _, it := range items {
		reasons[it.Slug] = it.Reasons
	}
	if !containsStr(reasons["prs"], "open PRs") {
		t.Errorf("prs project should have 'open PRs' reason: %v", reasons["prs"])
	}
	if !containsStr(reasons["idle"], "stale (active but idle)") {
		t.Errorf("idle project should have stale reason: %v", reasons["idle"])
	}
}

// ─── update: all manual-metadata fields apply ────────────────────

func TestUpdate_AllManualFields(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("full"))
	tool := buildUpdateTool(d)
	_ = mustCall(t, tool, map[string]any{
		"slug":         "full",
		"display_name": "New Display",
		"language":     "Rust",
		"local_path":   "/home/u/full",
		"tags":         []string{"x", "y"},
		"depends_on":   []string{"alpha"},
	}).(map[string]any)

	got, _ := d.Registry.Get("full")
	if got.DisplayName != "New Display" {
		t.Errorf("display_name: %q", got.DisplayName)
	}
	if got.Language != "Rust" {
		t.Errorf("language: %q", got.Language)
	}
	if got.LocalPath != "/home/u/full" {
		t.Errorf("local_path: %q", got.LocalPath)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "x" {
		t.Errorf("tags: %v", got.Tags)
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != "alpha" {
		t.Errorf("depends_on: %v", got.DependsOn)
	}
}

// ─── update: registry validation failure surfaces as internal error ──
// Setting display_name to "" passes the handler's own checks but fails
// project.Validate() inside Registry.Update, exercising the "update failed" path.

func TestUpdate_RegistryValidationFails(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("inval"))
	tool := buildUpdateTool(d)
	b, _ := json.Marshal(map[string]any{"slug": "inval", "display_name": ""})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "internal") {
		t.Errorf("expected internal (update failed) when validation rejects the change, got %v", err)
	}
}

// ─── stats: with-sprint and stale-active counters ────────────────

func TestStats_SprintAndStaleCounters(t *testing.T) {
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("sp", func(p *project.Project) {
		p.Sprint = &project.Sprint{Phase: project.PhaseBuild}
	}))
	_ = d.Registry.Add(sampleProject("st", func(p *project.Project) {
		p.Stage = project.StageActive
		p.LatestActivity = fixedNow.AddDate(0, 0, -30)
	}))
	tool := buildStatsTool(d)
	r := mustCall(t, tool, map[string]any{}).(map[string]any)
	if r["with_active_sprint"].(int) != 1 {
		t.Errorf("with_active_sprint: %v", r["with_active_sprint"])
	}
	if r["stale_active_count"].(int) != 1 {
		t.Errorf("stale_active_count: %v", r["stale_active_count"])
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
