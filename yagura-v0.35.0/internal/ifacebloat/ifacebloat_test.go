package ifacebloat

import "testing"

func methodCounts(t *testing.T, threshold int, body string) map[string]int {
	t.Helper()
	r := Scan(map[string]string{"x.go": "package p\n" + body}, threshold)
	m := map[string]int{}
	for _, i := range r.Interfaces {
		m[i.Name] = i.Methods
	}
	return m
}

func flaggedNames(r Report) map[string]bool {
	m := map[string]bool{}
	for _, f := range r.Findings {
		if f.Rule == "interface-bloat" {
			m[f.Name] = true
		}
	}
	return m
}

func TestScan_SmallInterfaceNotFlagged(t *testing.T) {
	src := "type Reader interface {\n\tRead(p []byte) (int, error)\n\tClose() error\n}"
	r := Scan(map[string]string{"x.go": "package p\n" + src}, 10)
	if len(r.Findings) != 0 {
		t.Fatalf("small interface must not be flagged, got %+v", r.Findings)
	}
}

func TestScan_CountsMethods(t *testing.T) {
	src := "type I interface {\n\tA()\n\tB()\n\tC()\n}"
	got := methodCounts(t, 10, src)
	if got["I"] != 3 {
		t.Fatalf("want 3 methods, got %d", got["I"])
	}
}

func TestScan_BigInterfaceFlagged(t *testing.T) {
	src := "type Big interface {\n\tA()\n\tB()\n\tC()\n\tD()\n}"
	r := Scan(map[string]string{"x.go": "package p\n" + src}, 3)
	if !flaggedNames(r)["Big"] {
		t.Fatalf("interface over threshold must be flagged, got %+v", r.Findings)
	}
}

func TestScan_ExactlyThresholdNotFlagged(t *testing.T) {
	// 3 methods, threshold 3 → not flagged (only > threshold)
	src := "type I interface {\n\tA()\n\tB()\n\tC()\n}"
	r := Scan(map[string]string{"x.go": "package p\n" + src}, 3)
	if flaggedNames(r)["I"] {
		t.Fatalf("count == threshold must NOT be flagged, got %+v", r.Findings)
	}
}

func TestScan_EmptyInterfaceNotFlagged(t *testing.T) {
	src := "type Any interface{}"
	got := methodCounts(t, 10, src)
	if got["Any"] != 0 {
		t.Fatalf("empty interface should have 0 methods, got %d", got["Any"])
	}
}

func TestScan_EmbeddedInterfacesCounted(t *testing.T) {
	// each embedded interface counts as one element
	src := "import \"io\"\ntype RWC interface {\n\tio.Reader\n\tio.Writer\n\tio.Closer\n}"
	got := methodCounts(t, 10, src)
	if got["RWC"] != 3 {
		t.Fatalf("want 3 (embedded count as 1 each), got %d", got["RWC"])
	}
}

func TestScan_MixedMethodsAndEmbedded(t *testing.T) {
	src := "import \"io\"\ntype I interface {\n\tio.Reader\n\tExtra()\n\tMore()\n}"
	got := methodCounts(t, 10, src)
	if got["I"] != 3 {
		t.Fatalf("want 3 (1 embedded + 2 methods), got %d", got["I"])
	}
}

func TestScan_UnionConstraintCountsAsOne(t *testing.T) {
	// a type-union term is a single element, not one per term
	src := "type Num interface {\n\t~int | ~int64 | ~float64\n\tString() string\n}"
	got := methodCounts(t, 10, src)
	if got["Num"] != 2 {
		t.Fatalf("want 2 (1 union term + 1 method), got %d", got["Num"])
	}
}

func TestScan_SeverityMediumAndHigh(t *testing.T) {
	// threshold 3: 4 methods → medium (<=6); 7 methods → high (>2*3)
	med := "type M interface { A(); B(); C(); D() }"
	hi := "type H interface { A(); B(); C(); D(); E(); F(); G() }"
	r := Scan(map[string]string{"x.go": "package p\n" + med + "\n" + hi}, 3)
	sev := map[string]string{}
	for _, f := range r.Findings {
		sev[f.Name] = f.Severity
	}
	if sev["M"] != "medium" {
		t.Fatalf("M (4 methods, thr 3): want medium, got %s", sev["M"])
	}
	if sev["H"] != "high" {
		t.Fatalf("H (7 methods, thr 3): want high, got %s", sev["H"])
	}
}

func TestScan_DefaultThreshold(t *testing.T) {
	// threshold<=0 → default 10; 11 methods flagged, 10 not
	var b string
	for _, n := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K"} {
		b += "\t" + n + "()\n"
	}
	src := "type Eleven interface {\n" + b + "}"
	r := Scan(map[string]string{"x.go": "package p\n" + src}, 0)
	if r.Threshold != 10 {
		t.Fatalf("want default threshold 10, got %d", r.Threshold)
	}
	if !flaggedNames(r)["Eleven"] {
		t.Fatalf("11-method interface must be flagged at default threshold, got %+v", r.Findings)
	}
}

func TestScan_TestFileSkipped(t *testing.T) {
	src := "type Big interface { A(); B(); C(); D() }"
	r := Scan(map[string]string{"x_test.go": "package p\n" + src}, 3)
	if len(r.Findings) != 0 || len(r.Interfaces) != 0 {
		t.Fatalf("_test.go must be skipped, got %+v", r.Findings)
	}
}

func TestScan_ParseErrorEmitsFinding(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\ntype ("}, 10)
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

func TestScan_NonGoFileIgnored(t *testing.T) {
	r := Scan(map[string]string{"readme.md": "type I interface { A() }"}, 10)
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Fatalf("non-go file ignored, got scanned=%d findings=%+v", r.FilesScanned, r.Findings)
	}
}

func TestScan_MaxMethodsTracked(t *testing.T) {
	src := "type A interface { X() }\ntype B interface { X(); Y(); Z() }"
	r := Scan(map[string]string{"x.go": "package p\n" + src}, 10)
	if r.MaxMethods != 3 {
		t.Fatalf("want MaxMethods 3, got %d", r.MaxMethods)
	}
	if r.InterfacesScanned != 2 {
		t.Fatalf("want 2 interfaces scanned, got %d", r.InterfacesScanned)
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := "type Bbb interface { A(); B(); C(); D() }\ntype Aaa interface { A(); B(); C(); D() }"
	r1 := Scan(map[string]string{"z.go": "package p\n" + src}, 3)
	r2 := Scan(map[string]string{"z.go": "package p\n" + src}, 3)
	if len(r1.Findings) != len(r2.Findings) {
		t.Fatalf("nondeterministic count")
	}
	for i := range r1.Findings {
		if r1.Findings[i] != r2.Findings[i] {
			t.Fatalf("nondeterministic order at %d", i)
		}
	}
}
