package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit sets up a throwaway git repo in dir with one committed .go file.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
}

func TestReadGoFilesAtRev_RoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)

	// Commit an initial version.
	oldSrc := "package p\n\nfunc Handle(a int) int { return a }\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(oldSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "x.go"}, {"commit", "-qm", "v1"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Degrade the working tree (uncommitted).
	newSrc := "package p\n\nfunc Handle(a, b, c int) int { return a }\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(newSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// readGoFilesAtRev(HEAD) must return the *committed* (old) content, not the
	// working-tree version.
	files, err := readGoFilesAtRev(dir, "HEAD")
	if err != nil {
		t.Fatalf("readGoFilesAtRev: %v", err)
	}
	got, ok := files["x.go"]
	if !ok {
		t.Fatalf("x.go missing from rev files: %v", files)
	}
	if got != oldSrc {
		t.Errorf("expected committed content, got working-tree content:\n%q", got)
	}
}

func TestReadGoFilesAtRev_NotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir() // no git init
	if _, err := readGoFilesAtRev(dir, "HEAD"); err == nil {
		t.Error("expected an error for a non-git directory")
	}
}

// cliRegress with --base on a real repo should detect a regression end-to-end.
func TestCLIRegress_BaseDetectsRegression(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package p\n\nfunc F(a int) int { return a }\n")
	for _, args := range [][]string{{"add", "x.go"}, {"commit", "-qm", "v1"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Degrade: 1 param → 6 params (crosses the gate).
	write("package p\n\nfunc F(a, b, c, d, e, f int) int { return a }\n")

	var out, errBuf strings.Builder
	err := cliRegress([]string{"--base", "HEAD", "--new", dir, "--strict"}, &out, &errBuf)
	if err == nil {
		t.Errorf("--strict should fail when a regression crosses the gate; out=%q", out.String())
	}
	if !strings.Contains(out.String(), "params") {
		t.Errorf("expected a params regression in output, got: %q", out.String())
	}
}
