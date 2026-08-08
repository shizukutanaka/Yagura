package complexity_test

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/complexity"
)

const simpleSrc = `package foo

func Add(a, b int) int {
	return a + b
}
`

const oneIfSrc = `package foo

func Check(x int) bool {
	if x > 0 {
		return true
	}
	return false
}
`

const andSrc = `package foo

func Both(a, b bool) bool {
	if a && b {
		return true
	}
	return false
}
`

const switchSrc = `package foo

func Classify(n int) string {
	switch n {
	case 1:
		return "one"
	case 2:
		return "two"
	default:
		return "many"
	}
}
`

const funcLitSrc = `package foo

func Outer() {
	fn := func(x int) int {
		if x > 0 {
			return x
		}
		return -x
	}
	_ = fn
}
`

const methodSrc = `package foo

type T struct{}

func (t *T) Method(x int) int {
	if x > 0 {
		return 1
	}
	return 0
}
`

func find(r complexity.Report, name string) (complexity.FuncComplexity, bool) {
	for _, f := range r.Functions {
		if f.Func == name {
			return f, true
		}
	}
	return complexity.FuncComplexity{}, false
}

func TestScan_SimpleFunc_Complexity1(t *testing.T) {
	r := complexity.Scan(map[string]string{"a.go": simpleSrc}, 10)
	f, ok := find(r, "Add")
	if !ok {
		t.Fatal("Add not found")
	}
	if f.Complexity != 1 {
		t.Errorf("Add complexity: want 1 got %d", f.Complexity)
	}
}

func TestScan_OneIf_Complexity2(t *testing.T) {
	r := complexity.Scan(map[string]string{"a.go": oneIfSrc}, 10)
	f, _ := find(r, "Check")
	if f.Complexity != 2 {
		t.Errorf("Check complexity: want 2 got %d", f.Complexity)
	}
}

func TestScan_AndOperator_Complexity3(t *testing.T) {
	// base 1 + if 1 + && 1 = 3
	r := complexity.Scan(map[string]string{"a.go": andSrc}, 10)
	f, _ := find(r, "Both")
	if f.Complexity != 3 {
		t.Errorf("Both complexity: want 3 got %d", f.Complexity)
	}
}

func TestScan_Switch_CountsEachCase(t *testing.T) {
	// base 1 + 3 case clauses (incl default) = 4
	r := complexity.Scan(map[string]string{"a.go": switchSrc}, 10)
	f, _ := find(r, "Classify")
	if f.Complexity != 4 {
		t.Errorf("Classify complexity: want 4 got %d", f.Complexity)
	}
}

func TestScan_FuncLit_CountedSeparately(t *testing.T) {
	r := complexity.Scan(map[string]string{"a.go": funcLitSrc}, 10)
	// Outer itself has no branches (the if is inside the closure) → complexity 1
	outer, _ := find(r, "Outer")
	if outer.Complexity != 1 {
		t.Errorf("Outer complexity: want 1 (branch is in the closure) got %d", outer.Complexity)
	}
	// the closure should appear as its own entry with complexity 2
	var litFound bool
	for _, f := range r.Functions {
		if f.Func != "Outer" && f.Complexity == 2 {
			litFound = true
		}
	}
	if !litFound {
		t.Error("closure should be a separate function entry with complexity 2")
	}
}

func TestScan_MethodNameIncludesReceiver(t *testing.T) {
	r := complexity.Scan(map[string]string{"a.go": methodSrc}, 10)
	if _, ok := find(r, "(*T).Method"); !ok {
		var names []string
		for _, f := range r.Functions {
			names = append(names, f.Func)
		}
		t.Errorf("method should be named (*T).Method; got %v", names)
	}
}

func TestScan_OverThresholdFindings(t *testing.T) {
	// a function with 12 ifs → complexity 13, over threshold 10
	src := `package foo

func Big(x int) int {
	r := 0
	if x == 1 { r++ }
	if x == 2 { r++ }
	if x == 3 { r++ }
	if x == 4 { r++ }
	if x == 5 { r++ }
	if x == 6 { r++ }
	if x == 7 { r++ }
	if x == 8 { r++ }
	if x == 9 { r++ }
	if x == 10 { r++ }
	if x == 11 { r++ }
	if x == 12 { r++ }
	return r
}
`
	r := complexity.Scan(map[string]string{"a.go": src}, 10)
	f, _ := find(r, "Big")
	if f.Complexity != 13 {
		t.Fatalf("Big complexity: want 13 got %d", f.Complexity)
	}
	if r.OverThreshold != 1 {
		t.Errorf("OverThreshold: want 1 got %d", r.OverThreshold)
	}
	if len(r.Findings) != 1 {
		t.Fatalf("Findings: want 1 got %d", len(r.Findings))
	}
	if r.Findings[0].Func != "Big" {
		t.Errorf("finding should name Big, got %q", r.Findings[0].Func)
	}
	if r.MaxComplexity != 13 {
		t.Errorf("MaxComplexity: want 13 got %d", r.MaxComplexity)
	}
}

func TestScan_DefaultThreshold(t *testing.T) {
	// threshold <= 0 must default to 10
	r := complexity.Scan(map[string]string{"a.go": oneIfSrc}, 0)
	if r.Threshold != 10 {
		t.Errorf("default threshold: want 10 got %d", r.Threshold)
	}
}

func TestScan_Empty(t *testing.T) {
	r := complexity.Scan(map[string]string{}, 10)
	if r.FilesScanned != 0 {
		t.Errorf("FilesScanned: want 0 got %d", r.FilesScanned)
	}
	if r.MaxComplexity != 0 || r.AvgComplexity != 0 {
		t.Errorf("empty: max=%d avg=%f", r.MaxComplexity, r.AvgComplexity)
	}
}

func TestScan_ParseErrorSurfaced(t *testing.T) {
	r := complexity.Scan(map[string]string{"broken.go": "package foo\nfunc {{{"}, 10)
	found := false
	for _, f := range r.Findings {
		if f.Rule == "parse-error" {
			found = true
		}
	}
	if !found {
		t.Error("parse error must be surfaced, not silently dropped")
	}
}

func TestScan_Deterministic(t *testing.T) {
	files := map[string]string{
		"z.go": switchSrc,
		"a.go": methodSrc,
		"m.go": andSrc,
	}
	first := complexity.Scan(files, 10)
	for i := 0; i < 20; i++ {
		got := complexity.Scan(files, 10)
		if len(got.Functions) != len(first.Functions) {
			t.Fatalf("run %d: func count drift", i)
		}
		for j := range got.Functions {
			if got.Functions[j] != first.Functions[j] {
				t.Fatalf("run %d: func[%d] mismatch %+v vs %+v", i, j, got.Functions[j], first.Functions[j])
			}
		}
	}
}

func TestScan_SeverityBands(t *testing.T) {
	// complexity 21+ → high; 11-20 → medium
	mk := func(ifs int) string {
		s := "package foo\n\nfunc F(x int) int {\n\tr := 0\n"
		for i := 0; i < ifs; i++ {
			s += "\tif x > 0 { r++ }\n"
		}
		return s + "\treturn r\n}\n"
	}
	// 25 ifs → complexity 26 → high
	r := complexity.Scan(map[string]string{"a.go": mk(25)}, 10)
	if r.Findings[0].Severity != "high" {
		t.Errorf("complexity 26 should be high, got %q", r.Findings[0].Severity)
	}
	// 12 ifs → complexity 13 → medium
	r2 := complexity.Scan(map[string]string{"b.go": mk(12)}, 10)
	if r2.Findings[0].Severity != "medium" {
		t.Errorf("complexity 13 should be medium, got %q", r2.Findings[0].Severity)
	}
}
