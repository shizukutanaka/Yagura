package nakedret

import "testing"

func has(r Report, fn string) bool {
	for _, f := range r.Findings {
		if f.Func == fn && f.Rule == "naked-return-long-func" {
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

// buildLong は named-result + naked return を持つ「長い」関数を作る(N 行の本体)。
func longBody(lines int) string {
	body := ""
	for i := 0; i < lines; i++ {
		body += "\tx++\n"
	}
	return body
}

// ─── core rule ───────────────────────────────────────────

func TestScan_NakedReturnInLongFuncFlagged(t *testing.T) {
	src := `package p
func Big() (x int) {
` + longBody(40) + `	return
}
`
	r := Scan(map[string]string{"x.go": src}, 30)
	if !has(r, "Big") {
		t.Errorf("naked return in 40-line named-result func should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_NakedReturnInShortFuncClean(t *testing.T) {
	src := `package p
func Small() (x int) {
	x = 1
	return
}
`
	r := Scan(map[string]string{"x.go": src}, 30)
	if countReal(r) != 0 {
		t.Errorf("naked return in short func is fine, got: %+v", r.Findings)
	}
}

func TestScan_ExplicitReturnInLongFuncClean(t *testing.T) {
	src := `package p
func Big() (x int) {
` + longBody(40) + `	return x
}
`
	r := Scan(map[string]string{"x.go": src}, 30)
	if countReal(r) != 0 {
		t.Errorf("explicit return should be clean even in long func, got: %+v", r.Findings)
	}
}

// A long function with UNNAMED results cannot have a naked return (compile error),
// so such functions are simply never flagged. Verify no false positive on explicit return.
func TestScan_UnnamedResultsLongFuncClean(t *testing.T) {
	src := `package p
func Big() int {
` + longBody(40) + `	return x
}
`
	r := Scan(map[string]string{"x.go": src}, 30)
	if countReal(r) != 0 {
		t.Errorf("unnamed-result func must not be flagged, got: %+v", r.Findings)
	}
}

func TestScan_MultipleNakedReturnsEachFlagged(t *testing.T) {
	src := `package p
func Big() (x int) {
	if x > 0 {
` + longBody(20) + `		return
	}
` + longBody(20) + `	return
}
`
	r := Scan(map[string]string{"x.go": src}, 30)
	if countReal(r) != 2 {
		t.Errorf("each naked return in a long func should be flagged; want 2, got %d: %+v", countReal(r), r.Findings)
	}
}

// Threshold boundary: a func exactly at threshold lines is NOT flagged (strictly greater).
func TestScan_ThresholdBoundary(t *testing.T) {
	// func spanning exactly 5 lines, threshold 5 → not flagged
	src := `package p
func F() (x int) {
	x = 1
	return
}
`
	r := Scan(map[string]string{"x.go": src}, 5)
	if countReal(r) != 0 {
		t.Errorf("func at exactly threshold should not be flagged, got: %+v", r.Findings)
	}
}

func TestScan_DefaultThresholdWhenZero(t *testing.T) {
	// 40-line func, threshold 0 → default 30 applies → flagged
	src := `package p
func Big() (x int) {
` + longBody(40) + `	return
}
`
	r := Scan(map[string]string{"x.go": src}, 0)
	if !has(r, "Big") {
		t.Errorf("threshold 0 should fall back to default 30 and flag, got: %+v", r.Findings)
	}
}

// ─── closures: naked return binds to innermost function ──

func TestScan_NakedReturnInLongClosureFlagged(t *testing.T) {
	src := `package p
func Outer() {
	f := func() (x int) {
` + longBody(40) + `		return
	}
	_ = f
}
`
	r := Scan(map[string]string{"x.go": src}, 30)
	if countReal(r) != 1 {
		t.Errorf("naked return in long closure should be flagged once, got: %+v", r.Findings)
	}
}

func TestScan_ShortClosureInLongFuncClean(t *testing.T) {
	// Outer is long, but the naked return is in a SHORT closure → not flagged.
	src := `package p
func Outer() (y int) {
` + longBody(40) + `	f := func() (x int) {
		x = 1
		return
	}
	_ = f
	return y
}
`
	r := Scan(map[string]string{"x.go": src}, 30)
	if countReal(r) != 0 {
		t.Errorf("naked return belongs to the short closure, not the long outer; got: %+v", r.Findings)
	}
}

// ─── contract: skips & robustness ────────────────────────

func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
func Big() (x int) {
` + longBody(40) + `	return
}
`
	r := Scan(map[string]string{"x_test.go": src}, 30)
	if countReal(r) != 0 {
		t.Errorf("_test.go must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"README.md": "func Big() (x int) { return }"}, 30)
	if r.FilesScanned != 0 {
		t.Errorf("non-go must not be scanned, FilesScanned=%d", r.FilesScanned)
	}
}

func TestScan_ParseErrorNoCrash(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("}, 30)
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
	r := Scan(map[string]string{}, 30)
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Errorf("empty input should be empty report, got %+v", r)
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := `package p
func A() (x int) {
` + longBody(40) + `	return
}
func B() (y int) {
` + longBody(40) + `	return
}
`
	a := Scan(map[string]string{"x.go": src}, 30)
	b := Scan(map[string]string{"x.go": src}, 30)
	if len(a.Findings) != len(b.Findings) {
		t.Fatalf("non-deterministic count: %d vs %d", len(a.Findings), len(b.Findings))
	}
	for i := range a.Findings {
		if a.Findings[i] != b.Findings[i] {
			t.Errorf("finding %d differs: %+v vs %+v", i, a.Findings[i], b.Findings[i])
		}
	}
}

func TestScan_MethodNameQualified(t *testing.T) {
	src := `package p
type T struct{}
func (t T) Big() (x int) {
` + longBody(40) + `	return
}
`
	r := Scan(map[string]string{"x.go": src}, 30)
	if !has(r, "(T).Big") {
		t.Errorf("method should be named (T).Big, got: %+v", r.Findings)
	}
}

func TestScan_FuncLengthReported(t *testing.T) {
	src := `package p
func Big() (x int) {
` + longBody(40) + `	return
}
`
	r := Scan(map[string]string{"x.go": src}, 30)
	if len(r.Findings) == 0 || r.Findings[0].FuncLines < 40 {
		t.Errorf("finding should report func line count >= 40, got: %+v", r.Findings)
	}
}
