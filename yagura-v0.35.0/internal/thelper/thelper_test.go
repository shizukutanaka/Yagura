package thelper

import "testing"

func scan(t *testing.T, files map[string]string) Report {
	t.Helper()
	return Scan(files)
}

func ruleNames(r Report) map[string]bool {
	m := map[string]bool{}
	for _, f := range r.Findings {
		if f.Rule == "missing-t-helper" {
			m[f.Func] = true
		}
	}
	return m
}

func TestScan_FlagsHelperWithoutHelperCall(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc assertEqual(t *testing.T, a, b int) {\n\tif a != b {\n\t\tt.Errorf(\"ne\")\n\t}\n}"
	got := ruleNames(scan(t, map[string]string{"x_test.go": src}))
	if !got["assertEqual"] {
		t.Fatalf("want assertEqual flagged, got %v", got)
	}
}

func TestScan_NoFlagWhenHelperCalled(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc assertEqual(t *testing.T, a, b int) {\n\tt.Helper()\n\tif a != b {\n\t\tt.Errorf(\"ne\")\n\t}\n}"
	got := ruleNames(scan(t, map[string]string{"x_test.go": src}))
	if got["assertEqual"] {
		t.Fatalf("helper with t.Helper() must NOT be flagged, got %v", got)
	}
}

func TestScan_NoFlagForTestEntryPoint(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc TestFoo(t *testing.T) {\n\tif 1 != 1 {\n\t\tt.Errorf(\"ne\")\n\t}\n}"
	got := ruleNames(scan(t, map[string]string{"x_test.go": src}))
	if got["TestFoo"] {
		t.Fatalf("TestXxx entry point must NOT be flagged, got %v", got)
	}
}

func TestScan_NoFlagForBenchmarkEntryPoint(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc BenchmarkFoo(b *testing.B) {\n\tfor i := 0; i < b.N; i++ {\n\t}\n}"
	got := ruleNames(scan(t, map[string]string{"x_test.go": src}))
	if got["BenchmarkFoo"] {
		t.Fatalf("BenchmarkXxx must NOT be flagged, got %v", got)
	}
}

func TestScan_NoFlagForFuzzEntryPoint(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc FuzzFoo(f *testing.F) {\n\tf.Add(1)\n}"
	got := ruleNames(scan(t, map[string]string{"x_test.go": src}))
	if got["FuzzFoo"] {
		t.Fatalf("FuzzXxx must NOT be flagged, got %v", got)
	}
}

func TestScan_FlagsBenchmarkHelper(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc setupBench(b *testing.B, n int) {\n\t_ = n\n\tb.ResetTimer()\n}"
	got := ruleNames(scan(t, map[string]string{"x_test.go": src}))
	if !got["setupBench"] {
		t.Fatalf("want setupBench (B helper) flagged, got %v", got)
	}
}

func TestScan_NoFlagForTBHelperWithHelperCall(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc mustWrite(tb *testing.TB, s string) {\n\t(*tb).Helper()\n\t_ = s\n}"
	// (*tb).Helper() is an unusual form; the common case is tb.Helper()
	src2 := "package p\nimport \"testing\"\nfunc mustWrite(tb testing.TB, s string) {\n\ttb.Helper()\n\t_ = s\n}"
	_ = src
	got := ruleNames(scan(t, map[string]string{"x_test.go": src2}))
	if got["mustWrite"] {
		t.Fatalf("TB helper with tb.Helper() must NOT be flagged, got %v", got)
	}
}

func TestScan_FlagsTBHelperWithoutHelperCall(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc mustWrite(tb testing.TB, s string) {\n\t_ = s\n}"
	got := ruleNames(scan(t, map[string]string{"x_test.go": src}))
	if !got["mustWrite"] {
		t.Fatalf("want mustWrite (TB helper) flagged, got %v", got)
	}
}

func TestScan_NoFlagForNonTestingFunction(t *testing.T) {
	src := "package p\nfunc add(a, b int) int {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn b\n}"
	got := ruleNames(scan(t, map[string]string{"x_test.go": src}))
	if got["add"] {
		t.Fatalf("non-testing func must NOT be flagged, got %v", got)
	}
	if len(got) != 0 {
		t.Fatalf("want no findings, got %v", got)
	}
}

func TestScan_MethodHelperNameConvention(t *testing.T) {
	src := "package p\nimport \"testing\"\ntype suite struct{}\nfunc (s *suite) assertX(t *testing.T) {\n\tt.Fail()\n}"
	r := scan(t, map[string]string{"x_test.go": src})
	found := false
	for _, f := range r.Findings {
		if f.Func == "(*suite).assertX" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want finding on (*suite).assertX, got %+v", r.Findings)
	}
}

func TestScan_HelperCallNotFirstStillCounts(t *testing.T) {
	// conservative: we only flag ABSENCE of t.Helper(); presence anywhere is OK
	src := "package p\nimport \"testing\"\nfunc h(t *testing.T, ok bool) {\n\tif !ok {\n\t\tt.Helper()\n\t\tt.Fatal(\"bad\")\n\t}\n}"
	got := ruleNames(scan(t, map[string]string{"x_test.go": src}))
	if got["h"] {
		t.Fatalf("helper with t.Helper() present (not first) must NOT be flagged, got %v", got)
	}
}

func TestScan_HelperParamNamedBlankNotFlagged(t *testing.T) {
	// can't call Helper() on _, so don't flag (avoid noise)
	src := "package p\nimport \"testing\"\nfunc h(_ *testing.T, n int) {\n\t_ = n\n}"
	got := ruleNames(scan(t, map[string]string{"x_test.go": src}))
	if got["h"] {
		t.Fatalf("helper with blank testing param must NOT be flagged, got %v", got)
	}
}

func TestScan_TestMainExcluded(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc TestMain(m *testing.M) {\n\tm.Run()\n}"
	got := ruleNames(scan(t, map[string]string{"x_test.go": src}))
	if got["TestMain"] {
		t.Fatalf("TestMain must NOT be flagged, got %v", got)
	}
}

func TestScan_ParseErrorEmitsFinding(t *testing.T) {
	r := Scan(map[string]string{"bad_test.go": "package p\nfunc ("})
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

func TestScan_FlaggedCount(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc a(t *testing.T) { t.Fail() }\nfunc b(t *testing.T) { t.Helper(); t.Fail() }"
	r := scan(t, map[string]string{"x_test.go": src})
	if r.Flagged != 1 {
		t.Fatalf("want Flagged 1 (only a), got %d", r.Flagged)
	}
}

func TestScan_NonGoFileIgnored(t *testing.T) {
	r := Scan(map[string]string{"readme.md": "func h(t *testing.T) {}"})
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Fatalf("non-go file ignored, got scanned=%d findings=%+v", r.FilesScanned, r.Findings)
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := "package p\nimport \"testing\"\nfunc bbb(t *testing.T) { t.Fail() }\nfunc aaa(t *testing.T) { t.Fail() }"
	r1 := Scan(map[string]string{"z_test.go": src})
	r2 := Scan(map[string]string{"z_test.go": src})
	if len(r1.Findings) != len(r2.Findings) {
		t.Fatalf("nondeterministic count")
	}
	for i := range r1.Findings {
		if r1.Findings[i] != r2.Findings[i] {
			t.Fatalf("nondeterministic order at %d", i)
		}
	}
}
