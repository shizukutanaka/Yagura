package nestdepth

import "testing"

func has(r Report, fn string) bool {
	for _, f := range r.Findings {
		if f.Func == fn && f.Rule == "deep-nesting" {
			return true
		}
	}
	return false
}

func depthOf(r Report, fn string) int {
	for _, f := range r.Findings {
		if f.Func == fn {
			return f.Depth
		}
	}
	return -1
}

// ─── core: pyramid vs flat ───────────────────────────────

func TestScan_DeepPyramidFlagged(t *testing.T) {
	src := `package p
func Pyramid(a, b, c, d, e int) {
	if a > 0 {
		for b > 0 {
			if c > 0 {
				if d > 0 {
					if e > 0 {
						println("deep")
					}
				}
			}
		}
	}
}
`
	r := Scan(map[string]string{"x.go": src}, 4)
	if !has(r, "Pyramid") {
		t.Fatalf("5-level pyramid should be flagged, got: %+v", r.Findings)
	}
	if d := depthOf(r, "Pyramid"); d != 5 {
		t.Errorf("depth: want 5, got %d", d)
	}
}

// Flat guard clauses: high complexity, but depth 1 — must NOT be flagged.
func TestScan_FlatGuardsClean(t *testing.T) {
	src := `package p
func Guards(a, b, c, d, e int) int {
	if a < 0 {
		return 0
	}
	if b < 0 {
		return 1
	}
	if c < 0 {
		return 2
	}
	if d < 0 {
		return 3
	}
	if e < 0 {
		return 4
	}
	return 5
}
`
	// Five flat guards: cyclomatic complexity ~6, but nesting depth is only 1.
	// At threshold 0 (=default 4) it must not be flagged.
	r := Scan(map[string]string{"x.go": src}, 0)
	if has(r, "Guards") {
		t.Errorf("flat guard clauses (depth 1) must not be flagged, got: %+v", r.Findings)
	}
}

// else-if chains stay flat: 4 else-if branches would be depth 4+ if chains
// counted as nesting, but they don't — so at threshold 1 it stays unflagged.
func TestScan_ElseIfChainStaysFlat(t *testing.T) {
	src := `package p
func Chain(a int) int {
	if a == 1 {
		return 1
	} else if a == 2 {
		return 2
	} else if a == 3 {
		return 3
	} else if a == 4 {
		return 4
	}
	return 0
}
`
	// threshold 1: a depth-1 chain is not > 1, so not flagged. If else-if
	// accumulated, depth would be ~4 and it WOULD be flagged.
	r := Scan(map[string]string{"x.go": src}, 1)
	if has(r, "Chain") {
		t.Errorf("else-if chain should stay depth 1 (not flagged at threshold 1), got: %+v", r.Findings)
	}
}

func TestScan_SwitchWithNestedIf(t *testing.T) {
	src := `package p
func Sw(a, b int) {
	switch a {
	case 1:
		if b > 0 {
			println("x")
		}
	}
}
`
	// threshold 1 → depth-2 func is flagged, so we can read its depth.
	r := Scan(map[string]string{"x.go": src}, 1)
	if d := depthOf(r, "Sw"); d != 2 {
		t.Errorf("switch+if depth: want 2, got %d", d)
	}
}

func TestScan_RangeInsideIf(t *testing.T) {
	src := `package p
func RI(xs []int, a int) {
	if a > 0 {
		for _, x := range xs {
			println(x)
		}
	}
}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	if d := depthOf(r, "RI"); d != 2 {
		t.Errorf("if+range depth: want 2, got %d", d)
	}
}

// ─── threshold ───────────────────────────────────────────

func TestScan_AtThresholdNotFlagged(t *testing.T) {
	// depth exactly 4 with threshold 4 → not flagged (flag is > threshold).
	src := `package p
func D4(a, b, c, d int) {
	if a > 0 {
		if b > 0 {
			if c > 0 {
				if d > 0 {
					println("4")
				}
			}
		}
	}
}
`
	r := Scan(map[string]string{"x.go": src}, 4)
	if has(r, "D4") {
		t.Errorf("depth 4 at threshold 4 should not be flagged, got: %+v", r.Findings)
	}
}

func TestScan_DefaultThresholdWhenZero(t *testing.T) {
	// 5-level → default threshold 4 → flagged.
	src := `package p
func P(a, b, c, d, e int) {
	if a > 0 {
		if b > 0 {
			if c > 0 {
				if d > 0 {
					if e > 0 {
						println("5")
					}
				}
			}
		}
	}
}
`
	r := Scan(map[string]string{"x.go": src}, 0)
	if !has(r, "P") {
		t.Errorf("threshold 0 should default to 4 and flag depth 5, got: %+v", r.Findings)
	}
	if r.Threshold != 4 {
		t.Errorf("Threshold: want 4, got %d", r.Threshold)
	}
}

// ─── severity ────────────────────────────────────────────

func TestScan_SeverityScales(t *testing.T) {
	// depth 5 → medium, depth 6 → high.
	d5 := `package p
func D5(a,b,c,d,e int){ if a>0 { if b>0 { if c>0 { if d>0 { if e>0 { println(1) } } } } } }
`
	r := Scan(map[string]string{"x.go": d5}, 4)
	for _, f := range r.Findings {
		if f.Func == "D5" && f.Severity != "medium" {
			t.Errorf("depth 5 severity: want medium, got %s", f.Severity)
		}
	}
}

// ─── method names + closures ─────────────────────────────

func TestScan_MethodNameQualified(t *testing.T) {
	src := `package p
type T struct{}
func (t T) Deep(a,b,c,d,e int) {
	if a>0 { if b>0 { if c>0 { if d>0 { if e>0 { println(1) } } } } }
}
`
	r := Scan(map[string]string{"x.go": src}, 4)
	if !has(r, "(T).Deep") {
		t.Errorf("method should be named (T).Deep, got: %+v", r.Findings)
	}
}

// A closure's internal nesting does not count toward the enclosing function.
func TestScan_ClosureNotCountedInOuter(t *testing.T) {
	src := `package p
func Outer() {
	f := func() {
		if true {
			if true {
				if true {
					if true {
						if true {
							println("deep in closure")
						}
					}
				}
			}
		}
	}
	_ = f
}
`
	r := Scan(map[string]string{"x.go": src}, 4)
	// Outer's own body has no control flow → depth 0 → not flagged.
	if has(r, "Outer") {
		t.Errorf("closure nesting must not count toward Outer, got: %+v", r.Findings)
	}
}

// ─── contract: skips & robustness ────────────────────────

func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
func P(a,b,c,d,e int){ if a>0 { if b>0 { if c>0 { if d>0 { if e>0 { println(1) } } } } } }
`
	r := Scan(map[string]string{"x_test.go": src}, 4)
	if len(r.Findings) != 0 {
		t.Errorf("_test.go must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_TestFuncSkipped(t *testing.T) {
	src := `package p
import "testing"
func TestX(t *testing.T) { if true { if true { if true { if true { if true { println(1) } } } } } }
`
	r := Scan(map[string]string{"x.go": src}, 4)
	if len(r.Findings) != 0 {
		t.Errorf("TestXxx must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"README.md": "if x { if y {} }"}, 4)
	if r.FilesScanned != 0 {
		t.Errorf("non-go must not be scanned, FilesScanned=%d", r.FilesScanned)
	}
}

func TestScan_ParseErrorNoCrash(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("}, 4)
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
	r := Scan(map[string]string{}, 4)
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Errorf("empty input should be empty report, got %+v", r)
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := `package p
func A(a,b,c,d,e int){ if a>0 { if b>0 { if c>0 { if d>0 { if e>0 { println(1) } } } } } }
func B(a,b,c,d,e int){ if a>0 { if b>0 { if c>0 { if d>0 { if e>0 { println(1) } } } } } }
`
	x := Scan(map[string]string{"x.go": src}, 4)
	y := Scan(map[string]string{"x.go": src}, 4)
	if len(x.Findings) != len(y.Findings) {
		t.Fatalf("non-deterministic count")
	}
	for i := range x.Findings {
		if x.Findings[i] != y.Findings[i] {
			t.Errorf("finding %d differs", i)
		}
	}
}

func TestScan_MaxDepthTracked(t *testing.T) {
	src := `package p
func P(a,b,c,d,e int){ if a>0 { if b>0 { if c>0 { if d>0 { if e>0 { println(1) } } } } } }
`
	r := Scan(map[string]string{"x.go": src}, 4)
	if r.MaxDepth != 5 {
		t.Errorf("MaxDepth: want 5, got %d", r.MaxDepth)
	}
	if r.FuncsScanned != 1 {
		t.Errorf("FuncsScanned: want 1, got %d", r.FuncsScanned)
	}
}
