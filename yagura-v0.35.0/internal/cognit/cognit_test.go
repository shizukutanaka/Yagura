package cognit

import "testing"

// cognitOf は単一ソースを threshold=1 で走査し、関数名→cognit 値の map を返す。
// Functions リストは threshold に関係なく全関数を載せるので、正確な値を検証できる。
func cognitOf(t *testing.T, body string) map[string]int {
	t.Helper()
	r := Scan(map[string]string{"x.go": "package p\n" + body}, 1)
	m := map[string]int{}
	for _, f := range r.Functions {
		m[f.Func] = f.Cognit
	}
	return m
}

func TestScan_FlatFunctionIsZero(t *testing.T) {
	got := cognitOf(t, "func f(a bool) { _ = a }")
	if got["f"] != 0 {
		t.Fatalf("flat func: want 0, got %d", got["f"])
	}
}

func TestScan_SingleIfIsOne(t *testing.T) {
	got := cognitOf(t, "func f(a bool) { if a { _ = a } }")
	if got["f"] != 1 {
		t.Fatalf("single if: want 1, got %d", got["f"])
	}
}

func TestScan_NestedIfAddsNestingIncrement(t *testing.T) {
	// if(+1, n0) → inner if(+1 base +1 nesting) = 3
	got := cognitOf(t, "func f(a, b bool) { if a { if b { _ = b } } }")
	if got["f"] != 3 {
		t.Fatalf("nested if: want 3, got %d", got["f"])
	}
}

func TestScan_ElseAddsOne(t *testing.T) {
	got := cognitOf(t, "func f(a bool) { if a {} else { _ = a } }")
	if got["f"] != 2 {
		t.Fatalf("if-else: want 2, got %d", got["f"])
	}
}

func TestScan_ElseIfIsStructuralOnly(t *testing.T) {
	// else if does NOT get a nesting increment — only +1 structural
	got := cognitOf(t, "func f(a, b bool) { if a {} else if b {} }")
	if got["f"] != 2 {
		t.Fatalf("if-elseif: want 2, got %d", got["f"])
	}
}

func TestScan_ElseIfElseChain(t *testing.T) {
	got := cognitOf(t, "func f(a, b bool) { if a {} else if b {} else {} }")
	if got["f"] != 3 {
		t.Fatalf("if-elseif-else: want 3, got %d", got["f"])
	}
}

func TestScan_SwitchIsOneRegardlessOfCases(t *testing.T) {
	// the key divergence from McCabe: switch = +1, not +1 per case
	got := cognitOf(t, "func f(n int) { switch n { case 1: case 2: case 3: } }")
	if got["f"] != 1 {
		t.Fatalf("switch 3 cases: want 1, got %d", got["f"])
	}
}

func TestScan_NestedForAddsNesting(t *testing.T) {
	// for(+1 n0) → inner for(+1 +1 nesting) = 3
	got := cognitOf(t, "func f(xs []int) { for range xs { for range xs {} } }")
	if got["f"] != 3 {
		t.Fatalf("nested for: want 3, got %d", got["f"])
	}
}

func TestScan_LogicalAndSequenceIsOne(t *testing.T) {
	// if(+1) + (&& sequence +1) = 2
	got := cognitOf(t, "func f(a, b bool) { if a && b {} }")
	if got["f"] != 2 {
		t.Fatalf("if a && b: want 2, got %d", got["f"])
	}
}

func TestScan_LogicalAndChainStaysOneSequence(t *testing.T) {
	got := cognitOf(t, "func f(a, b, c bool) { if a && b && c {} }")
	if got["f"] != 2 {
		t.Fatalf("if a && b && c: want 2, got %d", got["f"])
	}
}

func TestScan_LogicalMixedOperatorsCountSeparately(t *testing.T) {
	// if(+1) + (&& seq +1) + (|| seq +1) = 3
	got := cognitOf(t, "func f(a, b, c bool) { if a && b || c {} }")
	if got["f"] != 3 {
		t.Fatalf("if a && b || c: want 3, got %d", got["f"])
	}
}

func TestScan_LabeledBreakAddsOne(t *testing.T) {
	src := "func f(xs []int) {\nLoop:\n\tfor range xs {\n\t\tfor range xs {\n\t\t\tbreak Loop\n\t\t}\n\t}\n}"
	// outer for(+1 n0) + inner for(+1 +1 n1) + break Loop(+1) = 4
	got := cognitOf(t, src)
	if got["f"] != 4 {
		t.Fatalf("labeled break: want 4, got %d", got["f"])
	}
}

func TestScan_FuncLitIncrementsNesting(t *testing.T) {
	// funclit body at nesting 1 → if inside = +1 base +1 nesting = 2
	got := cognitOf(t, "func f() { g := func(a bool) { if a {} }; _ = g }")
	if got["f"] != 2 {
		t.Fatalf("funclit nesting: want 2, got %d", got["f"])
	}
}

func TestScan_FuncLitNotCountedAsSeparateFunction(t *testing.T) {
	// unlike McCabe complexity, cognit folds closures into the enclosing func
	got := cognitOf(t, "func f() { g := func(a bool) { if a {} }; _ = g }")
	if len(got) != 1 {
		t.Fatalf("want exactly 1 function (f), got %d: %v", len(got), got)
	}
	if _, ok := got["f"]; !ok {
		t.Fatalf("want function f present, got %v", got)
	}
}

func TestScan_DirectRecursionAddsOne(t *testing.T) {
	// if(+1) + recursion(+1) = 2
	src := "func f(n int) int { if n <= 0 { return 0 }; return f(n - 1) }"
	got := cognitOf(t, src)
	if got["f"] != 2 {
		t.Fatalf("recursion: want 2, got %d", got["f"])
	}
}

func TestScan_RecursionCountedOncePerFunction(t *testing.T) {
	// two recursive call sites still add only +1
	src := "func f(n int) int { if n <= 0 { return 0 }; return f(n-1) + f(n-2) }"
	got := cognitOf(t, src)
	// if(+1) + recursion(+1, once) + (the + is not logical) = 2
	if got["f"] != 2 {
		t.Fatalf("recursion twice: want 2, got %d", got["f"])
	}
}

func TestScan_TypeSwitchIsOne(t *testing.T) {
	got := cognitOf(t, "func f(x any) { switch x.(type) { case int: case string: } }")
	if got["f"] != 1 {
		t.Fatalf("type switch: want 1, got %d", got["f"])
	}
}

func TestScan_SelectIsOne(t *testing.T) {
	got := cognitOf(t, "func f(ch chan int) { select { case <-ch: } }")
	if got["f"] != 1 {
		t.Fatalf("select: want 1, got %d", got["f"])
	}
}

func TestScan_RangeIsOne(t *testing.T) {
	got := cognitOf(t, "func f(xs []int) { for range xs {} }")
	if got["f"] != 1 {
		t.Fatalf("range: want 1, got %d", got["f"])
	}
}

func TestScan_MethodNameUsesReceiverConvention(t *testing.T) {
	r := Scan(map[string]string{"x.go": "package p\ntype T struct{}\nfunc (t *T) M(a bool) { if a {} }"}, 1)
	found := false
	for _, f := range r.Functions {
		if f.Func == "(*T).M" {
			found = true
			if f.Cognit != 1 {
				t.Fatalf("(*T).M cognit: want 1, got %d", f.Cognit)
			}
		}
	}
	if !found {
		t.Fatalf("want function (*T).M, got %+v", r.Functions)
	}
}

func TestScan_OverThresholdEmitsFinding(t *testing.T) {
	// 3 nested ifs: if(1) + if(2) + if(3) = 6
	src := "package p\nfunc f(a, b, c bool) { if a { if b { if c {} } } }"
	r := Scan(map[string]string{"x.go": src}, 5)
	if r.OverThreshold != 1 {
		t.Fatalf("want 1 over threshold, got %d", r.OverThreshold)
	}
	if len(r.Findings) != 1 || r.Findings[0].Rule != "high-cognitive-complexity" {
		t.Fatalf("want 1 high-cognitive-complexity finding, got %+v", r.Findings)
	}
	if r.Findings[0].Cognit != 6 {
		t.Fatalf("want cognit 6 in finding, got %d", r.Findings[0].Cognit)
	}
}

func TestScan_UnderThresholdNoFinding(t *testing.T) {
	src := "package p\nfunc f(a bool) { if a {} }"
	r := Scan(map[string]string{"x.go": src}, 5)
	if len(r.Findings) != 0 {
		t.Fatalf("want no findings under threshold, got %+v", r.Findings)
	}
}

func TestScan_SeverityMediumBoundary(t *testing.T) {
	// 7 nested ifs: 1+2+3+4+5+6+7 = 28 → over 15 but <= 30 → medium
	src := "package p\nfunc f(a, b, c, d, e, g, h bool) { if a { if b { if c { if d { if e { if g { if h {} } } } } } } }"
	r := Scan(map[string]string{"x.go": src}, 15)
	if len(r.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", r.Findings)
	}
	if r.Findings[0].Cognit != 28 {
		t.Fatalf("want cognit 28, got %d", r.Findings[0].Cognit)
	}
	if r.Findings[0].Severity != "medium" {
		t.Fatalf("cognit 28: want medium, got %s", r.Findings[0].Severity)
	}
}

func TestScan_SeverityHighAboveThirty(t *testing.T) {
	// 8 nested ifs: 1+2+3+4+5+6+7+8 = 36 → over 30 → high
	src := "package p\nfunc f(a, b, c, d, e, g, h, i bool) { if a { if b { if c { if d { if e { if g { if h { if i {} } } } } } } } }"
	r := Scan(map[string]string{"x.go": src}, 15)
	if len(r.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", r.Findings)
	}
	if r.Findings[0].Cognit != 36 {
		t.Fatalf("want cognit 36, got %d", r.Findings[0].Cognit)
	}
	if r.Findings[0].Severity != "high" {
		t.Fatalf("cognit 36: want high, got %s", r.Findings[0].Severity)
	}
}

func TestScan_TestFileSkipped(t *testing.T) {
	r := Scan(map[string]string{"x_test.go": "package p\nfunc f(a bool) { if a {} }"}, 1)
	if len(r.Functions) != 0 {
		t.Fatalf("want _test.go skipped, got %+v", r.Functions)
	}
}

func TestScan_TestFunctionsSkipped(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc TestX(t *testing.T) { if true {} }\nfunc BenchmarkY(b *testing.B) { if true {} }"
	r := Scan(map[string]string{"x.go": src}, 1)
	for _, f := range r.Functions {
		if f.Func == "TestX" || f.Func == "BenchmarkY" {
			t.Fatalf("test/benchmark funcs should be skipped, got %s", f.Func)
		}
	}
}

func TestScan_ParseErrorEmitsFinding(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("}, 1)
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

func TestScan_DeterministicSort(t *testing.T) {
	src := "package p\nfunc b(x bool) { if x { if x {} } }\nfunc a(x bool) { if x { if x {} } }"
	r1 := Scan(map[string]string{"z.go": src}, 1)
	r2 := Scan(map[string]string{"z.go": src}, 1)
	if len(r1.Findings) != len(r2.Findings) {
		t.Fatalf("nondeterministic finding count")
	}
	for i := range r1.Findings {
		if r1.Findings[i] != r2.Findings[i] {
			t.Fatalf("nondeterministic order at %d: %+v vs %+v", i, r1.Findings[i], r2.Findings[i])
		}
	}
	// a should sort before b by func name at same file/line ordering
	if len(r1.Findings) >= 2 && r1.Findings[0].Func > r1.Findings[1].Func && r1.Findings[0].Line == r1.Findings[1].Line {
		t.Fatalf("findings not sorted by func: %+v", r1.Findings)
	}
}

func TestScan_NonGoFileIgnored(t *testing.T) {
	r := Scan(map[string]string{"readme.md": "if this were go it would parse"}, 1)
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Fatalf("non-go file should be ignored, got scanned=%d findings=%+v", r.FilesScanned, r.Findings)
	}
}
