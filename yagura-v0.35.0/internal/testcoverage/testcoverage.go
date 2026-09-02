// Package testcoverage は source-test 対応関係を検出する。
//
// 動機 (v0.26.0):
//
//	m's harness G0.7 INVARIANT は「AI 生成物は **テスト通過** + 人間確認が必須」と
//	明記。v0.25 で risk pattern は検出できるようになったが、
//	"AI 生成箇所に対応 test が存在するか" は audit できていなかった。
//
//	本 package は language-aware な test file 検出を提供し、aiverify と組合せて
//	"untested AI risk" を機械的に flag する。
//
// 設計判断 (ADR-0001 ゼロ依存):
//   - regex / 文字列操作のみ(stdlib)
//   - 言語ごとの test 命名慣習を encode (Go/TS/JS/Python/Rust/Java)
//   - Rust は content scan で #[cfg(test)] を検出(filename だけでは不明)
//   - Inline test (Go の internal test や Python の doctest) は別途
//
// 性能:
//   - O(N + M log M), N=files, M=test 候補マップ
//   - 大規模 monorepo (10K ファイル) で ~100ms 実測予測
package testcoverage

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// FileStatus は 1 ファイルの test 検出結果。
type FileStatus struct {
	Path      string `json:"path"`
	Language  string `json:"language,omitempty"`
	IsTest    bool   `json:"is_test,omitempty"`    // この file 自体が test か
	HasTest   bool   `json:"has_test,omitempty"`   // 対応 test が input に存在するか
	TestPath  string `json:"test_path,omitempty"`  // 検出された test の path
	HasInline bool   `json:"has_inline,omitempty"` // Rust の #[cfg(test)] 等 inline test
}

// AuditResult は集計。
type AuditResult struct {
	FilesScanned    int                  `json:"files_scanned"`
	TestFiles       int                  `json:"test_files"`
	SourceFiles     int                  `json:"source_files"`
	SourcesWithTest int                  `json:"sources_with_test"`
	SourcesNoTest   int                  `json:"sources_no_test"`
	CoverageRatio   float64              `json:"coverage_ratio"` // 0.0-1.0
	ByLanguage      map[string]LangStats `json:"by_language,omitempty"`
	UntestedFiles   []string             `json:"untested_files,omitempty"` // test なし source の path
}

// LangStats は言語別集計。
type LangStats struct {
	Sources       int     `json:"sources"`
	Tests         int     `json:"tests"`
	WithTest      int     `json:"with_test"`
	CoverageRatio float64 `json:"coverage_ratio"`
}

// rustCfgTestRe は Rust の inline test 検出。
//
// 一般形: #[cfg(test)] / #[cfg(feature = "test")] / #[cfg(any(test, ...))]
var rustCfgTestRe = regexp.MustCompile(`#\[cfg\s*\(\s*(?:test\b|any\([^)]*\btest\b|feature\s*=\s*"test")`)

// pythonDoctestRe は Python の doctest 検出(行頭 >>> )。
var pythonDoctestRe = regexp.MustCompile(`(?m)^\s*>>>\s+`)

// IsTestFile は path から test file かを判定する。
//
// 各言語の慣習:
//
//	Go     : *_test.go
//	TS/JS  : *.test.ts/tsx/js/jsx, *.spec.ts/tsx/js/jsx, *__tests__/*
//	Python : test_*.py, *_test.py, tests/*.py
//	Rust   : tests/*.rs (integration test dir)
//	Java   : *Test.java, *IT.java, *Tests.java
func IsTestFile(p string) bool {
	base := path.Base(p)
	dir := path.Dir(p)

	// Go
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	// TS/JS
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts"} {
		if strings.HasSuffix(base, ".test"+ext) || strings.HasSuffix(base, ".spec"+ext) {
			return true
		}
	}
	if strings.Contains(p, "/__tests__/") || strings.HasPrefix(p, "__tests__/") {
		return true
	}
	// Python
	if strings.HasSuffix(base, ".py") {
		if strings.HasPrefix(base, "test_") || strings.HasSuffix(strings.TrimSuffix(base, ".py"), "_test") {
			return true
		}
		// tests/ 配下のすべての .py を test とみなす
		if dir == "tests" || strings.HasSuffix(dir, "/tests") {
			return true
		}
	}
	// Rust integration tests
	if strings.HasSuffix(base, ".rs") {
		if dir == "tests" || strings.HasSuffix(dir, "/tests") {
			return true
		}
	}
	// Java
	if strings.HasSuffix(base, ".java") {
		stem := strings.TrimSuffix(base, ".java")
		if strings.HasSuffix(stem, "Test") || strings.HasSuffix(stem, "IT") ||
			strings.HasSuffix(stem, "Tests") || strings.HasSuffix(stem, "Spec") {
			return true
		}
	}
	return false
}

// TestPathCandidates は source path から想定される test path のリストを返す。
//
// 検出ロジック: source path → 言語別 test 命名を全候補列挙
// 戻り値は同一 directory 想定 + 別 directory 慣習(例: __tests__, tests/)も含む。
func TestPathCandidates(p string) []string {
	base := path.Base(p)
	dir := path.Dir(p)

	// Go: foo.go → foo_test.go
	if strings.HasSuffix(base, ".go") && !strings.HasSuffix(base, "_test.go") {
		stem := strings.TrimSuffix(base, ".go")
		return []string{
			path.Join(dir, stem+"_test.go"),
		}
	}
	// TS/JS: foo.ts → foo.test.ts, foo.spec.ts, __tests__/foo.test.ts
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts"} {
		if strings.HasSuffix(base, ext) {
			if strings.HasSuffix(base, ".test"+ext) || strings.HasSuffix(base, ".spec"+ext) {
				return nil // already a test
			}
			stem := strings.TrimSuffix(base, ext)
			cands := []string{
				path.Join(dir, stem+".test"+ext),
				path.Join(dir, stem+".spec"+ext),
				path.Join(dir, "__tests__", stem+".test"+ext),
				path.Join(dir, "__tests__", stem+".spec"+ext),
				path.Join(dir, "__tests__", stem+ext),
			}
			return cands
		}
	}
	// Python: foo.py → test_foo.py / foo_test.py / tests/test_foo.py
	if strings.HasSuffix(base, ".py") {
		if strings.HasPrefix(base, "test_") || strings.HasSuffix(strings.TrimSuffix(base, ".py"), "_test") {
			return nil
		}
		stem := strings.TrimSuffix(base, ".py")
		return []string{
			path.Join(dir, "test_"+stem+".py"),
			path.Join(dir, stem+"_test.py"),
			path.Join(dir, "tests", "test_"+stem+".py"),
			path.Join("tests", "test_"+stem+".py"),
		}
	}
	// Rust: foo.rs → tests/foo.rs (integration). inline は別途 content scan。
	if strings.HasSuffix(base, ".rs") {
		if dir == "tests" || strings.HasSuffix(dir, "/tests") {
			return nil // already integration test
		}
		stem := strings.TrimSuffix(base, ".rs")
		return []string{
			path.Join("tests", stem+".rs"),
			path.Join(path.Dir(dir), "tests", stem+".rs"),
		}
	}
	// Java: Foo.java → FooTest.java, FooIT.java
	if strings.HasSuffix(base, ".java") {
		stem := strings.TrimSuffix(base, ".java")
		if strings.HasSuffix(stem, "Test") || strings.HasSuffix(stem, "IT") {
			return nil
		}
		return []string{
			path.Join(dir, stem+"Test.java"),
			path.Join(dir, stem+"Tests.java"),
			path.Join(dir, stem+"IT.java"),
		}
	}
	return nil
}

// detectLanguage は path 拡張子から言語を返す。
func detectLanguage(p string) string {
	switch {
	case strings.HasSuffix(p, ".go"):
		return "go"
	case strings.HasSuffix(p, ".ts"), strings.HasSuffix(p, ".tsx"):
		return "ts"
	case strings.HasSuffix(p, ".js"), strings.HasSuffix(p, ".jsx"):
		return "js"
	case strings.HasSuffix(p, ".py"):
		return "python"
	case strings.HasSuffix(p, ".rs"):
		return "rust"
	case strings.HasSuffix(p, ".java"):
		return "java"
	default:
		return ""
	}
}

// HasInlineTest は content 内に inline test 定義があるか判定する。
//
// 対応:
//   - Rust: #[cfg(test)] mod tests { ... }
//   - Python: doctest (>>> 行)
func HasInlineTest(filepath, content string) bool {
	switch detectLanguage(filepath) {
	case "rust":
		return rustCfgTestRe.MatchString(content)
	case "python":
		return pythonDoctestRe.MatchString(content)
	}
	return false
}

// Audit は files map を scan して test 対応を集計する。
//
// IsTestFile = true のものは source としてカウントしない。
// 各 source に対し TestPathCandidates の中から files map に存在するものを探す。
// inline test (Rust #[cfg(test)], Python doctest) も has_test として扱う。
func Audit(files map[string]string) AuditResult {
	res := AuditResult{
		ByLanguage: map[string]LangStats{},
	}
	// 全 path を set に
	pathSet := make(map[string]bool, len(files))
	for p := range files {
		pathSet[p] = true
	}

	var untested []string
	statuses := make(map[string]FileStatus, len(files))
	for p, content := range files {
		res.FilesScanned++
		st := FileStatus{Path: p, Language: detectLanguage(p)}
		if IsTestFile(p) {
			st.IsTest = true
			res.TestFiles++
			statuses[p] = st
			continue
		}
		res.SourceFiles++
		// inline test?
		if HasInlineTest(p, content) {
			st.HasInline = true
			st.HasTest = true
			res.SourcesWithTest++
		} else {
			// 候補 path を集合チェック
			for _, cand := range TestPathCandidates(p) {
				if pathSet[cand] {
					st.HasTest = true
					st.TestPath = cand
					res.SourcesWithTest++
					break
				}
			}
			if !st.HasTest {
				res.SourcesNoTest++
				untested = append(untested, p)
			}
		}
		statuses[p] = st

		// 言語別集計
		if st.Language != "" {
			ls := res.ByLanguage[st.Language]
			ls.Sources++
			if st.HasTest {
				ls.WithTest++
			}
			res.ByLanguage[st.Language] = ls
		}
	}

	// test file 数を language stats に
	for p, st := range statuses {
		if st.IsTest && st.Language != "" {
			ls := res.ByLanguage[st.Language]
			ls.Tests++
			res.ByLanguage[st.Language] = ls
		}
		_ = p
	}

	// coverage_ratio
	if res.SourceFiles > 0 {
		res.CoverageRatio = float64(res.SourcesWithTest) / float64(res.SourceFiles)
	}
	for k, ls := range res.ByLanguage {
		if ls.Sources > 0 {
			ls.CoverageRatio = float64(ls.WithTest) / float64(ls.Sources)
		}
		res.ByLanguage[k] = ls
	}

	// 決定論的 sort
	sort.Strings(untested)
	res.UntestedFiles = untested
	return res
}

// AuditFile は単一ファイルの status のみを返す(integration 用)。
func AuditFile(filepath, content string, allPaths map[string]bool) FileStatus {
	st := FileStatus{Path: filepath, Language: detectLanguage(filepath)}
	if IsTestFile(filepath) {
		st.IsTest = true
		return st
	}
	if HasInlineTest(filepath, content) {
		st.HasInline = true
		st.HasTest = true
		return st
	}
	for _, cand := range TestPathCandidates(filepath) {
		if allPaths[cand] {
			st.HasTest = true
			st.TestPath = cand
			return st
		}
	}
	return st
}
