package typeassert

import "testing"

func has(r Report, fn string) bool {
	for _, f := range r.Findings {
		if f.Func == fn && f.Rule == "unchecked-type-assert" {
			return true
		}
	}
	return false
}

func countReal(r Report) int {
	n := 0
	for _, f := range r.Findings {
		if f.Rule != "parse-error" {
			n++
		}
	}
	return n
}

// ─── single-value (panics) vs comma-ok (safe) ────────────

func TestScan_SingleValueAssignFlagged(t *testing.T) {
	src := `package p
func F(x any) {
	v := x.(int)
	_ = v
}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "F") {
		t.Errorf("single-value assertion should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_CommaOkClean(t *testing.T) {
	src := `package p
func F(x any) {
	v, ok := x.(int)
	_, _ = v, ok
}
`
	r := Scan(map[string]string{"x.go": src})
	if countReal(r) != 0 {
		t.Errorf("comma-ok assertion is safe, got: %+v", r.Findings)
	}
}

func TestScan_CommaOkPlainAssignClean(t *testing.T) {
	src := `package p
func F(x any) {
	var v int
	var ok bool
	v, ok = x.(int)
	_, _ = v, ok
}
`
	r := Scan(map[string]string{"x.go": src})
	if countReal(r) != 0 {
		t.Errorf("comma-ok plain assign is safe, got: %+v", r.Findings)
	}
}

func TestScan_TypeSwitchClean(t *testing.T) {
	src := `package p
func F(x any) string {
	switch x.(type) {
	case int:
		return "int"
	default:
		return "other"
	}
}
`
	r := Scan(map[string]string{"x.go": src})
	if countReal(r) != 0 {
		t.Errorf("type switch is safe, got: %+v", r.Findings)
	}
}

func TestScan_ReturnAssertFlagged(t *testing.T) {
	src := `package p
func F(x any) int { return x.(int) }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "F") {
		t.Errorf("return of single-value assertion should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_FuncArgAssertFlagged(t *testing.T) {
	src := `package p
func sink(int) {}
func F(x any) { sink(x.(int)) }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "F") {
		t.Errorf("assertion as func arg should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_BlankSingleAssignFlagged(t *testing.T) {
	// `_ = x.(T)` is single-value — it still panics on mismatch.
	src := `package p
func F(x any) { _ = x.(int) }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "F") {
		t.Errorf("_ = x.(T) is single-value and panics; should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_BlankCommaOkClean(t *testing.T) {
	src := `package p
func F(x any) { _, ok := x.(int); _ = ok }
`
	r := Scan(map[string]string{"x.go": src})
	if countReal(r) != 0 {
		t.Errorf("_, ok := x.(T) is safe, got: %+v", r.Findings)
	}
}

func TestScan_VarSpecCommaOkClean(t *testing.T) {
	src := `package p
var x any
var v, ok = x.(int)
func use() { _, _ = v, ok }
`
	r := Scan(map[string]string{"x.go": src})
	if countReal(r) != 0 {
		t.Errorf("var v, ok = x.(T) is comma-ok safe, got: %+v", r.Findings)
	}
}

// ─── attribution + closures ──────────────────────────────

func TestScan_MethodNameQualified(t *testing.T) {
	src := `package p
type T struct{}
func (t T) M(x any) { v := x.(int); _ = v }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "(T).M") {
		t.Errorf("method should be named (T).M, got: %+v", r.Findings)
	}
}

func TestScan_ClosureAttributedToEnclosing(t *testing.T) {
	src := `package p
func Outer(x any) {
	f := func() { v := x.(int); _ = v }
	f()
}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "Outer") {
		t.Errorf("assertion in closure should be attributed to Outer, got: %+v", r.Findings)
	}
}

func TestScan_MultipleAssertsCounted(t *testing.T) {
	src := `package p
func F(x, y any) {
	a := x.(int)
	b := y.(string)
	_, _ = a, b
}
`
	r := Scan(map[string]string{"x.go": src})
	if countReal(r) != 2 {
		t.Errorf("two single-value assertions expected, got %d: %+v", countReal(r), r.Findings)
	}
}

// ─── contract: skips & robustness ────────────────────────

func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
func F(x any) { v := x.(int); _ = v }
`
	r := Scan(map[string]string{"x_test.go": src})
	if countReal(r) != 0 {
		t.Errorf("_test.go must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_TestFuncSkipped(t *testing.T) {
	src := `package p
import "testing"
func TestX(t *testing.T) { var x any; v := x.(int); _ = v }
`
	r := Scan(map[string]string{"x.go": src})
	if countReal(r) != 0 {
		t.Errorf("TestXxx must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"README.md": "v := x.(int)"})
	if r.FilesScanned != 0 {
		t.Errorf("non-go must not be scanned, FilesScanned=%d", r.FilesScanned)
	}
}

func TestScan_ParseErrorNoCrash(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("})
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
func F(x, y any) { a := x.(int); b := y.(int); _, _ = a, b }
`
	a := Scan(map[string]string{"x.go": src})
	b := Scan(map[string]string{"x.go": src})
	if len(a.Findings) != len(b.Findings) {
		t.Fatalf("non-deterministic count")
	}
	for i := range a.Findings {
		if a.Findings[i] != b.Findings[i] {
			t.Errorf("finding %d differs: %+v vs %+v", i, a.Findings[i], b.Findings[i])
		}
	}
}

func TestScan_FlaggedCount(t *testing.T) {
	src := `package p
func F(x any) {
	a := x.(int)        // flagged
	b, ok := x.(string) // safe
	_, _, _ = a, b, ok
}
`
	r := Scan(map[string]string{"x.go": src})
	if r.Flagged != 1 {
		t.Errorf("Flagged: want 1, got %d", r.Flagged)
	}
}
