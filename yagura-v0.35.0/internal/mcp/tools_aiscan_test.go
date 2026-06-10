// tools_aiscan_test.go: scanProjectAICode (release radar's scan_code=true path).
//
// The function was at 0% coverage: it only runs when an MCP client passes
// scan_code=true to yagura_release_radar AND the registry has a project with a
// LocalPath containing source files — no test wired all of that. The walk
// logic carries real policy (extension allowlist, hidden/vendor skip, file
// size cap, file count cap) that should be pinned independently of the tool.
package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanProjectAICode_EmptyDir(t *testing.T) {
	res := scanProjectAICode(t.TempDir())
	if res.FilesScanned != 0 {
		t.Errorf("FilesScanned = %d for empty dir, want 0", res.FilesScanned)
	}
	if res.RiskScore != 0 || res.HasCritical {
		t.Errorf("empty dir should yield zero Result, got risk=%d critical=%v",
			res.RiskScore, res.HasCritical)
	}
}

func TestScanProjectAICode_NonexistentDir(t *testing.T) {
	// filepath.Walk error is swallowed → zero Result, no panic.
	res := scanProjectAICode(filepath.Join(t.TempDir(), "no-such-dir"))
	if res.FilesScanned != 0 {
		t.Errorf("FilesScanned = %d for nonexistent dir, want 0", res.FilesScanned)
	}
}

func TestScanProjectAICode_ScansGoFile(t *testing.T) {
	dir := t.TempDir()
	src := "package x\n\n// AI-generated\nfunc f() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res := scanProjectAICode(dir)
	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", res.FilesScanned)
	}
}

func TestScanProjectAICode_SkipsNonSourceAndHidden(t *testing.T) {
	dir := t.TempDir()
	// Non-source extension → skipped.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Hidden file → skipped (basename starts with ".").
	if err := os.WriteFile(filepath.Join(dir, ".hidden.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// vendor/ and node_modules/ files: the dir itself isn't pruned (the walk
	// skips by basename), but files named exactly "vendor"/"node_modules"
	// have no source extension anyway. Place a source file inside vendor to
	// document the current behavior: basename of the *file* is what's checked.
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "ok.py"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := scanProjectAICode(dir)
	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1 (only sub/ok.py)", res.FilesScanned)
	}
}

func TestScanProjectAICode_SkipsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	big := "// filler\n" + strings.Repeat("x", 257*1024) // > 256KB cap
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := scanProjectAICode(dir)
	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1 (big.go over the 256KB cap)", res.FilesScanned)
	}
}

func TestScanProjectAICode_FileCountCap(t *testing.T) {
	dir := t.TempDir()
	// 70 source files > maxFiles=64. The cap stops collection at 64.
	for i := 0; i < 70; i++ {
		name := filepath.Join(dir, "f"+string(rune('a'+i/26))+string(rune('a'+i%26))+".go")
		if err := os.WriteFile(name, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := scanProjectAICode(dir)
	if res.FilesScanned > 64 {
		t.Errorf("FilesScanned = %d, want <= 64 (runaway-walk cap)", res.FilesScanned)
	}
	if res.FilesScanned == 0 {
		t.Error("expected some files scanned")
	}
}
