package prealloc

import "testing"

func findingsFor(t *testing.T, body string) []Finding {
	t.Helper()
	r := Scan(map[string]string{"x.go": "package p\n" + body})
	return r.Findings
}

func names(fs []Finding) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		if f.Rule == "prealloc-candidate" {
			m[f.Name] = true
		}
	}
	return m
}

func TestScan_FlagsVarDeclAppendInRange(t *testing.T) {
	src := "func f(xs []int) []int {\n\tvar out []int\n\tfor _, x := range xs {\n\t\tout = append(out, x)\n\t}\n\treturn out\n}"
	got := names(findingsFor(t, src))
	if !got["out"] {
		t.Fatalf("want 'out' flagged, got %v", got)
	}
}

func TestScan_FlagsEmptyCompositeLiteral(t *testing.T) {
	src := "func f(xs []int) []int {\n\tout := []int{}\n\tfor _, x := range xs {\n\t\tout = append(out, x)\n\t}\n\treturn out\n}"
	got := names(findingsFor(t, src))
	if !got["out"] {
		t.Fatalf("want 'out' flagged (empty composite), got %v", got)
	}
}

func TestScan_FlagsMakeZeroCap(t *testing.T) {
	src := "func f(xs []int) []int {\n\tout := make([]int, 0)\n\tfor _, x := range xs {\n\t\tout = append(out, x)\n\t}\n\treturn out\n}"
	got := names(findingsFor(t, src))
	if !got["out"] {
		t.Fatalf("want 'out' flagged (make 0 cap), got %v", got)
	}
}

func TestScan_NoFlagWhenPreallocated(t *testing.T) {
	// make([]int, 0, len(xs)) is the GOOD form — must not flag
	src := "func f(xs []int) []int {\n\tout := make([]int, 0, len(xs))\n\tfor _, x := range xs {\n\t\tout = append(out, x)\n\t}\n\treturn out\n}"
	got := names(findingsFor(t, src))
	if got["out"] {
		t.Fatalf("preallocated slice must NOT be flagged, got %v", got)
	}
}

func TestScan_NoFlagWhenMakeWithLength(t *testing.T) {
	// make([]int, len(xs)) preallocates length; index assignment, no append
	src := "func f(xs []int) []int {\n\tout := make([]int, len(xs))\n\tfor i, x := range xs {\n\t\tout[i] = x\n\t}\n\treturn out\n}"
	got := names(findingsFor(t, src))
	if got["out"] {
		t.Fatalf("length-preallocated slice must NOT be flagged, got %v", got)
	}
}

func TestScan_NoFlagForPlainForLoop(t *testing.T) {
	// conservative: only range loops (known length); plain for is not flagged
	src := "func f() []int {\n\tvar out []int\n\tfor i := 0; i < 10; i++ {\n\t\tout = append(out, i)\n\t}\n\treturn out\n}"
	got := names(findingsFor(t, src))
	if got["out"] {
		t.Fatalf("plain for loop must NOT be flagged (count not range-known), got %v", got)
	}
}

func TestScan_NoFlagForConditionalAppend(t *testing.T) {
	// append guarded by a conditional → count unknown → not flagged (FP guard)
	src := "func f(xs []int) []int {\n\tvar out []int\n\tfor _, x := range xs {\n\t\tif x > 0 {\n\t\t\tout = append(out, x)\n\t\t}\n\t}\n\treturn out\n}"
	got := names(findingsFor(t, src))
	if got["out"] {
		t.Fatalf("conditional append must NOT be flagged, got %v", got)
	}
}

func TestScan_NoFlagWhenNotAppendedInLoop(t *testing.T) {
	src := "func f(xs []int) []int {\n\tvar out []int\n\tfor _, x := range xs {\n\t\t_ = x\n\t}\n\treturn out\n}"
	got := names(findingsFor(t, src))
	if got["out"] {
		t.Fatalf("slice not appended in loop must NOT be flagged, got %v", got)
	}
}

func TestScan_FlagsRangeOverMap(t *testing.T) {
	// map length is known → preallocation valid
	src := "func f(m map[string]int) []string {\n\tvar out []string\n\tfor k := range m {\n\t\tout = append(out, k)\n\t}\n\treturn out\n}"
	got := names(findingsFor(t, src))
	if !got["out"] {
		t.Fatalf("range over map should flag, got %v", got)
	}
}

func TestScan_FlagsMultipleSlicesInSameLoop(t *testing.T) {
	src := "func f(xs []int) ([]int, []int) {\n\tvar a []int\n\tvar b []int\n\tfor _, x := range xs {\n\t\ta = append(a, x)\n\t\tb = append(b, x)\n\t}\n\treturn a, b\n}"
	got := names(findingsFor(t, src))
	if !got["a"] || !got["b"] {
		t.Fatalf("want both 'a' and 'b' flagged, got %v", got)
	}
}

func TestScan_FlaggedCountMatches(t *testing.T) {
	src := "package p\nfunc f(xs []int) []int {\n\tvar out []int\n\tfor _, x := range xs {\n\t\tout = append(out, x)\n\t}\n\treturn out\n}"
	r := Scan(map[string]string{"x.go": src})
	if r.Flagged != 1 {
		t.Fatalf("want Flagged 1, got %d", r.Flagged)
	}
}

func TestScan_MethodNameConvention(t *testing.T) {
	src := "package p\ntype T struct{}\nfunc (t *T) M(xs []int) []int {\n\tvar out []int\n\tfor _, x := range xs {\n\t\tout = append(out, x)\n\t}\n\treturn out\n}"
	r := Scan(map[string]string{"x.go": src})
	found := false
	for _, f := range r.Findings {
		if f.Func == "(*T).M" && f.Name == "out" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want finding on (*T).M for 'out', got %+v", r.Findings)
	}
}

func TestScan_ParseErrorEmitsFinding(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("})
	found := false
	for _, f := range r.Findings {
		if f.Rule == "parse-error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want parse-error finding, got %+v", r.Findings)
	}
}

func TestScan_TestFileSkipped(t *testing.T) {
	src := "package p\nfunc f(xs []int) []int {\n\tvar out []int\n\tfor _, x := range xs {\n\t\tout = append(out, x)\n\t}\n\treturn out\n}"
	r := Scan(map[string]string{"x_test.go": src})
	if len(r.Findings) != 0 {
		t.Fatalf("_test.go must be skipped, got %+v", r.Findings)
	}
}

func TestScan_TestFunctionsSkipped(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc TestX(t *testing.T) {\n\tvar out []int\n\tfor _, x := range []int{1} {\n\t\tout = append(out, x)\n\t}\n\t_ = out\n}"
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Fatalf("TestXxx must be skipped, got %+v", r.Findings)
	}
}

func TestScan_NonGoFileIgnored(t *testing.T) {
	r := Scan(map[string]string{"readme.md": "var out []int; for x := range xs { out = append(out, x) }"})
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Fatalf("non-go file must be ignored, got scanned=%d findings=%+v", r.FilesScanned, r.Findings)
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := "package p\nfunc f(xs []int) ([]int, []int) {\n\tvar b []int\n\tvar a []int\n\tfor _, x := range xs {\n\t\tb = append(b, x)\n\t\ta = append(a, x)\n\t}\n\treturn a, b\n}"
	r1 := Scan(map[string]string{"z.go": src})
	r2 := Scan(map[string]string{"z.go": src})
	if len(r1.Findings) != len(r2.Findings) {
		t.Fatalf("nondeterministic count")
	}
	for i := range r1.Findings {
		if r1.Findings[i] != r2.Findings[i] {
			t.Fatalf("nondeterministic order at %d", i)
		}
	}
}
