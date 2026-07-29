package srcfiles

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree materializes {relpath: content} under a fresh temp dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestReadGo_CollectsOnlyGoFiles(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.go":          "package main",
		"lib/util.go":      "package lib",
		"README.md":        "# docs",
		"script.py":        "print(1)",
		"lib/util_test.go": "package lib",
	})
	res, err := ReadGo(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Files["main.go"]; !ok {
		t.Errorf("main.go missing: %v", keys(res.Files))
	}
	if _, ok := res.Files[filepath.Join("lib", "util.go")]; !ok {
		t.Errorf("lib/util.go missing: %v", keys(res.Files))
	}
	if _, ok := res.Files["README.md"]; ok {
		t.Errorf("non-Go file was collected")
	}
	if res.Truncated {
		t.Errorf("small tree should not truncate")
	}
}

func TestReadLimited_SkipsVendorAndGit(t *testing.T) {
	root := writeTree(t, map[string]string{
		"main.go":                  "package main",
		"vendor/dep/dep.go":        "package dep",
		"node_modules/x/y.go":      "package y",
		".git/hooks/pre-commit.go": "package hooks",
		".yagura/cache/c.go":       "package cache",
	})
	res, err := ReadGo(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 {
		t.Errorf("expected only main.go, got %v", keys(res.Files))
	}
}

func TestReadLimited_TruncatesAtFileCap(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 10; i++ {
		files[string(rune('a'+i))+".go"] = "package p"
	}
	root := writeTree(t, files)
	res, err := ReadLimited(root, 3, 1<<20, func(n string) bool { return filepath.Ext(n) == ".go" })
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Errorf("expected Truncated=true when the file cap is hit")
	}
	if len(res.Files) > 3 {
		t.Errorf("collected %d files, cap was 3", len(res.Files))
	}
}

func TestReadLimited_TruncatesAtByteCap(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.go": "package a // " + string(make([]byte, 500)),
		"b.go": "package b // " + string(make([]byte, 500)),
	})
	res, err := ReadLimited(root, 100, 200, func(n string) bool { return filepath.Ext(n) == ".go" })
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Errorf("expected Truncated=true when the byte cap is hit")
	}
}

func TestIncomplete_ReportsTruncatedOrUnreadable(t *testing.T) {
	if (Result{}).Incomplete() {
		t.Errorf("clean result should not be Incomplete")
	}
	if !(Result{Truncated: true}).Incomplete() {
		t.Errorf("truncated result must be Incomplete")
	}
	if !(Result{Unreadable: []string{"x.go"}}).Incomplete() {
		t.Errorf("unreadable result must be Incomplete")
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
