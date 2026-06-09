package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ─── New ─────────────────────────────────────────────────────

func TestNew_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("state dir not created: %v", err)
	}
	if s.Path() == "" {
		t.Error("Path() empty")
	}
}

func TestNew_EmptyDirError(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error("expected error for empty dir")
	}
}

// ─── Save / Load roundtrip ──────────────────────────────────

func TestSaveLoad_BasicRoundtrip(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	in := &Context{
		Version:    1,
		SavedAt:    now,
		SavedBy:    "claude_code",
		Workspace:  "/home/m/yagura",
		Branch:     "main",
		LastCommit: "a1b2c3d4",
		ActiveFiles: []string{"internal/scanner/scanner.go", "Plan.md"},
		PlanMdStep:  "Phase 2 — Implementation",
		OpenTodos: []Todo{
			{File: "internal/x/y.go", Line: 42, Kind: "TODO", Text: "implement retry"},
		},
		FreeNotes: "Switching to Windsurf because Claude Code 5h window exhausted",
	}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if out.SavedBy != in.SavedBy {
		t.Errorf("SavedBy: got %q, want %q", out.SavedBy, in.SavedBy)
	}
	if out.Workspace != in.Workspace {
		t.Errorf("Workspace: %q vs %q", out.Workspace, in.Workspace)
	}
	if len(out.OpenTodos) != 1 || out.OpenTodos[0].Text != "implement retry" {
		t.Errorf("OpenTodos: %+v", out.OpenTodos)
	}
	if out.FreeNotes != in.FreeNotes {
		t.Errorf("FreeNotes: %q vs %q", out.FreeNotes, in.FreeNotes)
	}
}

func TestSave_MissingWorkspaceError(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(&Context{Workspace: ""}); err == nil {
		t.Error("expected error for missing workspace")
	}
}

func TestSave_NilContextError(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(nil); err == nil {
		t.Error("expected error for nil context")
	}
}

func TestSave_VersionDefaultedTo1(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(&Context{Workspace: "/x"})
	out, _ := s.Load()
	if out.Version != 1 {
		t.Errorf("Version: got %d, want 1", out.Version)
	}
}

func TestSave_SavedAtAutoSet(t *testing.T) {
	s := newTestStore(t)
	before := time.Now().Add(-1 * time.Second).UTC()
	_ = s.Save(&Context{Workspace: "/x"})
	out, _ := s.Load()
	if out.SavedAt.Before(before) {
		t.Errorf("SavedAt not auto-set or too old: %v", out.SavedAt)
	}
}

// ─── Load: not-saved ────────────────────────────────────────

func TestLoad_NotSavedReturnsErrNotSaved(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Load()
	if !errors.Is(err, ErrNotSaved) {
		t.Errorf("expected ErrNotSaved, got %v", err)
	}
}

// ─── Clear ───────────────────────────────────────────────────

func TestClear_RemovesFile(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(&Context{Workspace: "/x"})
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load()
	if !errors.Is(err, ErrNotSaved) {
		t.Errorf("after Clear, Load should return ErrNotSaved, got %v", err)
	}
}

func TestClear_IdempotentWhenNotExists(t *testing.T) {
	s := newTestStore(t)
	if err := s.Clear(); err != nil {
		t.Errorf("Clear on empty should be nil, got %v", err)
	}
}

// ─── Overwrite ──────────────────────────────────────────────

func TestSave_Overwrite(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(&Context{Workspace: "/x", FreeNotes: "first"})
	_ = s.Save(&Context{Workspace: "/x", FreeNotes: "second"})
	out, _ := s.Load()
	if out.FreeNotes != "second" {
		t.Errorf("not overwritten: %q", out.FreeNotes)
	}
}

// ─── Atomic write: leftover tmp cleanup ─────────────────────

func TestSave_NoLeftoverTmpFile(t *testing.T) {
	s := newTestStore(t)
	_ = s.Save(&Context{Workspace: "/x"})
	dir := filepath.Dir(s.Path())
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}

// ─── Load broken JSON ────────────────────────────────────────

func TestLoad_BrokenJSON(t *testing.T) {
	s := newTestStore(t)
	// Write broken JSON directly to the handoff file path.
	if err := os.WriteFile(s.Path(), []byte("{not json}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load()
	if err == nil {
		t.Error("broken JSON should return error")
	}
}

// ─── New: MkdirAll failure ────────────────────────────────────

func TestNew_MkdirFails(t *testing.T) {
	// Place a regular file at the parent so MkdirAll fails
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(filepath.Join(blocker, "state"))
	if err == nil {
		t.Error("expected error when parent path is a file")
	}
}

// ─── Save / Load / Clear: filesystem error paths ─────────────

// TestSave_WriteFails covers the atomicfile.Write error branch in Save by
// making the handoff.json path a directory (Rename inside Write fails).
func TestSave_WriteFails(t *testing.T) {
	s := newTestStore(t)
	// Replace the handoff.json target with a directory of the same name.
	if err := os.Mkdir(s.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	err := s.Save(&Context{Workspace: "/ws"})
	if err == nil {
		t.Error("expected save error when target path is a directory, got nil")
	}
}

// TestLoad_OpenFailsNonNotExist covers the non-ErrNotExist branch of Load's
// os.Open error handling: the path is a directory, so Open succeeds but
// ReadAll fails (read on a directory).
func TestLoad_PathIsDir(t *testing.T) {
	s := newTestStore(t)
	if err := os.Mkdir(s.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load()
	if err == nil {
		t.Error("expected load error when path is a directory, got nil")
	}
	if errors.Is(err, ErrNotSaved) {
		t.Error("a directory at the path should not be reported as ErrNotSaved")
	}
}

// TestClear_RemoveFailsNonNotExist covers Clear's non-ErrNotExist error branch
// by making the path a non-empty directory (os.Remove fails on non-empty dir).
func TestClear_NonEmptyDir(t *testing.T) {
	s := newTestStore(t)
	if err := os.Mkdir(s.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	// Put a file inside so os.Remove(dir) fails with ENOTEMPTY.
	if err := os.WriteFile(filepath.Join(s.Path(), "child"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := s.Clear()
	if err == nil {
		t.Error("expected clear error when path is a non-empty directory, got nil")
	}
}

// ─── Save: pre-set SavedAt is preserved (UTC conversion) ─────

func TestSave_PreSetSavedAt(t *testing.T) {
	s := newTestStore(t)
	preSet := time.Date(2025, 1, 15, 12, 0, 0, 0, time.FixedZone("JST", 9*3600))
	ctx := &Context{
		Workspace: "/workspace",
		SavedAt:   preSet,
	}
	if err := s.Save(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Pre-set SavedAt should be preserved (converted to UTC)
	if loaded.SavedAt.UTC() != preSet.UTC() {
		t.Errorf("SavedAt not preserved: got %v, want %v", loaded.SavedAt.UTC(), preSet.UTC())
	}
}
