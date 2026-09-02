package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// ─── Detect: .git ディレクトリ ─────────────────────────────

func TestDetect_GitInSameDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, found, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected gitFound=true")
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Errorf("workspace: got %q, want %q", got, want)
	}
}

func TestDetect_GitInParent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "src", "internal")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, found, err := Detect(sub)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected gitFound=true")
	}
	want, _ := filepath.EvalSymlinks(root)
	if got != want {
		t.Errorf("workspace: got %q, want %q (expected to climb to root)", got, want)
	}
}

func TestDetect_GitAsFile(t *testing.T) {
	// git worktree / submodule では .git は file(gitfile pointer)
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /path/to/real/git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, found, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected gitFound=true for .git file")
	}
}

func TestDetect_NotFound_FallsBackToStartDir(t *testing.T) {
	dir := t.TempDir()
	got, found, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected gitFound=false")
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Errorf("fallback: got %q, want %q", got, want)
	}
}

// ─── 入力 validation ─────────────────────────────────────────

func TestDetect_EmptyStartDirError(t *testing.T) {
	_, _, err := Detect("")
	if err == nil {
		t.Error("expected error for empty startDir")
	}
}

// ─── MaxDepth 保護 ──────────────────────────────────────────

func TestDetect_DoesNotInfiniteLoopAtRoot(t *testing.T) {
	// .git のない root から開始 → MaxDepth で停止して fallback
	got, found, err := Detect("/")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Skip("test environment has .git at root (unusual); skipping")
	}
	if got == "" {
		t.Error("workspace should be set to startDir even when not found")
	}
}

// ─── 絶対パス変換 ──────────────────────────────────────────

func TestDetect_RelativePathConvertedToAbsolute(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	got, found, err := Detect(".")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected found")
	}
	if !filepath.IsAbs(got) {
		t.Errorf("workspace should be absolute, got %q", got)
	}
}

// ─── DetectCWD ──────────────────────────────────────────────

func TestDetectCWD(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	got, found, err := DetectCWD()
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected found")
	}
	if got == "" {
		t.Error("workspace empty")
	}
}

// ─── 階層上昇 ──────────────────────────────────────────────

func TestDetect_DeepNesting(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := root
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		deep = filepath.Join(deep, name)
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got, found, err := Detect(deep)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("should find .git through 5 levels")
	}
	want, _ := filepath.EvalSymlinks(root)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ─── error-path coverage ─────────────────────────────────────

func TestDetect_NonexistentDir_SymlinkFallback(t *testing.T) {
	// EvalSymlinks fails for a path that doesn't exist; Detect must fall back
	// to the absolute path and return it as the workspace (gitFound=false)
	// rather than erroring — daemon startup must survive a bogus cwd.
	ghost := filepath.Join(t.TempDir(), "does", "not", "exist")
	ws, gitFound, err := Detect(ghost)
	if err != nil {
		t.Fatalf("nonexistent dir should not error, got %v", err)
	}
	if gitFound {
		t.Error("gitFound should be false for a nonexistent path")
	}
	if ws != ghost {
		t.Errorf("workspace = %q, want fallback to %q", ws, ghost)
	}
}

func TestDetectCWD_DeletedCWD(t *testing.T) {
	// When the process cwd has been removed, os.Getwd fails and DetectCWD
	// must surface that error instead of panicking or guessing.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()

	doomed := filepath.Join(t.TempDir(), "doomed")
	if err := os.Mkdir(doomed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(doomed); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(doomed); err != nil {
		t.Fatal(err)
	}

	if _, _, err := DetectCWD(); err == nil {
		t.Skip("os.Getwd still resolved a deleted cwd on this platform")
	}
}
