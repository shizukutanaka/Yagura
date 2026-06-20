package returncheck

import "testing"

func hasFinding(r Report, funcName string) bool {
	for _, f := range r.Findings {
		if f.Func == funcName && f.Rule == "many-returns" {
			return true
		}
	}
	return false
}

func findFinding(r Report, funcName string) (Finding, bool) {
	for _, f := range r.Findings {
		if f.Func == funcName {
			return f, true
		}
	}
	return Finding{}, false
}

// TestScan_BeyondThresholdFlagged: threshold=3 → 4 returns はフラグ。
func TestScan_BeyondThresholdFlagged(t *testing.T) {
	src := `package p
func quad() (string, int, bool, error) { return "", 0, false, nil }
`
	r := Scan(map[string]string{"x.go": src}, 3)
	if !hasFinding(r, "quad") {
		t.Error("quad (4 returns) should be flagged at threshold 3")
	}
	if r.TooManyReturns != 1 {
		t.Errorf("TooManyReturns: want 1, got %d", r.TooManyReturns)
	}
}

// TestScan_AtThresholdNotFlagged: 丁度 threshold 個は flag しない(exclusive)。
func TestScan_AtThresholdNotFlagged(t *testing.T) {
	src := `package p
func triple() (string, int, error) { return "", 0, nil }
`
	r := Scan(map[string]string{"x.go": src}, 3)
	if hasFinding(r, "triple") {
		t.Error("triple (3 returns) at threshold 3 should NOT be flagged (exclusive)")
	}
}

// TestScan_NoReturns: 戻り値なしは flag しない。
func TestScan_NoReturns(t *testing.T) {
	src := `package p
func nothing() {}
`
	r := Scan(map[string]string{"x.go": src}, 3)
	if r.TooManyReturns != 0 {
		t.Errorf("no-return func should not be flagged, TooManyReturns=%d", r.TooManyReturns)
	}
}

// TestScan_SingleReturn: 1 戻り値は flag しない。
func TestScan_SingleReturn(t *testing.T) {
	src := `package p
func single() error { return nil }
`
	r := Scan(map[string]string{"x.go": src}, 3)
	if r.TooManyReturns != 0 {
		t.Errorf("single-return func should not be flagged, TooManyReturns=%d", r.TooManyReturns)
	}
}

// TestScan_NamedReturnsCountSame: 名前付き戻り値も同じように計数。
func TestScan_NamedReturnsCountSame(t *testing.T) {
	src := `package p
func named() (a string, b int, c bool, d error) { return }
`
	r := Scan(map[string]string{"x.go": src}, 3)
	f, ok := findFinding(r, "named")
	if !ok {
		t.Fatal("named (4 named returns) should be flagged")
	}
	if f.ReturnCount != 4 {
		t.Errorf("ReturnCount: want 4, got %d", f.ReturnCount)
	}
}

// TestScan_GroupedNamed: `a, b string` は 2 件として計数。
func TestScan_GroupedNamed(t *testing.T) {
	src := `package p
func grouped() (a, b string, c, d int) { return }
`
	r := Scan(map[string]string{"x.go": src}, 3)
	f, ok := findFinding(r, "grouped")
	if !ok {
		t.Fatal("grouped (4 named returns) should be flagged")
	}
	if f.ReturnCount != 4 {
		t.Errorf("ReturnCount: want 4, got %d", f.ReturnCount)
	}
}

// TestScan_SeverityLow: 4 returns → low severity。
func TestScan_SeverityLow(t *testing.T) {
	src := `package p
func four() (string, int, bool, error) { return "", 0, false, nil }
`
	r := Scan(map[string]string{"x.go": src}, 3)
	f, ok := findFinding(r, "four")
	if !ok {
		t.Fatal("four should be flagged")
	}
	if f.Severity != "low" {
		t.Errorf("4-return func: want low severity, got %q", f.Severity)
	}
}

// TestScan_SeverityMedium: 6+ returns → medium severity。
func TestScan_SeverityMedium(t *testing.T) {
	src := `package p
func six() (string, int, bool, float64, error, int) { return "", 0, false, 0, nil, 0 }
`
	r := Scan(map[string]string{"x.go": src}, 3)
	f, ok := findFinding(r, "six")
	if !ok {
		t.Fatal("six should be flagged")
	}
	if f.Severity != "medium" {
		t.Errorf("6-return func: want medium severity, got %q", f.Severity)
	}
}

// TestScan_DefaultThresholdWhenZero: threshold<=0 は defaultThreshold を使用。
func TestScan_DefaultThresholdWhenZero(t *testing.T) {
	r := Scan(map[string]string{"x.go": "package p\n"}, 0)
	if r.Threshold != defaultThreshold {
		t.Errorf("threshold<=0 should default to %d, got %d", defaultThreshold, r.Threshold)
	}
}

// TestScan_TestFuncSkipped: TestXxx / BenchmarkXxx / ExampleXxx はスキップ。
func TestScan_TestFuncSkipped(t *testing.T) {
	src := `package p
import "testing"
func TestMany(t *testing.T) {}
func BenchmarkMany(b *testing.B) {}
func helperMany() (string, int, bool, error) { return "", 0, false, nil }
`
	r := Scan(map[string]string{"x.go": src}, 3)
	for _, f := range r.Findings {
		if f.Rule == "many-returns" {
			if f.Func == "TestMany" || f.Func == "BenchmarkMany" {
				t.Errorf("test func %q should be skipped", f.Func)
			}
		}
	}
	if !hasFinding(r, "helperMany") {
		t.Error("helperMany (4 returns) should be flagged")
	}
}

// TestScan_TestFileSkipped: _test.go ファイルはすべてスキップ。
func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
func helper() (string, int, bool, error) { return "", 0, false, nil }
`
	r := Scan(map[string]string{"foo_test.go": src}, 3)
	if r.TooManyReturns != 0 {
		t.Errorf("_test.go functions should be skipped, TooManyReturns=%d", r.TooManyReturns)
	}
}

// TestScan_FuncLitSkipped: 入れ子 FuncLit は FuncDecl のみ対象なのでスキップ。
func TestScan_FuncLitSkipped(t *testing.T) {
	src := `package p
func outer() {
	cb := func() (string, int, bool, error) { return "", 0, false, nil }
	_ = cb
}
`
	r := Scan(map[string]string{"x.go": src}, 3)
	if r.TooManyReturns != 0 {
		t.Errorf("FuncLit returns must not be flagged, TooManyReturns=%d", r.TooManyReturns)
	}
}

// TestScan_ParseError: 壊れた Go → parse-error finding。
func TestScan_ParseError(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("}, 3)
	found := false
	for _, f := range r.Findings {
		if f.Rule == "parse-error" {
			found = true
		}
	}
	if !found {
		t.Error("malformed source should produce a parse-error finding")
	}
}

// TestScan_NonGoSkipped: .go でないファイルは FilesScanned に含まない。
func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"readme.md": "func fake() (a, b, c, d string) {}"}, 3)
	if r.FilesScanned != 0 {
		t.Errorf("non-.go files must not count, FilesScanned=%d", r.FilesScanned)
	}
}

// TestScan_Deterministic: 同一入力 → 同一順序。
func TestScan_Deterministic(t *testing.T) {
	src := `package p
func zz() (string, int, bool, error) { return "", 0, false, nil }
func aa() (string, int, bool, error) { return "", 0, false, nil }
`
	r1 := Scan(map[string]string{"x.go": src}, 3)
	r2 := Scan(map[string]string{"x.go": src}, 3)
	if len(r1.Findings) != 2 || len(r2.Findings) != 2 {
		t.Fatalf("want 2 findings, got %d/%d", len(r1.Findings), len(r2.Findings))
	}
	// File→Line→Func ソート: 宣言順(zz が先)。
	if r1.Findings[0].Func != "zz" || r1.Findings[1].Func != "aa" {
		t.Errorf("order: want [zz aa], got [%s %s]", r1.Findings[0].Func, r1.Findings[1].Func)
	}
	if r2.Findings[0].Func != "zz" {
		t.Error("run 2 differs from run 1")
	}
}

// TestScan_MaxAndAvg: MaxReturns と AvgReturns の計算。
func TestScan_MaxAndAvg(t *testing.T) {
	src := `package p
func a() error { return nil }
func b() (string, int, bool) { return "", 0, false }
`
	r := Scan(map[string]string{"x.go": src}, 3)
	if r.MaxReturns != 3 {
		t.Errorf("MaxReturns: want 3, got %d", r.MaxReturns)
	}
	want := (1.0 + 3.0) / 2
	if r.AvgReturns != want {
		t.Errorf("AvgReturns: want %.1f, got %.1f", want, r.AvgReturns)
	}
}

// TestScan_FuncsScannedCount: FuncsScanned はスキップ対象外の関数数を返す。
func TestScan_FuncsScannedCount(t *testing.T) {
	src := `package p
func A() error { return nil }
func B() (string, int, bool, error) { return "", 0, false, nil }
`
	r := Scan(map[string]string{"x.go": src}, 3)
	if r.FuncsScanned != 2 {
		t.Errorf("FuncsScanned: want 2, got %d", r.FuncsScanned)
	}
}
