package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/hookreceiver"
	"github.com/shizukutanaka/yagura/internal/mcp"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/registry"
)

// newHookReceiverWithEvents builds a receiver wired to reg and feeds it the given
// raw hook payloads (POSTed to /hooks/agent), returning the populated receiver.
func newHookReceiverWithEvents(t *testing.T, reg *registry.Registry, payloads ...string) *hookreceiver.Receiver {
	t.Helper()
	hr, err := hookreceiver.NewReceiver(filepath.Join(t.TempDir(), "hooks.jsonl"), &registryLookup{reg: reg}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range payloads {
		hr.Handle(httptest.NewRecorder(), httptest.NewRequest("POST", "/hooks/agent", strings.NewReader(p)))
	}
	return hr
}

// ─── registryLookup.ResolveByPath ────────────────────────────

func TestResolveByPath_PrefixMatch(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = reg.Add(&project.Project{
		Slug: "breeze", DisplayName: "Breeze", Repository: "o/breeze",
		LocalPath: "/home/user/breeze", Stage: project.StageActive,
	})
	l := &registryLookup{reg: reg}

	// cwd inside the project's LocalPath → resolves to slug
	if slug, ok := l.ResolveByPath("/home/user/breeze/internal/x"); !ok || slug != "breeze" {
		t.Errorf("ResolveByPath in-tree: got (%q, %v), want (breeze, true)", slug, ok)
	}
	// cwd outside any project → no match
	if slug, ok := l.ResolveByPath("/var/other"); ok || slug != "" {
		t.Errorf("ResolveByPath out-of-tree: got (%q, %v), want (\"\", false)", slug, ok)
	}
	// empty cwd → no match
	if _, ok := l.ResolveByPath(""); ok {
		t.Error("ResolveByPath empty cwd should not match")
	}
}

func TestResolveByPath_NilGuards(t *testing.T) {
	// nil receiver
	var l *registryLookup
	if _, ok := l.ResolveByPath("/x"); ok {
		t.Error("nil *registryLookup should return false")
	}
	// nil registry inside
	l2 := &registryLookup{reg: nil}
	if _, ok := l2.ResolveByPath("/x"); ok {
		t.Error("registryLookup with nil reg should return false")
	}
}

func TestResolveByPath_SkipsProjectsWithoutLocalPath(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// project with no LocalPath must never match a prefix
	_ = reg.Add(&project.Project{
		Slug: "nolocal", DisplayName: "NoLocal", Repository: "o/nolocal",
		Stage: project.StageActive,
	})
	l := &registryLookup{reg: reg}
	if _, ok := l.ResolveByPath("/anything"); ok {
		t.Error("project without LocalPath should not match any cwd")
	}
}

// ─── hookActivityAdapter.ProjectActivity ─────────────────────

func TestHookActivityAdapter_ProjectActivity(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proj := "/home/user/breeze"
	_ = reg.Add(&project.Project{
		Slug: "breeze", DisplayName: "Breeze", Repository: "o/breeze",
		LocalPath: proj, Stage: project.StageActive,
	})
	hr := newHookReceiverWithEvents(t, reg,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"`+proj+`"}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"`+proj+`"}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Read","cwd":"`+proj+`"}`,
	)
	a := hookActivityAdapter{hr: hr, srv: mcp.New("", nil)}

	act, ok := a.ProjectActivity("breeze")
	if !ok {
		t.Fatal("expected activity for a project with recorded events")
	}
	if act.Total < 3 {
		t.Errorf("Total = %d, want >= 3", act.Total)
	}
	// Bash appeared twice, Read once → top tool must be Bash.
	if act.TopTool != "Bash" {
		t.Errorf("TopTool = %q, want Bash", act.TopTool)
	}
}

func TestHookActivityAdapter_ProjectActivity_NoEvents(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hr := newHookReceiverWithEvents(t, reg) // no events
	a := hookActivityAdapter{hr: hr, srv: mcp.New("", nil)}
	if _, ok := a.ProjectActivity("ghost"); ok {
		t.Error("expected ok=false for a project with no recorded events")
	}
}

// ─── hookActivityAdapter.ProjectActivityDetail ───────────────

func TestHookActivityAdapter_ProjectActivityDetail(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proj := "/home/user/breeze"
	_ = reg.Add(&project.Project{
		Slug: "breeze", DisplayName: "Breeze", Repository: "o/breeze",
		LocalPath: proj, Stage: project.StageActive,
	})
	hr := newHookReceiverWithEvents(t, reg,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"`+proj+`"}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","cwd":"`+proj+`"}`,
		`{"agent":"gemini_cli","event":"beforeToolCall","tool":"Read","cwd":"`+proj+`"}`,
	)
	srv := mcp.New("", nil)
	srv.SetHookReceiver(hr)
	a := hookActivityAdapter{hr: hr, srv: srv}

	detail, ok := a.ProjectActivityDetail("breeze")
	if !ok {
		t.Fatal("expected detail for a project with recorded events")
	}
	if detail.Slug != "breeze" {
		t.Errorf("Slug = %q, want breeze", detail.Slug)
	}
	if detail.ToolInvocations == 0 {
		t.Errorf("ToolInvocations should be > 0, got %d", detail.ToolInvocations)
	}
	if len(detail.ByTool) == 0 {
		t.Errorf("ByTool should be populated, got empty")
	}
}

func TestHookActivityAdapter_ProjectActivityDetail_NilServer(t *testing.T) {
	a := hookActivityAdapter{hr: nil, srv: nil}
	if _, ok := a.ProjectActivityDetail("breeze"); ok {
		t.Error("expected ok=false when srv is nil")
	}
}

func TestHookActivityAdapter_ProjectActivityDetail_NoEvents(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hr := newHookReceiverWithEvents(t, reg) // no events
	srv := mcp.New("", nil)
	srv.SetHookReceiver(hr)
	a := hookActivityAdapter{hr: hr, srv: srv}
	if _, ok := a.ProjectActivityDetail("ghost"); ok {
		t.Error("expected ok=false for a project with no recorded events")
	}
}

// ─── projectScanItems ────────────────────────────────────────

func TestProjectScanItems_AllFields(t *testing.T) {
	p := &project.Project{
		Slug:        "breeze",
		DisplayName: "Breeze",
		Notes:       "some notes here",
		Tags:        []string{"go", "cli"},
		Sprint: &project.Sprint{
			Goal: "ship v1",
			Milestones: []project.Milestone{
				{Title: "design done"},
				{Title: "impl done"},
			},
		},
	}
	items := projectScanItems(p)
	// display_name + notes + tags + sprint.goal + 2 milestones = 6 items
	if len(items) != 6 {
		t.Fatalf("expected 6 scan items, got %d: %+v", len(items), items)
	}
	// every source is prefixed with the slug
	for _, it := range items {
		if !strings.HasPrefix(it.Source, "breeze:") {
			t.Errorf("source %q should be prefixed with slug", it.Source)
		}
	}
}

func TestProjectScanItems_SkipsEmptyFields(t *testing.T) {
	p := &project.Project{
		Slug:        "minimal",
		DisplayName: "Minimal",
		// no notes, no tags, no sprint
	}
	items := projectScanItems(p)
	// only display_name is non-empty
	if len(items) != 1 {
		t.Errorf("expected 1 item (display_name only), got %d: %+v", len(items), items)
	}
}

func TestProjectScanItems_SprintNoMilestones(t *testing.T) {
	p := &project.Project{
		Slug:        "s",
		DisplayName: "S",
		Sprint:      &project.Sprint{Goal: "goal only"},
	}
	items := projectScanItems(p)
	// display_name + sprint.goal = 2
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d: %+v", len(items), items)
	}
}

// ─── readWorkflowFiles ───────────────────────────────────────

func TestReadWorkflowFiles_FiltersAndSkipsDirs(t *testing.T) {
	dir := t.TempDir()
	// valid workflow files
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte("name: ci"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release.yaml"), []byte("name: release"), 0o644); err != nil {
		t.Fatal(err)
	}
	// non-workflow file → skipped
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// subdirectory → skipped
	if err := os.Mkdir(filepath.Join(dir, "nested.yml"), 0o755); err != nil {
		t.Fatal(err)
	}
	files, err := readWorkflowFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 workflow files, got %d: %v", len(files), files)
	}
	if files["ci.yml"] != "name: ci" || files["release.yaml"] != "name: release" {
		t.Errorf("file contents wrong: %v", files)
	}
	if _, ok := files["README.md"]; ok {
		t.Error("README.md should be filtered out")
	}
}

func TestReadWorkflowFiles_MissingDir(t *testing.T) {
	_, err := readWorkflowFiles(filepath.Join(t.TempDir(), "no-such-dir"))
	if err == nil {
		t.Error("missing directory should return an error")
	}
}

// ─── countMCPServers ─────────────────────────────────────────

func TestCountMCPServers(t *testing.T) {
	// two servers
	n := countMCPServers(`{"mcpServers":{"a":{"command":"x"},"b":{"command":"y"}}}`)
	if n != 2 {
		t.Errorf("expected 2 servers, got %d", n)
	}
	// none
	if got := countMCPServers(`{"mcpServers":{}}`); got != 0 {
		t.Errorf("empty mcpServers should be 0, got %d", got)
	}
	// missing key
	if got := countMCPServers(`{"other":1}`); got != 0 {
		t.Errorf("missing mcpServers should be 0, got %d", got)
	}
	// invalid JSON → 0, no panic
	if got := countMCPServers(`{not json`); got != 0 {
		t.Errorf("invalid JSON should be 0, got %d", got)
	}
}
