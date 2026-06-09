package registry

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/project"
)

func freshRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, dir
}

func sampleProject(slug string) *project.Project {
	return &project.Project{
		Slug:        slug,
		DisplayName: "Sample " + slug,
		Repository:  "shizukutanaka/" + slug,
		Stage:       project.StageActive,
		Priority:    3,
		Language:    "Go",
		Tags:        []string{"daemon"},
	}
}

func TestNew_EmptyDir(t *testing.T) {
	r, _ := freshRegistry(t)
	if got := r.List(); len(got) != 0 {
		t.Errorf("empty registry should have 0 projects, got %d", len(got))
	}
}

func TestNew_RequiresDir(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error("expected error")
	}
}

func TestNew_CreatesDir(t *testing.T) {
	parentDir := t.TempDir()
	newDir := filepath.Join(parentDir, "sub", "registry")
	if _, err := New(newDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Errorf("dir not created: %v", err)
	}
}

func TestAdd_Get_Update_Delete(t *testing.T) {
	r, dir := freshRegistry(t)
	p := sampleProject("mihari")
	if err := r.Add(p); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if p.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if _, err := os.Stat(filepath.Join(dir, "mihari.json")); err != nil {
		t.Errorf("file missing: %v", err)
	}
	got, err := r.Get("mihari")
	if err != nil {
		t.Fatal(err)
	}
	got.Priority = 5
	if err := r.Update(got); err != nil {
		t.Fatal(err)
	}
	got2, _ := r.Get("mihari")
	if got2.Priority != 5 {
		t.Errorf("Priority not updated: %d", got2.Priority)
	}
	if !got2.CreatedAt.Equal(p.CreatedAt) {
		t.Error("CreatedAt changed")
	}
	if err := r.Delete("mihari"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get("mihari"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAdd_DuplicateSlug(t *testing.T) {
	r, _ := freshRegistry(t)
	_ = r.Add(sampleProject("dup"))
	if err := r.Add(sampleProject("dup")); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestAdd_InvalidProject(t *testing.T) {
	r, _ := freshRegistry(t)
	if err := r.Add(&project.Project{Slug: "BAD"}); err == nil {
		t.Error("expected validation error")
	}
}

func TestUpdate_NotFound(t *testing.T) {
	r, _ := freshRegistry(t)
	if err := r.Update(sampleProject("ghost")); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdate_InvalidProject(t *testing.T) {
	r, _ := freshRegistry(t)
	_ = r.Add(sampleProject("valid"))
	bad := sampleProject("valid")
	bad.Repository = "" // required field
	if err := r.Update(bad); err == nil {
		t.Error("Update with invalid project should return error")
	}
}

func TestDelete_NotFound(t *testing.T) {
	r, _ := freshRegistry(t)
	if err := r.Delete("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestLoad_ContinuesOnInvalidProjectData(t *testing.T) {
	dir := t.TempDir()
	// Valid JSON, but missing required fields (empty repository) → Validate() fails
	invalid := `{"slug":"bad-slug-valid-json","display_name":"X","repository":"","stage":"active"}`
	_ = os.WriteFile(filepath.Join(dir, "invalid.json"), []byte(invalid), 0o600)
	good := `{"slug":"ok","display_name":"OK","repository":"x/ok","stage":"active"}`
	_ = os.WriteFile(filepath.Join(dir, "ok.json"), []byte(good), 0o600)
	r, _ := New(dir)
	// The invalid project should be skipped; good one should load
	if _, err := r.Get("ok"); err != nil {
		t.Errorf("valid project should load: %v", err)
	}
	if _, err := r.Get("bad-slug-valid-json"); err == nil {
		t.Error("invalid project should not be loaded")
	}
}

func TestList_SortedBySlug(t *testing.T) {
	r, _ := freshRegistry(t)
	_ = r.Add(sampleProject("zebra"))
	_ = r.Add(sampleProject("alpha"))
	_ = r.Add(sampleProject("mihari"))
	got := r.List()
	want := []string{"alpha", "mihari", "zebra"}
	for i, p := range got {
		if p.Slug != want[i] {
			t.Errorf("[%d] got %q want %q", i, p.Slug, want[i])
		}
	}
}

func TestCount(t *testing.T) {
	r, _ := freshRegistry(t)
	a := sampleProject("a")
	a.Stage = project.StageActive
	b := sampleProject("b")
	b.Stage = project.StageMaintenance
	c := sampleProject("c")
	c.Stage = project.StageArchived
	for _, p := range []*project.Project{a, b, c} {
		_ = r.Add(p)
	}
	counts := r.Count()
	if counts[project.StageActive] != 1 || counts[project.StageMaintenance] != 1 || counts[project.StageArchived] != 1 {
		t.Errorf("wrong counts: %v", counts)
	}
}

func TestFilter(t *testing.T) {
	r, _ := freshRegistry(t)
	a := sampleProject("a")
	a.Language = "Go"
	a.Tags = []string{"daemon"}
	b := sampleProject("b")
	b.Language = "Rust"
	b.Tags = []string{"daemon"}
	c := sampleProject("c")
	c.Language = "Python"
	c.Tags = []string{"experiment"}
	for _, p := range []*project.Project{a, b, c} {
		_ = r.Add(p)
	}
	gos := r.Filter(func(p *project.Project) bool { return p.Language == "Go" })
	if len(gos) != 1 || gos[0].Slug != "a" {
		t.Errorf("got %v", slugs(gos))
	}
	daemons := r.Filter(func(p *project.Project) bool { return p.HasTag("daemon") })
	if len(daemons) != 2 {
		t.Errorf("expected 2 daemons, got %d", len(daemons))
	}
}

func slugs(ps []*project.Project) []string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.Slug
	}
	return s
}

func TestPersistence_Reopen(t *testing.T) {
	dir := t.TempDir()
	r1, _ := New(dir)
	p := sampleProject("persistent")
	p.Notes = "important"
	_ = r1.Add(p)
	r2, _ := New(dir)
	got, err := r2.Get("persistent")
	if err != nil {
		t.Fatal(err)
	}
	if got.Notes != "important" {
		t.Errorf("Notes lost: %q", got.Notes)
	}
}

func TestLoad_ContinuesOnBadFile(t *testing.T) {
	dir := t.TempDir()
	good := `{"slug":"good","display_name":"Good","repository":"x/good","stage":"active"}`
	_ = os.WriteFile(filepath.Join(dir, "good.json"), []byte(good), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600)
	r, err := New(dir)
	if err == nil {
		t.Log("expected warning for broken")
	}
	if _, gerr := r.Get("good"); gerr != nil {
		t.Errorf("good should load: %v", gerr)
	}
}

func TestPersist_ConcurrentSafe(t *testing.T) {
	r, _ := freshRegistry(t)
	_ = r.Add(sampleProject("conc"))
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p, _ := r.Get("conc")
			p.Priority = (n % 5) + 1
			_ = r.Update(p)
		}(i)
	}
	wg.Wait()
	got, _ := r.Get("conc")
	if got.Priority < 1 || got.Priority > 5 {
		t.Errorf("OOB: %d", got.Priority)
	}
}

func TestGet_ReturnsClone(t *testing.T) {
	r, _ := freshRegistry(t)
	p := sampleProject("clone")
	p.Tags = []string{"original"}
	_ = r.Add(p)
	got, _ := r.Get("clone")
	got.Tags[0] = "MODIFIED"
	got2, _ := r.Get("clone")
	if got2.Tags[0] != "original" {
		t.Errorf("leaked: %q", got2.Tags[0])
	}
}

func TestUpdate_PreservesCreatedAt(t *testing.T) {
	r, _ := freshRegistry(t)
	p := sampleProject("ct")
	_ = r.Add(p)
	original := p.CreatedAt
	time.Sleep(2 * time.Millisecond)
	got, _ := r.Get("ct")
	got.Priority = 5
	_ = r.Update(got)
	got2, _ := r.Get("ct")
	if !got2.CreatedAt.Equal(original) {
		t.Error("CreatedAt changed")
	}
	if !got2.UpdatedAt.After(original) {
		t.Error("UpdatedAt not after CreatedAt")
	}
}

// TestUpdate_PersistFails covers the atomicfile.Write error path in persist.
// After a successful Add, we replace the .json file with a directory, which
// causes the atomic Rename inside atomicfile.Write to fail on Update.
func TestUpdate_PersistFails(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := sampleProject("overwrite-test")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	// Replace the project JSON file with a directory of the same name.
	// atomicfile.Write will create a temp file, then Rename(temp, dir) → error.
	jsonPath := filepath.Join(dir, "overwrite-test.json")
	if err := os.Remove(jsonPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(jsonPath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(jsonPath) })

	got, _ := r.Get("overwrite-test")
	got.Notes = "modified"
	if err := r.Update(got); err == nil {
		t.Error("expected persist error when target path is a directory, got nil")
	}
}

func TestGet_ClonesSprint(t *testing.T) {
	r, _ := freshRegistry(t)
	p := sampleProject("ws")
	p.Sprint = &project.Sprint{
		Phase:      project.PhaseBuild,
		Goal:       "implement X",
		Milestones: []project.Milestone{{Title: "design done", Done: true}},
	}
	_ = r.Add(p)
	got, _ := r.Get("ws")
	got.Sprint.Goal = "MODIFIED"
	got.Sprint.Milestones[0].Title = "MODIFIED"
	got2, _ := r.Get("ws")
	if got2.Sprint.Goal != "implement X" {
		t.Errorf("Sprint mutation leaked: %q", got2.Sprint.Goal)
	}
	if got2.Sprint.Milestones[0].Title != "design done" {
		t.Errorf("Milestone mutation leaked: %q", got2.Sprint.Milestones[0].Title)
	}
}
