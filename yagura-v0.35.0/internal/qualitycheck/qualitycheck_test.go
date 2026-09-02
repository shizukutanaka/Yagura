package qualitycheck

import (
	"strings"
	"testing"
)

// ─── 基本動作 ──────────────────────────────────────────────

func TestDefaultRules_AllValid(t *testing.T) {
	rules := DefaultRules()
	if len(rules) == 0 {
		t.Fatal("DefaultRules should not be empty")
	}
	for _, r := range rules {
		if r.ID == "" {
			t.Errorf("rule with empty ID: %+v", r)
		}
		if r.pattern == nil {
			t.Errorf("rule %s has nil pattern", r.ID)
		}
		if r.Severity != SevProhibited && r.Severity != SevWarning && r.Severity != SevInfo {
			t.Errorf("rule %s has invalid severity: %s", r.ID, r.Severity)
		}
	}
}

func TestScanText_CleanCode(t *testing.T) {
	content := `function add(a: number, b: number): number {
  return a + b;
}
`
	findings := ScanText("good.ts", content, "ts", DefaultRules())
	if len(findings) != 0 {
		t.Errorf("clean code should have 0 findings, got %d: %+v", len(findings), findings)
	}
}

// ─── TypeScript 個別ルール ─────────────────────────────────

func TestScanText_AsAny(t *testing.T) {
	content := `const value = data as any;`
	findings := ScanText("f.ts", content, "ts", DefaultRules())
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].RuleID != "ts-as-any" {
		t.Errorf("rule_id: got %s, want ts-as-any", findings[0].RuleID)
	}
	if findings[0].Severity != SevProhibited {
		t.Errorf("severity: got %s, want prohibited", findings[0].Severity)
	}
}

func TestScanText_AnyType(t *testing.T) {
	content := `function foo(x: any): void {}`
	findings := ScanText("f.ts", content, "ts", DefaultRules())
	// ts-any-type で 1 件
	count := 0
	for _, f := range findings {
		if f.RuleID == "ts-any-type" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 ts-any-type finding, got %d. all findings: %+v", count, findings)
	}
}

func TestScanText_TsIgnore(t *testing.T) {
	content := `// @ts-ignore
const x = wrongType;`
	findings := ScanText("f.ts", content, "ts", DefaultRules())
	if len(findings) == 0 || findings[0].RuleID != "ts-ignore" {
		t.Errorf("expected ts-ignore detection, got %+v", findings)
	}
}

func TestScanText_EslintDisable(t *testing.T) {
	cases := []string{
		"// eslint-disable",
		"// eslint-disable-next-line no-console",
		"/* eslint-disable-line */",
	}
	for _, c := range cases {
		findings := ScanText("f.ts", c, "ts", DefaultRules())
		hit := false
		for _, f := range findings {
			if f.RuleID == "eslint-disable" {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("%q should match eslint-disable", c)
		}
	}
}

// ─── Go 個別ルール ─────────────────────────────────────────

func TestScanText_GoNolint(t *testing.T) {
	content := `func foo() { //nolint
		bar()
}`
	findings := ScanText("f.go", content, "go", DefaultRules())
	hit := false
	for _, f := range findings {
		if f.RuleID == "go-nolint" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected go-nolint detection, got %+v", findings)
	}
}

func TestScanText_GoPanic(t *testing.T) {
	content := `if err != nil {
	panic(err)
}`
	findings := ScanText("f.go", content, "go", DefaultRules())
	hit := false
	for _, f := range findings {
		if f.RuleID == "go-panic-prod" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected go-panic-prod detection")
	}
}

// ─── Python 個別ルール ─────────────────────────────────────

func TestScanText_PyTypeIgnore(t *testing.T) {
	content := `x = something  # type: ignore`
	findings := ScanText("f.py", content, "py", DefaultRules())
	hit := false
	for _, f := range findings {
		if f.RuleID == "py-type-ignore" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected py-type-ignore")
	}
}

// ─── Universal: TODO/FIXME/HACK/XXX ──────────────────────

func TestScanText_TodoUniversal(t *testing.T) {
	contents := map[string]string{
		"a.ts": "// TODO: fix this later",
		"b.go": "// TODO refactor",
		"c.py": "# TODO add tests",
	}
	for path, content := range contents {
		lang := detectLanguage(path)
		findings := ScanText(path, content, lang, DefaultRules())
		hit := false
		for _, f := range findings {
			if f.RuleID == "todo" {
				hit = true
			}
		}
		if !hit {
			t.Errorf("%s: TODO not detected", path)
		}
	}
}

func TestScanText_AllUniversalMarkers(t *testing.T) {
	content := `// TODO: a
// FIXME: b
// HACK: c
// XXX: d`
	findings := ScanText("f.go", content, "go", DefaultRules())
	wanted := map[string]bool{"todo": false, "fixme": false, "hack": false, "xxx": false}
	for _, f := range findings {
		wanted[f.RuleID] = true
	}
	for id, hit := range wanted {
		if !hit {
			t.Errorf("%s not detected", id)
		}
	}
}

// ─── 言語フィルタリング ────────────────────────────────

func TestScanText_TsRuleDoesNotApplyToGo(t *testing.T) {
	content := `var x interface{} = "as any"  // string literal containing "as any"`
	findings := ScanText("f.go", content, "go", DefaultRules())
	// "as any" は文字列内だが、Go file なので ts-as-any rule は適用されない
	for _, f := range findings {
		if f.RuleID == "ts-as-any" {
			t.Errorf("ts-as-any should not apply to .go files: %+v", f)
		}
	}
}

// ─── 言語検出 ──────────────────────────────────────────

func TestDetectLanguage(t *testing.T) {
	cases := map[string]string{
		"foo.ts":      "ts",
		"foo.tsx":     "tsx",
		"foo.js":      "js",
		"bar.go":      "go",
		"baz.py":      "py",
		"q.rs":        "rust",
		"unknown.txt": "",
	}
	for path, want := range cases {
		got := detectLanguage(path)
		if got != want {
			t.Errorf("detectLanguage(%q): got %q, want %q", path, got, want)
		}
	}
}

// ─── ScanFiles 集計 ────────────────────────────────────

func TestScanFiles_AggregatesByFileSeverityRule(t *testing.T) {
	files := map[string]string{
		"a.ts": `const x = data as any;
// TODO
// @ts-ignore
const y = 1;`,
		"b.go": `// TODO: fix
panic("oops")`,
	}
	res := ScanFiles(files, DefaultRules())
	if res.FilesScanned != 2 {
		t.Errorf("files_scanned: got %d, want 2", res.FilesScanned)
	}
	// a.ts: ts-as-any(prohibited), todo, ts-ignore(prohibited) = 3
	// b.go: todo, go-panic-prod = 2
	if len(res.Findings) != 5 {
		t.Errorf("findings: got %d, want 5. findings=%+v", len(res.Findings), res.Findings)
	}
	if res.BySeverity[SevProhibited] != 2 {
		t.Errorf("prohibited: got %d, want 2", res.BySeverity[SevProhibited])
	}
}

func TestResult_HasProhibitedTrue(t *testing.T) {
	files := map[string]string{"a.ts": `const x = y as any;`}
	res := ScanFiles(files, DefaultRules())
	if !res.HasProhibited() {
		t.Error("expected HasProhibited=true")
	}
}

func TestResult_HasProhibitedFalse(t *testing.T) {
	files := map[string]string{"a.go": "// TODO clean"}
	res := ScanFiles(files, DefaultRules())
	if res.HasProhibited() {
		t.Error("expected HasProhibited=false (TODO is warning, not prohibited)")
	}
}

// ─── 行・列情報 ────────────────────────────────────────

func TestScanText_ReportsLineAndColumn(t *testing.T) {
	content := `line1
line2
const x = y as any;
line4`
	findings := ScanText("f.ts", content, "ts", DefaultRules())
	if len(findings) == 0 {
		t.Fatal("expected at least 1 finding")
	}
	if findings[0].Line != 3 {
		t.Errorf("line: got %d, want 3", findings[0].Line)
	}
	if findings[0].Column < 1 {
		t.Errorf("column should be >= 1, got %d", findings[0].Column)
	}
}

// ─── Excerpt 切り詰め ─────────────────────────────────

func TestScanText_LongLineTruncated(t *testing.T) {
	long := strings.Repeat("x", 200) + " as any"
	findings := ScanText("f.ts", long, "ts", DefaultRules())
	if len(findings) == 0 {
		t.Fatal("expected finding")
	}
	if len(findings[0].Excerpt) > 120 {
		t.Errorf("excerpt should be ≤ 120 chars, got %d", len(findings[0].Excerpt))
	}
	if !strings.HasSuffix(findings[0].Excerpt, "...") {
		t.Errorf("truncated excerpt should end in ...; got %q", findings[0].Excerpt)
	}
}

// ─── deterministic ordering ───────────────────────────

func TestScanFiles_FindingsAreSorted(t *testing.T) {
	files := map[string]string{
		"z.ts": `const x = y as any;`,
		"a.ts": `const z = w as any;`,
	}
	res := ScanFiles(files, DefaultRules())
	if len(res.Findings) != 2 {
		t.Fatal("expected 2 findings")
	}
	if res.Findings[0].File != "a.ts" || res.Findings[1].File != "z.ts" {
		t.Errorf("expected sorted by file: got %v", []string{res.Findings[0].File, res.Findings[1].File})
	}
}

// ─── 偽陽性回避: 自動生成ファイル想定 ─────────────────

func TestScanText_MultipleFindingsPerLine(t *testing.T) {
	// 1 行に 2 個 as any
	content := `const a = x as any, b = y as any;`
	findings := ScanText("f.ts", content, "ts", DefaultRules())
	count := 0
	for _, f := range findings {
		if f.RuleID == "ts-as-any" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 ts-as-any matches on same line, got %d", count)
	}
}

// ─── Summary 文字列 ──────────────────────────────────

func TestResult_SummaryClean(t *testing.T) {
	files := map[string]string{"a.go": "package main\n"}
	res := ScanFiles(files, DefaultRules())
	s := res.Summary()
	if !strings.Contains(s, "clean") {
		t.Errorf("clean summary expected, got %q", s)
	}
}

func TestResult_SummaryWithFindings(t *testing.T) {
	files := map[string]string{"a.ts": "const x = y as any;\n// TODO\n"}
	res := ScanFiles(files, DefaultRules())
	s := res.Summary()
	if !strings.Contains(s, "prohibited=1") {
		t.Errorf("expected prohibited=1 in summary, got %q", s)
	}
	if !strings.Contains(s, "warning=1") {
		t.Errorf("expected warning=1 in summary, got %q", s)
	}
}

// ─── v0.23.0: cache 統合 ───────────────────────────────────

// fakeCache は CacheLike interface の test 実装。
type fakeCache struct {
	store  map[string][]byte
	hits   int
	misses int
}

func newFakeCache() *fakeCache {
	return &fakeCache{store: map[string][]byte{}}
}

func (f *fakeCache) Get(key string) ([]byte, bool) {
	v, ok := f.store[key]
	if ok {
		f.hits++
		return v, true
	}
	f.misses++
	return nil, false
}

func (f *fakeCache) Set(key string, value []byte) {
	f.store[key] = value
}

func TestScanFilesCached_SameContentHitsCache(t *testing.T) {
	cache := newFakeCache()
	files := map[string]string{"a.ts": `const x = data as any;`}

	res1 := ScanFilesCached(files, DefaultRules(), cache)
	if res1.CacheMisses != 1 || res1.CacheHits != 0 {
		t.Errorf("1st scan: expected miss=1 hit=0, got miss=%d hit=%d",
			res1.CacheMisses, res1.CacheHits)
	}

	// 同じ内容を再 scan
	res2 := ScanFilesCached(files, DefaultRules(), cache)
	if res2.CacheHits != 1 || res2.CacheMisses != 0 {
		t.Errorf("2nd scan: expected hit=1 miss=0, got hit=%d miss=%d",
			res2.CacheHits, res2.CacheMisses)
	}

	// finding は同一(determinism 維持)
	if len(res1.Findings) != len(res2.Findings) {
		t.Errorf("cache should return same findings: %d vs %d",
			len(res1.Findings), len(res2.Findings))
	}
}

func TestScanFilesCached_ChangedContentMisses(t *testing.T) {
	cache := newFakeCache()
	v1 := map[string]string{"a.ts": `const x = data as any;`}
	v2 := map[string]string{"a.ts": `const x = 1;`} // 内容変更

	ScanFilesCached(v1, DefaultRules(), cache)
	res2 := ScanFilesCached(v2, DefaultRules(), cache)
	if res2.CacheHits != 0 {
		t.Errorf("changed content should miss; got hits=%d", res2.CacheHits)
	}
}

func TestScanFilesCached_DifferentPathSameContentDifferentKey(t *testing.T) {
	cache := newFakeCache()
	a := map[string]string{"a.ts": `const x = y as any;`}
	b := map[string]string{"b.ts": `const x = y as any;`} // path 違い

	ScanFilesCached(a, DefaultRules(), cache)
	res := ScanFilesCached(b, DefaultRules(), cache)
	// 違う path なので miss
	if res.CacheHits != 0 {
		t.Errorf("different path should miss cache; got hits=%d", res.CacheHits)
	}
}

func TestScanFilesCached_NilCacheBypassesCache(t *testing.T) {
	files := map[string]string{"a.ts": `const x = y as any;`}
	res := ScanFilesCached(files, DefaultRules(), nil)
	if res.CacheHits != 0 || res.CacheMisses != 0 {
		t.Errorf("nil cache: expected hits=0 misses=0, got hits=%d misses=%d",
			res.CacheHits, res.CacheMisses)
	}
	// findings は生成される
	if len(res.Findings) == 0 {
		t.Error("findings should still be generated without cache")
	}
}

func TestScanFiles_BackwardCompat(t *testing.T) {
	// 旧 API はそのまま動く
	files := map[string]string{"a.ts": `const x = y as any;`}
	res := ScanFiles(files, DefaultRules())
	if len(res.Findings) == 0 {
		t.Error("ScanFiles should still work without cache")
	}
}

func TestCacheKeyFor_DifferentLanguageDiffersKey(t *testing.T) {
	k1 := cacheKeyFor("foo", "content", "ts")
	k2 := cacheKeyFor("foo", "content", "go")
	if k1 == k2 {
		t.Error("different language should produce different cache key")
	}
}

// ─── v0.35: CompileRules (custom rule loading) ─────────────

func TestCompileRules_ValidSpec(t *testing.T) {
	rules, err := CompileRules([]RuleSpec{{
		ID:          "no-console-log",
		Pattern:     `console\.log`,
		Severity:    SevProhibited,
		Languages:   []string{"ts", "js"},
		Description: "console.log left in code",
		Suggestion:  "use a logger",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	r := rules[0]
	if r.ID != "no-console-log" || r.pattern == nil {
		t.Errorf("rule not compiled correctly: %+v", r)
	}
	if r.Severity != SevProhibited {
		t.Errorf("severity: got %s, want prohibited", r.Severity)
	}
}

func TestCompileRules_DefaultsSeverityAndLanguages(t *testing.T) {
	rules, err := CompileRules([]RuleSpec{{ID: "x", Pattern: `foo`}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules[0].Severity != SevWarning {
		t.Errorf("default severity: got %s, want warning", rules[0].Severity)
	}
	if len(rules[0].Languages) != 1 || rules[0].Languages[0] != "any" {
		t.Errorf("default languages: got %v, want [any]", rules[0].Languages)
	}
}

func TestCompileRules_PreservesInputOrder(t *testing.T) {
	rules, err := CompileRules([]RuleSpec{
		{ID: "a", Pattern: `a`},
		{ID: "b", Pattern: `b`},
		{ID: "c", Pattern: `c`},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := []string{rules[0].ID, rules[1].ID, rules[2].ID}
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("order not preserved: %v", got)
	}
}

func TestCompileRules_Errors(t *testing.T) {
	cases := []struct {
		name string
		spec RuleSpec
		want string
	}{
		{"empty id", RuleSpec{ID: " ", Pattern: `foo`}, "id is required"},
		{"empty pattern", RuleSpec{ID: "x", Pattern: "  "}, "pattern is required"},
		{"invalid regex", RuleSpec{ID: "x", Pattern: `[unclosed`}, "invalid pattern"},
		{"bad severity", RuleSpec{ID: "x", Pattern: `foo`, Severity: "fatal"}, "invalid severity"},
		{"pattern too long", RuleSpec{ID: "x", Pattern: strings.Repeat("a", maxPatternLen+1)}, "too long"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := CompileRules([]RuleSpec{c.spec})
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should contain %q", err.Error(), c.want)
			}
		})
	}
}

func TestCompileRules_EmptyInput(t *testing.T) {
	rules, err := CompileRules(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("expected 0 rules, got %d", len(rules))
	}
}

func TestCompileRules_CustomRuleFlagsPattern(t *testing.T) {
	custom, err := CompileRules([]RuleSpec{{
		ID:        "no-console-log",
		Pattern:   `console\.log`,
		Severity:  SevProhibited,
		Languages: []string{"any"},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rules := append(DefaultRules(), custom...)
	findings := ScanText("app.ts", `console.log("debug");`, "ts", rules)
	hit := false
	for _, f := range findings {
		if f.RuleID == "no-console-log" && f.Severity == SevProhibited {
			hit = true
		}
	}
	if !hit {
		t.Errorf("custom rule should flag console.log: %+v", findings)
	}
}

// TestTruncateExcerpt_SmallMaxDoesNotPanic pins the boundary: when maxLen < 3
// and the line is longer than maxLen, the old code computed trimmed[:maxLen-3]
// which is a negative index and panics. Small maxLen must truncate without the
// "..." suffix and never panic.
func TestTruncateExcerpt_SmallMaxDoesNotPanic(t *testing.T) {
	cases := []struct {
		line   string
		maxLen int
		want   string
	}{
		{"hello world", 2, "he"},       // <3 → no ellipsis room (was a panic)
		{"hello world", 0, ""},         // 0 → empty (was a panic)
		{"hello world", -1, ""},        // negative → empty (was a panic)
		{"abc", 5, "abc"},              // shorter than max → unchanged
		{"hello world", 8, "hello..."}, // normal path: 5 chars + "..."
	}
	for _, c := range cases {
		got := truncateExcerpt(c.line, c.maxLen)
		if got != c.want {
			t.Errorf("truncateExcerpt(%q, %d) = %q, want %q", c.line, c.maxLen, got, c.want)
		}
	}
}

// TestSortFindings_TotalOrder_SamePosition pins the deterministic tie-break.
// scanFilesImpl ranges the input files map (non-deterministic) and sorts with
// sort.Slice (unstable); two rules matching the same File/Line/Column would
// otherwise keep a map-walk-dependent order. Feeding the tied pair in both
// permutations must canonicalize identically.
func TestSortFindings_TotalOrder_SamePosition(t *testing.T) {
	a := Finding{File: "x.go", Line: 5, Column: 3, RuleID: "aaa-rule"}
	b := Finding{File: "x.go", Line: 5, Column: 3, RuleID: "zzz-rule"}

	fwd := []Finding{a, b}
	sortFindings(fwd)
	rev := []Finding{b, a}
	sortFindings(rev)

	if fwd[0].RuleID != rev[0].RuleID || fwd[1].RuleID != rev[1].RuleID {
		t.Errorf("sortFindings not a total order: [a,b] -> %s,%s but [b,a] -> %s,%s",
			fwd[0].RuleID, fwd[1].RuleID, rev[0].RuleID, rev[1].RuleID)
	}
	if fwd[0].RuleID != "aaa-rule" {
		t.Errorf("expected RuleID-ascending tie-break, got %s first", fwd[0].RuleID)
	}
}
