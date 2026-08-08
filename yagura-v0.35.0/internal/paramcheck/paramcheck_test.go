package paramcheck

import "testing"

// findFunc は Report から名前で 1 関数を引く(テスト用ヘルパ)。
func findFunc(r Report, name string) (FuncParams, bool) {
	for _, f := range r.Functions {
		if f.Func == name {
			return f, true
		}
	}
	return FuncParams{}, false
}

func hasFinding(r Report, name string) bool {
	for _, f := range r.Findings {
		if f.Func == name && f.Rule == "long-param-list" {
			return true
		}
	}
	return false
}

func TestScan_CountsParamsPerName(t *testing.T) {
	src := `package p
func two(a, b int) {}
func grouped(a, b, c int, d string) {}
`
	r := Scan(map[string]string{"x.go": src}, 5)
	if f, ok := findFunc(r, "two"); !ok || f.Params != 2 {
		t.Fatalf("two: want 2 params, got %+v (ok=%v)", f, ok)
	}
	// grouped: a,b,c (3) + d (1) = 4 個の引数(group ではなく名前単位)
	if f, ok := findFunc(r, "grouped"); !ok || f.Params != 4 {
		t.Fatalf("grouped: want 4 params, got %+v (ok=%v)", f, ok)
	}
}

func TestScan_VariadicCountsAsOne(t *testing.T) {
	src := `package p
func variadic(name string, xs ...int) {}
`
	r := Scan(map[string]string{"x.go": src}, 5)
	if f, ok := findFunc(r, "variadic"); !ok || f.Params != 2 {
		t.Fatalf("variadic: want 2 params, got %+v (ok=%v)", f, ok)
	}
}

func TestScan_ReceiverExcluded(t *testing.T) {
	src := `package p
type T struct{}
func (s *T) M(a, b int) {}
`
	r := Scan(map[string]string{"x.go": src}, 5)
	if f, ok := findFunc(r, "(*T).M"); !ok || f.Params != 2 {
		t.Fatalf("method: want 2 params (receiver excluded), got %+v (ok=%v)", f, ok)
	}
}

func TestScan_BlankParamCounts(t *testing.T) {
	src := `package p
func cb(w int, _ error, x string) {}
`
	r := Scan(map[string]string{"x.go": src}, 5)
	if f, ok := findFunc(r, "cb"); !ok || f.Params != 3 {
		t.Fatalf("cb: want 3 params (blank counts), got %+v (ok=%v)", f, ok)
	}
}

func TestScan_OverThresholdFlagged(t *testing.T) {
	src := `package p
func small(a, b int) {}
func big(a, b, c, d, e, f int) {}
`
	r := Scan(map[string]string{"x.go": src}, 5)
	if hasFinding(r, "small") {
		t.Error("small (2 params) should NOT be flagged at threshold 5")
	}
	if !hasFinding(r, "big") {
		t.Error("big (6 params) SHOULD be flagged at threshold 5")
	}
	if r.OverThreshold != 1 {
		t.Errorf("OverThreshold: want 1, got %d", r.OverThreshold)
	}
}

func TestScan_ThresholdBoundaryExclusive(t *testing.T) {
	// 丁度 threshold 個は flag しない(complexity と同じ c>threshold セマンティクス)。
	src := `package p
func exactly5(a, b, c, d, e int) {}
`
	r := Scan(map[string]string{"x.go": src}, 5)
	if hasFinding(r, "exactly5") {
		t.Error("exactly 5 params at threshold 5 should NOT be flagged (exclusive)")
	}
}

func TestScan_CustomThreshold(t *testing.T) {
	src := `package p
func three(a, b, c int) {}
`
	r := Scan(map[string]string{"x.go": src}, 2)
	if !hasFinding(r, "three") {
		t.Error("three params at threshold 2 should be flagged")
	}
}

func TestScan_DefaultThresholdWhenZero(t *testing.T) {
	r := Scan(map[string]string{"x.go": "package p\n"}, 0)
	if r.Threshold != defaultThreshold {
		t.Errorf("threshold<=0 should default to %d, got %d", defaultThreshold, r.Threshold)
	}
}

func TestScan_FuncLitSkipped(t *testing.T) {
	// 入れ子クロージャ(コールバック署名)は smell の対象外 — FuncDecl のみ計上。
	src := `package p
func outer() {
	cb := func(a, b, c, d, e, f int) {}
	_ = cb
}
`
	r := Scan(map[string]string{"x.go": src}, 5)
	for _, f := range r.Functions {
		if f.Func != "outer" {
			t.Errorf("only FuncDecl should be counted, got closure %q", f.Func)
		}
	}
	if r.OverThreshold != 0 {
		t.Errorf("closure params must not be flagged, OverThreshold=%d", r.OverThreshold)
	}
}

func TestScan_MaxAndAvg(t *testing.T) {
	src := `package p
func a(x int) {}
func b(x, y, z int) {}
`
	r := Scan(map[string]string{"x.go": src}, 5)
	if r.MaxParams != 3 {
		t.Errorf("MaxParams: want 3, got %d", r.MaxParams)
	}
	if r.AvgParams != 2.0 { // (1+3)/2
		t.Errorf("AvgParams: want 2.0, got %v", r.AvgParams)
	}
}

func TestScan_SeverityHighForVeryLong(t *testing.T) {
	src := `package p
func huge(a, b, c, d, e, f, g, h, i int) {}
`
	r := Scan(map[string]string{"x.go": src}, 5)
	var sev string
	for _, f := range r.Findings {
		if f.Func == "huge" {
			sev = f.Severity
		}
	}
	if sev != "high" {
		t.Errorf("9-param func should be high severity, got %q", sev)
	}
}

func TestScan_ParseErrorReported(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("}, 5)
	found := false
	for _, f := range r.Findings {
		if f.Rule == "parse-error" {
			found = true
		}
	}
	if !found {
		t.Error("malformed source should yield a parse-error finding")
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := `package p
func z(a, b, c, d, e, f int) {}
func a(g, h, i, j, k, l int) {}
`
	r1 := Scan(map[string]string{"x.go": src}, 5)
	r2 := Scan(map[string]string{"x.go": src}, 5)
	if len(r1.Findings) != len(r2.Findings) || len(r1.Findings) != 2 {
		t.Fatalf("want 2 findings deterministically, got %d / %d", len(r1.Findings), len(r2.Findings))
	}
	// File→Line→Func ソートなので a が z より先(同 file は Line 順 = 宣言順)。
	if r1.Findings[0].Func != "z" || r1.Findings[1].Func != "a" {
		t.Errorf("findings must be in line order, got %q then %q", r1.Findings[0].Func, r1.Findings[1].Func)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"readme.md": "# not go\nfunc fake(a,b,c,d,e,f int)"}, 5)
	if r.FilesScanned != 0 {
		t.Errorf("non-.go files must be skipped, FilesScanned=%d", r.FilesScanned)
	}
}
