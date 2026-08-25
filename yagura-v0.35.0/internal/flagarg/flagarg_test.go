package flagarg

import (
	"strings"
	"testing"
)

// hasFinding は Report から rule=="flag-arg" のFindingをfunc名で探す。
func hasFinding(r Report, funcName string) bool {
	for _, f := range r.Findings {
		if f.Func == funcName && f.Rule == "flag-arg" {
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

// TestScan_BoolParamFlagged: bool パラメータを持つ関数は flagged。
func TestScan_BoolParamFlagged(t *testing.T) {
	src := `package p
func process(name string, verbose bool) {}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	if !hasFinding(r, "process") {
		t.Error("process(name, verbose bool) should be flagged")
	}
	if r.FlagsFound != 1 {
		t.Errorf("FlagsFound: want 1, got %d", r.FlagsFound)
	}
}

// TestScan_NoBoolParam: bool パラメータなし → finding なし。
func TestScan_NoBoolParam(t *testing.T) {
	src := `package p
func greet(name string) {}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	if r.FlagsFound != 0 {
		t.Errorf("no bool param should yield 0 findings, got %d", r.FlagsFound)
	}
}

// TestScan_MultipleBoolParamsMediumSeverity: 2+ bool → medium severity。
func TestScan_MultipleBoolParamsMediumSeverity(t *testing.T) {
	src := `package p
func load(path string, dryRun bool, verbose bool) {}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	f, ok := findFinding(r, "load")
	if !ok {
		t.Fatal("load should be flagged")
	}
	if f.BoolCount != 2 {
		t.Errorf("BoolCount: want 2, got %d", f.BoolCount)
	}
	if f.Severity != "medium" {
		t.Errorf("severity: want medium, got %q", f.Severity)
	}
}

// TestScan_OneBoolLowSeverity: 1 bool → low severity。
func TestScan_OneBoolLowSeverity(t *testing.T) {
	src := `package p
func save(path string, overwrite bool) {}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	f, ok := findFinding(r, "save")
	if !ok {
		t.Fatal("save should be flagged")
	}
	if f.Severity != "low" {
		t.Errorf("severity: want low, got %q", f.Severity)
	}
}

// TestScan_BoolParamNamesReported: Finding.BoolParams に bool 引数名が入る。
func TestScan_BoolParamNamesReported(t *testing.T) {
	src := `package p
func run(slug string, dryRun bool) {}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	f, ok := findFinding(r, "run")
	if !ok {
		t.Fatal("run should be flagged")
	}
	if len(f.BoolParams) != 1 || f.BoolParams[0] != "dryRun" {
		t.Errorf("BoolParams: want [dryRun], got %v", f.BoolParams)
	}
}

// TestScan_GroupedBoolParams: `a, b bool` は 2 個として計上。
func TestScan_GroupedBoolParams(t *testing.T) {
	src := `package p
func multi(x string, a, b bool) {}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	f, ok := findFinding(r, "multi")
	if !ok {
		t.Fatal("multi should be flagged")
	}
	if f.BoolCount != 2 {
		t.Errorf("BoolCount: want 2, got %d", f.BoolCount)
	}
	if len(f.BoolParams) != 2 {
		t.Errorf("BoolParams: want [a b], got %v", f.BoolParams)
	}
}

// TestScan_ThresholdRespected: min-bools=2 → 1-bool 関数は flagged しない。
func TestScan_ThresholdRespected(t *testing.T) {
	src := `package p
func one(name string, verbose bool) {}
func two(name string, dryRun bool, verbose bool) {}
`
	r := Scan(map[string]string{"x.go": src}, 2)
	if hasFinding(r, "one") {
		t.Error("one bool at threshold=2 should NOT be flagged")
	}
	if !hasFinding(r, "two") {
		t.Error("two bools at threshold=2 SHOULD be flagged")
	}
}

// TestScan_DefaultThresholdWhenZero: threshold<=0 → defaultThreshold 使用。
func TestScan_DefaultThresholdWhenZero(t *testing.T) {
	r := Scan(map[string]string{"x.go": "package p\n"}, 0)
	if r.Threshold != defaultThreshold {
		t.Errorf("threshold<=0 should default to %d, got %d", defaultThreshold, r.Threshold)
	}
}

// TestScan_TestFuncSkipped: TestXxx / BenchmarkXxx / ExampleXxx は計上しない。
func TestScan_TestFuncSkipped(t *testing.T) {
	src := `package p
import "testing"
func TestFoo(t *testing.T) {}
func BenchmarkFoo(b *testing.B) {}
func ExampleFoo() {}
func TestHelper(name string, verbose bool) {}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	for _, f := range r.Findings {
		if strings.HasPrefix(f.Func, "Test") || strings.HasPrefix(f.Func, "Benchmark") || strings.HasPrefix(f.Func, "Example") {
			t.Errorf("test func %q should be skipped", f.Func)
		}
	}
}

// TestScan_TestFileSkipped: _test.go ファイルは全関数スキップ。
func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
func helper(name string, verbose bool) {}
`
	r := Scan(map[string]string{"foo_test.go": src}, 1)
	if r.FlagsFound != 0 {
		t.Errorf("_test.go functions should be skipped, FlagsFound=%d", r.FlagsFound)
	}
}

// TestScan_ReceiverExcluded: メソッドレシーバは bool カウントに含まない。
func TestScan_ReceiverExcluded(t *testing.T) {
	src := `package p
type T struct{}
func (t *T) Method(name string, enabled bool) {}
func (t *T) NoFlag(name string) {}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	if !hasFinding(r, "(*T).Method") {
		t.Error("(*T).Method with bool param should be flagged")
	}
	if hasFinding(r, "(*T).NoFlag") {
		t.Error("(*T).NoFlag without bool param should NOT be flagged")
	}
}

// TestScan_FuncLitSkipped: 入れ子 FuncLit は FuncDecl のみ対象なのでスキップ。
func TestScan_FuncLitSkipped(t *testing.T) {
	src := `package p
func outer() {
	cb := func(name string, verbose bool) {}
	_ = cb
}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	// outer 自体は bool param なし → flagged しない
	if r.FlagsFound != 0 {
		t.Errorf("FuncLit params must not be flagged, FlagsFound=%d", r.FlagsFound)
	}
}

// TestScan_ParseError: 壊れた Go ソース → parse-error finding を返す。
func TestScan_ParseError(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("}, 1)
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
	r := Scan(map[string]string{"readme.md": "func fake(x bool) {}"}, 1)
	if r.FilesScanned != 0 {
		t.Errorf("non-.go files must not count, FilesScanned=%d", r.FilesScanned)
	}
}

// TestScan_Deterministic: 同一入力 → 同一順序の出力。
func TestScan_Deterministic(t *testing.T) {
	src := `package p
func zz(path string, verbose bool) {}
func aa(name string, dryRun bool) {}
`
	r1 := Scan(map[string]string{"x.go": src}, 1)
	r2 := Scan(map[string]string{"x.go": src}, 1)
	if len(r1.Findings) != 2 || len(r2.Findings) != 2 {
		t.Fatalf("want 2 findings, got %d/%d", len(r1.Findings), len(r2.Findings))
	}
	// File→Line→Func ソート: 宣言順(zz が先、aa が後)。
	if r1.Findings[0].Func != "zz" || r1.Findings[1].Func != "aa" {
		t.Errorf("order: want [zz aa], got [%s %s]", r1.Findings[0].Func, r1.Findings[1].Func)
	}
	if r2.Findings[0].Func != "zz" {
		t.Error("run 2 differs from run 1")
	}
}

// TestScan_PtrBoolNotFlagged: *bool はポインタで bool 型ではない → flagged しない。
func TestScan_PtrBoolNotFlagged(t *testing.T) {
	src := `package p
func tri(v *bool, name string) {}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	if hasFinding(r, "tri") {
		t.Error("*bool param should NOT be flagged as boolean flag argument")
	}
}

// TestScan_FuncsScannedCount: FuncsScanned はスキップ対象外の関数数を数える。
func TestScan_FuncsScannedCount(t *testing.T) {
	src := `package p
func A(x string) {}
func B(y string, verbose bool) {}
`
	r := Scan(map[string]string{"x.go": src}, 1)
	if r.FuncsScanned != 2 {
		t.Errorf("FuncsScanned: want 2, got %d", r.FuncsScanned)
	}
}

// 引数が bool **1 つだけ** の関数は flag argument ではない(v1.3.1)。
//
// Fowler の "flag argument" smell は、**他の引数と並んだ bool が呼び出し側で
// 振る舞いを切り替える**ことを問題にする(`process(data, true)` の true が何か
// 分からない)。引数が bool 1 つしか無い関数は、その bool が **データそのもの** で
// あって modulate する対象が存在しない。`yesNo(true)` は関数名と合わせて曖昧さがない。
//
// 自リポジトリ実測: 17 件中 2 件(`yesNo(b bool) string` / `yesNoMark(ok bool) string`)
// がこの型の誤検出だった。
func TestScan_SingleBoolParamIsAConverterNotAFlag(t *testing.T) {
	files := map[string]string{
		"a.go": `package p

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
`,
	}
	rep := Scan(files, 1)
	if len(rep.Findings) != 0 {
		t.Errorf("a lone bool parameter is the function's subject, not a flag: %+v", rep.Findings)
	}
}

// 他の引数と並んだ bool は今も検出する(規則を弱めすぎていないこと)。
func TestScan_StillFlagsBoolAlongsideOtherParams(t *testing.T) {
	files := map[string]string{
		"a.go": `package p

func emit(data string, jsonOut bool) string {
	if jsonOut {
		return data
	}
	return data
}
`,
	}
	rep := Scan(files, 1)
	if len(rep.Findings) != 1 {
		t.Fatalf("a bool alongside another parameter is the real smell, got %d: %+v", len(rep.Findings), rep.Findings)
	}
}
