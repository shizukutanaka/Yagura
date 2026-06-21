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

// ─── Tukey-fence outlier detection (v0.81) ───────────────

func hasOutlier(r Report, fn, metric string) bool {
	for _, o := range r.Outliers {
		if o.Func == fn && o.Metric == metric {
			return true
		}
	}
	return false
}

func TestScan_P25Present(t *testing.T) {
	src := "package p\n"
	for i := 1; i <= 5; i++ {
		src += fnWithParams("F"+string(rune('0'+i)), i)
	}
	r := Scan(map[string]string{"x.go": src})
	d, _ := dist(r, "params")
	// sorted [1,2,3,4,5], p25 rank = 0.25*4 = 1 → sorted[1] = 2
	if d.P25 != 2 {
		t.Errorf("P25: want 2, got %v", d.P25)
	}
}

func TestScan_ComplexityOutlierDetected(t *testing.T) {
	// Nine trivial funcs (complexity 1) + one very complex func.
	src := "package p\n"
	for i := 0; i < 9; i++ {
		src += "func Triv" + string(rune('a'+i)) + "() {}\n"
	}
	// Monster: many ifs → high complexity.
	src += "func Monster(x int) {\n"
	for i := 0; i < 15; i++ {
		src += "\tif x > " + string(rune('0'+i%10)) + " {\n\t}\n"
	}
	src += "}\n"
	r := Scan(map[string]string{"x.go": src})
	if !hasOutlier(r, "Monster", "complexity") {
		t.Errorf("Monster should be a complexity outlier, got outliers: %+v", r.Outliers)
	}
	// Trivial funcs must not be outliers.
	if hasOutlier(r, "Triva", "complexity") {
		t.Errorf("trivial func should not be an outlier")
	}
}

func TestScan_UniformNoOutliers(t *testing.T) {
	// All funcs identical (complexity 1, 0 params, 0 returns, ~1 line).
	src := "package p\n"
	for i := 0; i < 10; i++ {
		src += "func F" + string(rune('a'+i)) + "() {}\n"
	}
	r := Scan(map[string]string{"x.go": src})
	if len(r.Outliers) != 0 {
		t.Errorf("uniform corpus should have no outliers, got: %+v", r.Outliers)
	}
}

func TestScan_OutlierFieldsPopulated(t *testing.T) {
	// Trivial funcs each take 1 param so Q1=Q3=1 → fence = 1 (IQR 0, but positive Q3).
	src := "package p\n"
	for i := 0; i < 9; i++ {
		src += "func Triv" + string(rune('a'+i)) + "(p int) {}\n"
	}
	src += "func Big(a, b, c, d, e, f, g, h int) {}\n" // 8 params, far above the rest (1)
	r := Scan(map[string]string{"x.go": src})
	var found *Outlier
	for i := range r.Outliers {
		if r.Outliers[i].Func == "Big" && r.Outliers[i].Metric == "params" {
			found = &r.Outliers[i]
		}
	}
	if found == nil {
		t.Fatalf("Big should be a params outlier, got: %+v", r.Outliers)
	}
	if found.File != "x.go" || found.Line == 0 || found.Value != 8 {
		t.Errorf("outlier fields incomplete: %+v", *found)
	}
	if found.Fence != 1 {
		t.Errorf("outlier should carry the upper fence (Q3=1, IQR=0 → fence 1), got %v", found.Fence)
	}
}

func TestScan_OutliersDeterministicOrder(t *testing.T) {
	src := "package p\n"
	for i := 0; i < 9; i++ {
		src += "func Triv" + string(rune('a'+i)) + "() {}\n"
	}
	src += "func Big1(a, b, c, d, e, f int) {}\n"
	src += "func Big2(a, b, c, d, e, f, g, h int) {}\n"
	a := Scan(map[string]string{"x.go": src})
	b := Scan(map[string]string{"x.go": src})
	if len(a.Outliers) != len(b.Outliers) {
		t.Fatalf("non-deterministic outlier count: %d vs %d", len(a.Outliers), len(b.Outliers))
	}
	for i := range a.Outliers {
		if a.Outliers[i] != b.Outliers[i] {
			t.Errorf("outlier %d differs: %+v vs %+v", i, a.Outliers[i], b.Outliers[i])
		}
	}
}
