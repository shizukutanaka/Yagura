package testcoverage

import (
	"strings"
	"testing"
)

// ─── IsTestFile ────────────────────────────────────

func TestIsTestFile_Go(t *testing.T) {
	yes := []string{"foo_test.go", "internal/mcp/server_test.go", "a/b/c_test.go"}
	no := []string{"foo.go", "internal/server.go", "tests/lib.go"}
	for _, p := range yes {
		if !IsTestFile(p) {
			t.Errorf("should be test: %s", p)
		}
	}
	for _, p := range no {
		if IsTestFile(p) {
			t.Errorf("should NOT be test: %s", p)
		}
	}
}

func TestIsTestFile_TS(t *testing.T) {
	yes := []string{
		"foo.test.ts",
		"foo.spec.tsx",
		"src/components/__tests__/Button.tsx",
		"a/__tests__/x.test.ts",
		"x.test.jsx",
	}
	no := []string{"foo.ts", "src/main.tsx", "lib.js"}
	for _, p := range yes {
		if !IsTestFile(p) {
			t.Errorf("should be test: %s", p)
		}
	}
	for _, p := range no {
		if IsTestFile(p) {
			t.Errorf("should NOT be test: %s", p)
		}
	}
}

func TestIsTestFile_Python(t *testing.T) {
	yes := []string{"test_foo.py", "foo_test.py", "tests/test_x.py", "src/tests/helper.py"}
	no := []string{"foo.py", "module.py"}
	for _, p := range yes {
		if !IsTestFile(p) {
			t.Errorf("should be test: %s", p)
		}
	}
	for _, p := range no {
		if IsTestFile(p) {
			t.Errorf("should NOT be test: %s", p)
		}
	}
}

func TestIsTestFile_Java(t *testing.T) {
	yes := []string{"FooTest.java", "FooIT.java", "FooTests.java", "BarSpec.java"}
	no := []string{"Foo.java", "Service.java"}
	for _, p := range yes {
		if !IsTestFile(p) {
			t.Errorf("should be test: %s", p)
		}
	}
	for _, p := range no {
		if IsTestFile(p) {
			t.Errorf("should NOT be test: %s", p)
		}
	}
}

func TestIsTestFile_RustIntegration(t *testing.T) {
	if !IsTestFile("tests/integration.rs") {
		t.Error("tests/*.rs should be integration test")
	}
	if !IsTestFile("crate/tests/foo.rs") {
		t.Error("nested tests/ should match")
	}
	if IsTestFile("src/lib.rs") {
		t.Error("src/lib.rs is not a test")
	}
}

// ─── TestPathCandidates ───────────────────────────

func TestTestPathCandidates_Go(t *testing.T) {
	cands := TestPathCandidates("foo.go")
	if len(cands) != 1 || cands[0] != "foo_test.go" {
		t.Errorf("Go candidate: %v", cands)
	}
}

func TestTestPathCandidates_GoNested(t *testing.T) {
	cands := TestPathCandidates("internal/mcp/server.go")
	if cands[0] != "internal/mcp/server_test.go" {
		t.Errorf("nested Go: %v", cands)
	}
}

func TestTestPathCandidates_TS(t *testing.T) {
	cands := TestPathCandidates("src/auth.ts")
	wantSome := map[string]bool{
		"src/auth.test.ts":           true,
		"src/auth.spec.ts":           true,
		"src/__tests__/auth.test.ts": true,
	}
	for _, c := range cands {
		delete(wantSome, c)
	}
	if len(wantSome) > 0 {
		t.Errorf("missing TS candidates: %v (got: %v)", wantSome, cands)
	}
}

func TestTestPathCandidates_Python(t *testing.T) {
	cands := TestPathCandidates("foo.py")
	wantSome := map[string]bool{
		"test_foo.py":       true,
		"foo_test.py":       true,
		"tests/test_foo.py": true,
	}
	for _, c := range cands {
		delete(wantSome, c)
	}
	if len(wantSome) > 0 {
		t.Errorf("missing Python candidates: %v (got: %v)", wantSome, cands)
	}
}

func TestTestPathCandidates_ReturnsNilForTestFile(t *testing.T) {
	cases := []string{"foo_test.go", "foo.test.ts", "test_foo.py", "FooTest.java"}
	for _, p := range cases {
		if cs := TestPathCandidates(p); cs != nil {
			t.Errorf("test file %s should yield nil candidates, got %v", p, cs)
		}
	}
}

// ─── Rust inline test ────────────────────────────

func TestHasInlineTest_RustCfgTest(t *testing.T) {
	content := `
pub fn foo() -> i32 { 42 }

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn it_works() {
        assert_eq!(foo(), 42);
    }
}
`
	if !HasInlineTest("src/lib.rs", content) {
		t.Error("should detect #[cfg(test)]")
	}
}

func TestHasInlineTest_RustWithFeature(t *testing.T) {
	content := `#[cfg(feature = "test")]
fn helper() {}`
	if !HasInlineTest("src/lib.rs", content) {
		t.Error("should detect #[cfg(feature=\"test\")]")
	}
}

func TestHasInlineTest_RustWithAny(t *testing.T) {
	content := `#[cfg(any(test, debug_assertions))]
fn helper() {}`
	if !HasInlineTest("src/lib.rs", content) {
		t.Error("should detect #[cfg(any(test, ...))]")
	}
}

func TestHasInlineTest_RustNoTest(t *testing.T) {
	content := `pub fn foo() { println!("hi") }`
	if HasInlineTest("src/lib.rs", content) {
		t.Error("plain rust should not have inline test")
	}
}

func TestHasInlineTest_PythonDoctest(t *testing.T) {
	content := `
def add(a, b):
    """
    >>> add(2, 3)
    5
    """
    return a + b
`
	if !HasInlineTest("foo.py", content) {
		t.Error("should detect Python doctest")
	}
}

// ─── Audit (集計) ─────────────────────────────────

func TestAudit_BasicGoCoverage(t *testing.T) {
	files := map[string]string{
		"a.go":      "package x",
		"a_test.go": "package x\nimport \"testing\"\nfunc TestA(t *testing.T) {}",
		"b.go":      "package x", // no test
	}
	r := Audit(files)
	if r.SourceFiles != 2 {
		t.Errorf("SourceFiles: got %d, want 2", r.SourceFiles)
	}
	if r.TestFiles != 1 {
		t.Errorf("TestFiles: got %d, want 1", r.TestFiles)
	}
	if r.SourcesWithTest != 1 {
		t.Errorf("SourcesWithTest: got %d, want 1", r.SourcesWithTest)
	}
	if r.SourcesNoTest != 1 {
		t.Errorf("SourcesNoTest: got %d, want 1", r.SourcesNoTest)
	}
	if r.CoverageRatio != 0.5 {
		t.Errorf("CoverageRatio: got %v, want 0.5", r.CoverageRatio)
	}
}

func TestAudit_RustInlineCountsAsTested(t *testing.T) {
	files := map[string]string{
		"src/lib.rs": "#[cfg(test)] mod tests {}",
		"src/foo.rs": "pub fn x() {}",
	}
	r := Audit(files)
	if r.SourcesWithTest != 1 {
		t.Errorf("Rust inline should count: SourcesWithTest=%d", r.SourcesWithTest)
	}
}

func TestAudit_TSWithTests(t *testing.T) {
	files := map[string]string{
		"src/auth.ts":                         "export const login = () => {}",
		"src/auth.test.ts":                    "test('login', () => {})",
		"src/billing.ts":                      "export const charge = () => {}",
		"src/components/Button.tsx":           "export const Button = () => null",
		"src/components/__tests__/Button.tsx": "test('button', () => {})",
	}
	r := Audit(files)
	if r.SourcesWithTest != 2 {
		t.Errorf("TS coverage: got %d, want 2", r.SourcesWithTest)
	}
	if r.SourcesNoTest != 1 {
		t.Errorf("untested: got %d, want 1 (billing)", r.SourcesNoTest)
	}
	if len(r.UntestedFiles) != 1 || r.UntestedFiles[0] != "src/billing.ts" {
		t.Errorf("UntestedFiles: %v", r.UntestedFiles)
	}
}

func TestAudit_ByLanguage(t *testing.T) {
	files := map[string]string{
		"a.go":      "package x",
		"a_test.go": "package x",
		"foo.py":    "x = 1",
	}
	r := Audit(files)
	if r.ByLanguage["go"].Sources != 1 || r.ByLanguage["go"].WithTest != 1 {
		t.Errorf("go stats: %+v", r.ByLanguage["go"])
	}
	if r.ByLanguage["python"].Sources != 1 || r.ByLanguage["python"].WithTest != 0 {
		t.Errorf("py stats: %+v", r.ByLanguage["python"])
	}
	if r.ByLanguage["go"].CoverageRatio != 1.0 {
		t.Errorf("go ratio: %v", r.ByLanguage["go"].CoverageRatio)
	}
}

func TestAudit_AllTestFilesNoSources(t *testing.T) {
	files := map[string]string{
		"a_test.go": "package x",
		"b_test.go": "package x",
	}
	r := Audit(files)
	if r.SourceFiles != 0 {
		t.Errorf("SourceFiles: got %d, want 0", r.SourceFiles)
	}
	if r.CoverageRatio != 0 {
		t.Errorf("CoverageRatio should be 0 for no sources, got %v", r.CoverageRatio)
	}
}

func TestAudit_DeterministicUntestedOrder(t *testing.T) {
	files := map[string]string{
		"z.go": "package x",
		"a.go": "package x",
		"m.go": "package x",
	}
	r := Audit(files)
	expected := []string{"a.go", "m.go", "z.go"}
	for i, p := range expected {
		if r.UntestedFiles[i] != p {
			t.Errorf("untested[%d]: got %s, want %s", i, r.UntestedFiles[i], p)
		}
	}
}

// ─── AuditFile (単一) ─────────────────────────────

func TestAuditFile_StandaloneSource(t *testing.T) {
	paths := map[string]bool{"foo.go": true, "foo_test.go": true}
	st := AuditFile("foo.go", "package x", paths)
	if !st.HasTest {
		t.Error("should detect test in path set")
	}
	if st.TestPath != "foo_test.go" {
		t.Errorf("TestPath: %s", st.TestPath)
	}
}

func TestAuditFile_NoTestFound(t *testing.T) {
	paths := map[string]bool{"foo.go": true}
	st := AuditFile("foo.go", "package x", paths)
	if st.HasTest {
		t.Error("should NOT have test")
	}
}

func TestAuditFile_InlineTest(t *testing.T) {
	paths := map[string]bool{"lib.rs": true}
	st := AuditFile("lib.rs", "#[cfg(test)] mod tests {}", paths)
	if !st.HasTest || !st.HasInline {
		t.Errorf("inline should mark HasTest+HasInline: %+v", st)
	}
}

// TestAuditFile_IsTestFile covers `st.IsTest = true; return st` in AuditFile
// when IsTestFile returns true for a known test file name.
func TestAuditFile_IsTestFile(t *testing.T) {
	paths := map[string]bool{"foo_test.go": true}
	st := AuditFile("foo_test.go", "package x", paths)
	if !st.IsTest {
		t.Error("AuditFile for a test file should set IsTest=true")
	}
}

// ─── TestPathCandidates: Rust integration + Java ──────────────

// TestTestPathCandidates_RustAlreadyIntegration covers the
// `return nil // already integration test` branch in the Rust case when the
// file is already inside a "tests" directory.
func TestTestPathCandidates_RustAlreadyIntegration(t *testing.T) {
	cases := []string{"tests/foo.rs", "crate/tests/bar.rs"}
	for _, p := range cases {
		if got := TestPathCandidates(p); got != nil {
			t.Errorf("TestPathCandidates(%q): expected nil for integration test, got %v", p, got)
		}
	}
}

// TestTestPathCandidates_Java covers the Java source → test candidate paths.
func TestTestPathCandidates_Java(t *testing.T) {
	cands := TestPathCandidates("src/main/java/Foo.java")
	if len(cands) == 0 {
		t.Fatal("Java source should produce test candidates")
	}
	found := false
	for _, c := range cands {
		if strings.HasSuffix(c, "FooTest.java") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected FooTest.java candidate, got %v", cands)
	}
}

// ─── detectLanguage: JS and Java and unknown ──────────────────

func TestDetectLanguage_JsAndJava(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"app.js", "js"},
		{"app.jsx", "js"},
		{"Main.java", "java"},
		{"script.sh", ""},
	}
	for _, c := range cases {
		if got := detectLanguage(c.path); got != c.want {
			t.Errorf("detectLanguage(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
