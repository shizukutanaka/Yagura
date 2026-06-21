package calibrate

import "testing"

func dist(r Report, metric string) (Distribution, bool) {
	for _, d := range r.Distributions {
		if d.Metric == metric {
			return d, true
		}
	}
	return Distribution{}, false
}

// fnWithParams は n 個の引数を持つ関数ソースを生成する。
func fnWithParams(name string, n int) string {
	s := "func " + name + "("
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ", "
		}
		s += "p" + string(rune('a'+i)) + " int"
	}
	s += ") {}\n"
	return s
}

func TestScan_ParamDistribution(t *testing.T) {
	// 5 functions with 1,2,3,4,5 params.
	src := "package p\n"
	for i := 1; i <= 5; i++ {
		src += fnWithParams("F"+string(rune('0'+i)), i)
	}
	r := Scan(map[string]string{"x.go": src})
	d, ok := dist(r, "params")
	if !ok {
		t.Fatal("params distribution missing")
	}
	if d.Count != 5 {
		t.Errorf("Count: want 5, got %d", d.Count)
	}
	if d.Min != 1 || d.Max != 5 {
		t.Errorf("Min/Max: want 1/5, got %d/%d", d.Min, d.Max)
	}
	if d.Median != 3 {
		t.Errorf("Median: want 3, got %v", d.Median)
	}
	if d.Mean != 3 {
		t.Errorf("Mean: want 3, got %v", d.Mean)
	}
	// CurrentDefault for params is 5 (param-check --max 5). 0 functions strictly exceed 5.
	if d.CurrentDefault != 5 {
		t.Errorf("CurrentDefault: want 5, got %d", d.CurrentDefault)
	}
	if d.OverCurrentDefault != 0 {
		t.Errorf("OverCurrentDefault: want 0 (none > 5), got %d", d.OverCurrentDefault)
	}
}

func TestScan_SuggestedThresholdIsCeilP95(t *testing.T) {
	src := "package p\n"
	for i := 1; i <= 5; i++ {
		src += fnWithParams("F"+string(rune('0'+i)), i)
	}
	r := Scan(map[string]string{"x.go": src})
	d, _ := dist(r, "params")
	// sorted [1,2,3,4,5], p95 rank = 0.95*4 = 3.8 → 4 + 0.8*(5-4) = 4.8 → ceil 5
	if d.P95 < 4.7 || d.P95 > 4.9 {
		t.Errorf("P95: want ~4.8, got %v", d.P95)
	}
	if d.SuggestedThreshold != 5 {
		t.Errorf("SuggestedThreshold: want ceil(4.8)=5, got %d", d.SuggestedThreshold)
	}
}

func TestScan_ReturnsDistribution(t *testing.T) {
	src := `package p
func A() {}
func B() int { return 0 }
func C() (int, error) { return 0, nil }
func D() (a, b, c, d int) { return }
`
	r := Scan(map[string]string{"x.go": src})
	d, ok := dist(r, "returns")
	if !ok {
		t.Fatal("returns distribution missing")
	}
	if d.Count != 4 || d.Min != 0 || d.Max != 4 {
		t.Errorf("returns: want count4/min0/max4, got %d/%d/%d", d.Count, d.Min, d.Max)
	}
	if d.CurrentDefault != 3 {
		t.Errorf("returns CurrentDefault: want 3, got %d", d.CurrentDefault)
	}
	// D has 4 returns, strictly > 3 → 1 over.
	if d.OverCurrentDefault != 1 {
		t.Errorf("returns OverCurrentDefault: want 1, got %d", d.OverCurrentDefault)
	}
}

func TestScan_ComplexityMatchesMcCabe(t *testing.T) {
	// Plain func = 1; one if = 2; if + for = 3; if with && = +1.
	src := `package p
func Plain() {}
func OneIf(x int) {
	if x > 0 {
	}
}
func IfAndFor(x int) {
	if x > 0 {
	}
	for i := 0; i < x; i++ {
	}
}
`
	r := Scan(map[string]string{"x.go": src})
	d, ok := dist(r, "complexity")
	if !ok {
		t.Fatal("complexity distribution missing")
	}
	if d.Count != 3 || d.Min != 1 || d.Max != 3 {
		t.Errorf("complexity: want count3/min1/max3, got %d/%d/%d", d.Count, d.Min, d.Max)
	}
	if d.CurrentDefault != 10 {
		t.Errorf("complexity CurrentDefault: want 10, got %d", d.CurrentDefault)
	}
}

func TestScan_ComplexityCountsAndOr(t *testing.T) {
	src := `package p
func F(a, b bool) {
	if a && b {
	}
}
`
	r := Scan(map[string]string{"x.go": src})
	d, _ := dist(r, "complexity")
	// base 1 + if 1 + && 1 = 3
	if d.Max != 3 {
		t.Errorf("complexity with &&: want max 3, got %d", d.Max)
	}
}

func TestScan_FuncLinesDistribution(t *testing.T) {
	body := ""
	for i := 0; i < 10; i++ {
		body += "\tx++\n"
	}
	src := "package p\nfunc Big(x int) {\n" + body + "}\n"
	r := Scan(map[string]string{"x.go": src})
	d, ok := dist(r, "func_lines")
	if !ok {
		t.Fatal("func_lines distribution missing")
	}
	if d.CurrentDefault != 30 {
		t.Errorf("func_lines CurrentDefault: want 30, got %d", d.CurrentDefault)
	}
	if d.Max < 11 {
		t.Errorf("func_lines max: want >=11, got %d", d.Max)
	}
}

func TestScan_AllFourMetricsPresent(t *testing.T) {
	src := `package p
func F(a int) int { return a }
`
	r := Scan(map[string]string{"x.go": src})
	for _, m := range []string{"complexity", "params", "returns", "func_lines"} {
		if _, ok := dist(r, m); !ok {
			t.Errorf("metric %q missing from Distributions", m)
		}
	}
}

// ─── contract: skips & robustness ────────────────────────

func TestScan_MethodsCounted(t *testing.T) {
	src := `package p
type T struct{}
func (t T) M(a, b int) {}
`
	r := Scan(map[string]string{"x.go": src})
	d, _ := dist(r, "params")
	if d.Count != 1 || d.Max != 2 {
		t.Errorf("method should be counted with 2 params, got count %d max %d", d.Count, d.Max)
	}
}

func TestScan_FuncLitNotCounted(t *testing.T) {
	src := `package p
func F() {
	g := func(a, b, c, d, e int) {}
	_ = g
}
`
	r := Scan(map[string]string{"x.go": src})
	d, _ := dist(r, "params")
	// Only F (0 params) counted; the closure's 5 params are ignored.
	if d.Count != 1 || d.Max != 0 {
		t.Errorf("func literal must not be counted, got count %d max %d", d.Count, d.Max)
	}
}

func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
func F(a, b, c, d, e, f int) {}
`
	r := Scan(map[string]string{"x_test.go": src})
	if r.FuncsScanned != 0 {
		t.Errorf("_test.go must be skipped, FuncsScanned=%d", r.FuncsScanned)
	}
}

func TestScan_TestFuncSkipped(t *testing.T) {
	src := `package p
import "testing"
func TestX(t *testing.T) {}
`
	r := Scan(map[string]string{"x.go": src})
	if r.FuncsScanned != 0 {
		t.Errorf("TestXxx must be skipped, FuncsScanned=%d", r.FuncsScanned)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"README.md": "func F() {}"})
	if r.FilesScanned != 0 {
		t.Errorf("non-go must not be scanned, FilesScanned=%d", r.FilesScanned)
	}
}

func TestScan_ParseErrorNoCrash(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("})
	// Should not crash; no functions counted.
	if r.FuncsScanned != 0 {
		t.Errorf("broken file should yield 0 functions, got %d", r.FuncsScanned)
	}
}

func TestScan_EmptyInput(t *testing.T) {
	r := Scan(map[string]string{})
	if r.FilesScanned != 0 || r.FuncsScanned != 0 {
		t.Errorf("empty input should be empty, got %+v", r)
	}
	// Distributions still present but all zero-count.
	for _, m := range []string{"complexity", "params", "returns", "func_lines"} {
		d, ok := dist(r, m)
		if !ok || d.Count != 0 {
			t.Errorf("metric %q should be present with count 0", m)
		}
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := `package p
func A(a, b int) int { if a > 0 { return a }; return b }
func B(x int) {}
`
	a := Scan(map[string]string{"x.go": src})
	b := Scan(map[string]string{"x.go": src})
	if len(a.Distributions) != len(b.Distributions) {
		t.Fatal("non-deterministic distribution count")
	}
	for i := range a.Distributions {
		if a.Distributions[i] != b.Distributions[i] {
			t.Errorf("distribution %d differs: %+v vs %+v", i, a.Distributions[i], b.Distributions[i])
		}
	}
}

func TestScan_FuncsScannedTracked(t *testing.T) {
	src := `package p
func A() {}
func B() {}
func C() {}
`
	r := Scan(map[string]string{"x.go": src})
	if r.FuncsScanned != 3 {
		t.Errorf("FuncsScanned: want 3, got %d", r.FuncsScanned)
	}
}
