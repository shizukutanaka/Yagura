package srcfiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// TestReadLimited_ReportsHowIncompleteTheScanWas は「不完全である」だけでなく
// **どれだけ不完全か** を報告することを要求する。
//
// 発見の経緯: kubernetes(17,878 Go ファイル)を測ったとき、MCP の応答は
// `incomplete: true` の 1 語だった。実際に読めていたのは 1,000 件 = 5.6% だが、
// 利用者にはそれが 99% なのか 5% なのか判別する手段が無い。真偽値は
// 「クリーンだと結論するな」とは言えても「どれだけ信用してよいか」を言えない。
func TestReadLimited_ReportsHowIncompleteTheScanWas(t *testing.T) {
	dir := t.TempDir()
	const total = 25
	for i := 0; i < total; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%02d.go", i))
		if err := os.WriteFile(p, []byte("package p\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 対象外の拡張子は Matched に数えない(accept を通った母数であること)。
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	const cap = 10
	res, err := ReadLimited(dir, cap, DefaultMaxBytes, func(n string) bool {
		return strings.HasSuffix(n, ".go")
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != cap {
		t.Fatalf("cap should bound what is read: got %d want %d", len(res.Files), cap)
	}
	if !res.Truncated {
		t.Error("hitting the cap must set Truncated")
	}
	if res.Matched != total {
		t.Errorf("Matched must count every accepted file, read or not: got %d want %d", res.Matched, total)
	}
	// 完全スキャンでは母数と読了数が一致する——ここが崩れると
	// files_total は「上限そのもの」を返すだけの飾りになる。
	full, err := ReadLimited(dir, 1000, DefaultMaxBytes, func(n string) bool {
		return strings.HasSuffix(n, ".go")
	})
	if err != nil {
		t.Fatal(err)
	}
	if full.Matched != len(full.Files) || full.Matched != total {
		t.Errorf("a complete scan must report Matched == len(Files) == %d, got %d/%d",
			total, full.Matched, len(full.Files))
	}
	if full.Incomplete() {
		t.Error("a complete scan must not report Incomplete")
	}
}

// TestReadLimited_SaysWhichCapBoundTheScan は「どちらの上限で切れたか」を
// 報告することを要求する。
//
// 発見の経緯(v1.87.0): kubernetes(13,424 Go ファイル)で上限を掃引したところ、
// max_files を 5,000 → 10,000 → 25,000 と上げても読めるのは **3,843 件のまま**
// だった。先に効いていたのは 50 MB のバイト上限だからである。
//
// 利用者から見ると、文書化された摘み(max_files)を上げても `incomplete: true` の
// ままで、**なぜ効かないのかを知る手段が無い**。これは v1.86.0 で直した
// 「履歴が大きすぎる → max_commits を下げよ」と同じ型の欠陥——
// **効かない対処を指す診断**。同じ型を 3 度目に踏まないために形にする。
func TestReadLimited_SaysWhichCapBoundTheScan(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		body := "package p\n// " + strings.Repeat("x", 500) + "\n"
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d.go", i)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	accept := func(n string) bool { return strings.HasSuffix(n, ".go") }

	// ファイル数で切れた場合。
	byFiles, err := ReadLimited(dir, 5, DefaultMaxBytes, accept)
	if err != nil {
		t.Fatal(err)
	}
	if !byFiles.Truncated || byFiles.TruncatedBy != TruncatedByFiles {
		t.Errorf("hitting the file cap must be attributed to %q, got %q (truncated=%v)",
			TruncatedByFiles, byFiles.TruncatedBy, byFiles.Truncated)
	}

	// バイト数で切れた場合。ファイル上限は十分大きいので、こちらが binding。
	byBytes, err := ReadLimited(dir, 1000, 2000, accept)
	if err != nil {
		t.Fatal(err)
	}
	if !byBytes.Truncated || byBytes.TruncatedBy != TruncatedByBytes {
		t.Errorf("hitting the byte cap must be attributed to %q, got %q (truncated=%v)",
			TruncatedByBytes, byBytes.TruncatedBy, byBytes.Truncated)
	}
	// 「max_files を上げれば直る」と誤解させないことがこのテストの本体。
	if byBytes.TruncatedBy == TruncatedByFiles {
		t.Error("a byte-bound scan must NOT be reported as file-bound: raising max_files " +
			"would change nothing and the caller would have no way to discover that")
	}

	// 完全スキャンでは空(常に何か言う報告器は何も報告していない)。
	full, err := ReadLimited(dir, 1000, DefaultMaxBytes, accept)
	if err != nil {
		t.Fatal(err)
	}
	if full.Truncated || full.TruncatedBy != "" {
		t.Errorf("a complete scan must attribute nothing, got %q", full.TruncatedBy)
	}
}
