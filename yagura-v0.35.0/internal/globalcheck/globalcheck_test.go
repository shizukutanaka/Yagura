package globalcheck

import "testing"

func has(r Report, name string) bool {
	for _, f := range r.Findings {
		if f.Name == name && f.Rule == "mutable-global" {
			return true
		}
	}
	return false
}

func sev(r Report, name string) string {
	for _, f := range r.Findings {
		if f.Name == name {
			return f.Severity
		}
	}
	return ""
}

// ─── core: mutated vs read-only ──────────────────────────

func TestScan_MutatedGlobalFlagged(t *testing.T) {
	src := `package p
var count int
func Inc() { count++ }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "count") {
		t.Errorf("mutated global count should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_ReadOnlyGlobalClean(t *testing.T) {
	src := `package p
var table = map[string]int{"a": 1}
func Lookup(k string) int { return table[k] }
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "table") {
		t.Errorf("read-only global must not be flagged, got: %+v", r.Findings)
	}
}

func TestScan_ConstNotFlagged(t *testing.T) {
	src := `package p
const Limit = 10
func F() int { return Limit }
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "Limit") {
		t.Errorf("const must not be flagged, got: %+v", r.Findings)
	}
}

func TestScan_ErrorSentinelClean(t *testing.T) {
	src := `package p
import "errors"
var ErrNotFound = errors.New("not found")
func F() error { return ErrNotFound }
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "ErrNotFound") {
		t.Errorf("error sentinel (never reassigned) must not be flagged, got: %+v", r.Findings)
	}
}

// ─── mutation forms ──────────────────────────────────────

func TestScan_PlainAssignFlagged(t *testing.T) {
	src := `package p
var cur string
func Set(s string) { cur = s }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "cur") {
		t.Errorf("plain reassignment should flag, got: %+v", r.Findings)
	}
}

func TestScan_MapIndexWriteFlagged(t *testing.T) {
	src := `package p
var cache = map[string]int{}
func Put(k string, v int) { cache[k] = v }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "cache") {
		t.Errorf("map index write should flag the global, got: %+v", r.Findings)
	}
}

func TestScan_StructFieldWriteFlagged(t *testing.T) {
	src := `package p
type cfg struct{ N int }
var conf cfg
func Bump() { conf.N++ }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "conf") {
		t.Errorf("struct field mutation should flag the global, got: %+v", r.Findings)
	}
}

func TestScan_CompoundAssignFlagged(t *testing.T) {
	src := `package p
var total int
func Add(n int) { total += n }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "total") {
		t.Errorf("compound assign should flag, got: %+v", r.Findings)
	}
}

// ─── severity ────────────────────────────────────────────

func TestScan_ExportedMutableHighSeverity(t *testing.T) {
	src := `package p
var Counter int
func Inc() { Counter++ }
`
	r := Scan(map[string]string{"x.go": src})
	if sev(r, "Counter") != "high" {
		t.Errorf("exported mutable global should be high severity, got %q", sev(r, "Counter"))
	}
}

func TestScan_UnexportedMutableMediumSeverity(t *testing.T) {
	src := `package p
var counter int
func Inc() { counter++ }
`
	r := Scan(map[string]string{"x.go": src})
	if sev(r, "counter") != "medium" {
		t.Errorf("unexported mutable global should be medium severity, got %q", sev(r, "counter"))
	}
}

// ─── conservatism: local shadow ──────────────────────────

func TestScan_ShadowedNameNotFlagged(t *testing.T) {
	// Global `n` exists, but some function declares a local `n` and mutates THAT.
	// Without type info we can't tell which `n` is mutated → conservatively skip.
	src := `package p
var n int
func Other() {
	n := 0
	n++
	_ = n
}
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "n") {
		t.Errorf("name shadowed by a local must be conservatively skipped, got: %+v", r.Findings)
	}
}

func TestScan_ParamNameShadowNotFlagged(t *testing.T) {
	src := `package p
var val int
func F(val int) { val = 5; _ = val }
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "val") {
		t.Errorf("global shadowed by a param must be conservatively skipped, got: %+v", r.Findings)
	}
}

// ─── scope: locals & cross-package isolation ─────────────

func TestScan_LocalVarNotGlobal(t *testing.T) {
	src := `package p
func F() {
	var x int
	x++
	_ = x
}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("function-local var is not a global, got: %+v", r.Findings)
	}
}

func TestScan_CrossPackageNoFalseMatch(t *testing.T) {
	// Global `g` in dir a; a mutation of a *local* `g` in dir b must not flag a's global.
	a := `package a
var g int
func Read() int { return g }
`
	b := `package b
func F() { g := 0; g++; _ = g }
`
	r := Scan(map[string]string{"a/x.go": a, "b/y.go": b})
	if has(r, "g") {
		t.Errorf("cross-package name must not flag a's read-only global, got: %+v", r.Findings)
	}
}

// ─── contract: skips & robustness ────────────────────────

func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
var count int
func Inc() { count++ }
`
	r := Scan(map[string]string{"x_test.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("_test.go must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"README.md": "var count int"})
	if r.FilesScanned != 0 {
		t.Errorf("non-go must not be scanned, FilesScanned=%d", r.FilesScanned)
	}
}

func TestScan_ParseErrorNoCrash(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nvar ("})
	found := false
	for _, f := range r.Findings {
		if f.Rule == "parse-error" {
			found = true
		}
	}
	if !found {
		t.Error("broken source should yield a parse-error finding, not a crash")
	}
}

func TestScan_EmptyInput(t *testing.T) {
	r := Scan(map[string]string{})
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Errorf("empty input should be empty report, got %+v", r)
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := `package p
var a int
var b int
func F() { a++; b++ }
`
	x := Scan(map[string]string{"x.go": src})
	y := Scan(map[string]string{"x.go": src})
	if len(x.Findings) != len(y.Findings) {
		t.Fatalf("non-deterministic count: %d vs %d", len(x.Findings), len(y.Findings))
	}
	for i := range x.Findings {
		if x.Findings[i] != y.Findings[i] {
			t.Errorf("finding %d differs: %+v vs %+v", i, x.Findings[i], y.Findings[i])
		}
	}
}

func TestScan_Counts(t *testing.T) {
	src := `package p
var a int
var b = 5
const c = 1
func F() { a++ }
`
	r := Scan(map[string]string{"x.go": src})
	if r.Globals != 2 { // a, b are vars; c is const
		t.Errorf("Globals: want 2, got %d", r.Globals)
	}
	if r.Flagged != 1 { // only a is mutated
		t.Errorf("Flagged: want 1, got %d", r.Flagged)
	}
}
