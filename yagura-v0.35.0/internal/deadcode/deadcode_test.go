package deadcode_test

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/deadcode"
)

func deadNames(r deadcode.Report) map[string]bool {
	m := map[string]bool{}
	for _, f := range r.Findings {
		if f.Rule == "dead-unexported" {
			m[f.Name] = true
		}
	}
	return m
}

func TestScan_UnusedFunc_Dead(t *testing.T) {
	src := `package foo

func used() int { return 1 }

func orphan() int { return 2 }

// Exported keeps used() alive.
func Exported() int { return used() }
`
	r := deadcode.Scan(map[string]string{"a.go": src})
	dead := deadNames(r)
	if !dead["orphan"] {
		t.Error("orphan should be dead (never referenced)")
	}
	if dead["used"] {
		t.Error("used must not be dead (called by Exported)")
	}
}

func TestScan_ExportedIgnored(t *testing.T) {
	src := `package foo

func Orphan() int { return 1 }
`
	r := deadcode.Scan(map[string]string{"a.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("exported symbols are not analyzed; got %+v", r.Findings)
	}
	if r.DeclaredUnexported != 0 {
		t.Errorf("DeclaredUnexported: want 0 got %d", r.DeclaredUnexported)
	}
}

func TestScan_InitAndMainIgnored(t *testing.T) {
	src := `package main

import "fmt"

func init() {}

func main() { fmt.Println("hi") }
`
	r := deadcode.Scan(map[string]string{"a.go": src})
	dead := deadNames(r)
	if dead["init"] || dead["main"] {
		t.Errorf("init/main must never be flagged; got %v", dead)
	}
}

func TestScan_UsedOnlyInTest_NotDead(t *testing.T) {
	files := map[string]string{
		"a.go": `package foo

func helper() int { return 42 }
`,
		"a_test.go": `package foo

import "testing"

func TestHelper(t *testing.T) {
	if helper() != 42 {
		t.Fail()
	}
}
`,
	}
	r := deadcode.Scan(files)
	if deadNames(r)["helper"] {
		t.Error("helper is used by a test; must not be dead")
	}
}

func TestScan_DeclaredInTestFile_NotAnalyzed(t *testing.T) {
	// An unexported symbol declared only in a _test.go file is test scaffolding;
	// we analyze declarations in non-test files only.
	files := map[string]string{
		"a_test.go": `package foo

func testOnlyOrphan() int { return 1 }
`,
	}
	r := deadcode.Scan(files)
	if r.DeclaredUnexported != 0 {
		t.Errorf("test-file declarations are not analyzed; got %d", r.DeclaredUnexported)
	}
}

func TestScan_UnusedTypeConstVar_Dead(t *testing.T) {
	src := `package foo

type orphanType struct{}

const orphanConst = 1

var orphanVar = 2
`
	r := deadcode.Scan(map[string]string{"a.go": src})
	dead := deadNames(r)
	for _, n := range []string{"orphanType", "orphanConst", "orphanVar"} {
		if !dead[n] {
			t.Errorf("%s should be dead", n)
		}
	}
}

func TestScan_UnexportedMethodExcluded(t *testing.T) {
	// Unexported methods may satisfy an interface and be called indirectly;
	// excluding them avoids false positives.
	src := `package foo

type T struct{}

func (t *T) unusedMethod() {}

// Keep T alive.
var _ = T{}
`
	r := deadcode.Scan(map[string]string{"a.go": src})
	if deadNames(r)["unusedMethod"] {
		t.Error("unexported methods must be excluded from dead-code analysis")
	}
}

func TestScan_RecursiveOnly_NotFlagged(t *testing.T) {
	// A function that only calls itself is conservatively treated as alive
	// (safe bias: under-report rather than wrongly flag).
	src := `package foo

func loop(n int) int {
	if n <= 0 {
		return 0
	}
	return loop(n - 1)
}
`
	r := deadcode.Scan(map[string]string{"a.go": src})
	if deadNames(r)["loop"] {
		t.Error("recursive-only func should not be flagged (conservative)")
	}
}

func TestScan_CrossFileReference(t *testing.T) {
	files := map[string]string{
		"a.go": `package foo

func shared() int { return 1 }
`,
		"b.go": `package foo

// Exported uses shared from another file.
func Exported() int { return shared() }
`,
	}
	r := deadcode.Scan(files)
	if deadNames(r)["shared"] {
		t.Error("shared is referenced from b.go; must not be dead")
	}
}

func TestScan_SeparatePackages(t *testing.T) {
	// orphan in pkg a is dead even though pkg b has a same-named used symbol.
	files := map[string]string{
		"a/a.go": `package a

func orphan() int { return 1 }
`,
		"b/b.go": `package b

func orphan() int { return 1 }

// Use keeps b.orphan alive.
func Use() int { return orphan() }
`,
	}
	r := deadcode.Scan(files)
	var aOrphanDead bool
	for _, f := range r.Findings {
		if f.Name == "orphan" && f.Package == "a" {
			aOrphanDead = true
		}
		if f.Name == "orphan" && f.Package == "b" {
			t.Error("b.orphan is used; must not be dead")
		}
	}
	if !aOrphanDead {
		t.Error("a.orphan should be dead")
	}
}

func TestScan_ParseErrorSurfaced(t *testing.T) {
	r := deadcode.Scan(map[string]string{"broken.go": "package foo\nfunc {{{"})
	found := false
	for _, f := range r.Findings {
		if f.Rule == "parse-error" {
			found = true
		}
	}
	if !found {
		t.Error("parse error must be surfaced")
	}
}

func TestScan_Empty(t *testing.T) {
	r := deadcode.Scan(map[string]string{})
	if r.FilesScanned != 0 || r.Dead != 0 {
		t.Errorf("empty: files=%d dead=%d", r.FilesScanned, r.Dead)
	}
}

func TestScan_Deterministic(t *testing.T) {
	files := map[string]string{
		"z.go": "package foo\nfunc orphanA() {}\nfunc orphanB() {}\n",
		"a.go": "package foo\nfunc orphanC() {}\n",
	}
	first := deadcode.Scan(files)
	for i := 0; i < 20; i++ {
		got := deadcode.Scan(files)
		if len(got.Findings) != len(first.Findings) {
			t.Fatalf("run %d: finding count drift", i)
		}
		for j := range got.Findings {
			if got.Findings[j] != first.Findings[j] {
				t.Fatalf("run %d: finding[%d] mismatch %+v vs %+v", i, j, got.Findings[j], first.Findings[j])
			}
		}
	}
}
