package predeclared

import "testing"

func hasFinding(r Report, name, kind string) bool {
	for _, f := range r.Findings {
		if f.Name == name && f.Kind == kind {
			return true
		}
	}
	return false
}

// ─── variable / short-decl ───────────────────────────────

func TestScan_ShortDeclShadowingLenFlagged(t *testing.T) {
	src := `package p
func F() int {
	len := 5
	return len
}
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if !hasFinding(r, "len", "variable") {
		t.Errorf("len := should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_VarDeclShadowingErrorFlagged(t *testing.T) {
	src := `package p
var error string
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if !hasFinding(r, "error", "variable") {
		t.Errorf("var error should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_ConstShadowingNilFlagged(t *testing.T) {
	src := `package p
const nil = 0
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if !hasFinding(r, "nil", "constant") {
		t.Errorf("const nil should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_NonShadowingClean(t *testing.T) {
	src := `package p
func F() int {
	count := 5
	return count
}
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if len(r.Findings) != 0 {
		t.Errorf("normal var should be clean, got: %+v", r.Findings)
	}
}

func TestScan_BlankIdentifierClean(t *testing.T) {
	src := `package p
func F() {
	_, _ = doThing()
}
func doThing() (int, error) { return 0, nil }
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if len(r.Findings) != 0 {
		t.Errorf("blank identifier should be skipped, got: %+v", r.Findings)
	}
}

// ─── parameters & results ────────────────────────────────

func TestScan_ParamShadowingCopyFlagged(t *testing.T) {
	src := `package p
func F(copy int) int { return copy }
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if !hasFinding(r, "copy", "parameter") {
		t.Errorf("param named copy should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_NamedResultShadowingNewFlagged(t *testing.T) {
	src := `package p
func F() (new int) { return }
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if !hasFinding(r, "new", "result") {
		t.Errorf("named result new should be flagged, got: %+v", r.Findings)
	}
}

// ─── range loop key/value ────────────────────────────────

func TestScan_RangeKeyShadowingLenFlagged(t *testing.T) {
	src := `package p
func F() {
	for len, v := range []int{1, 2, 3} {
		_ = len
		_ = v
	}
}
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if !hasFinding(r, "len", "variable") {
		t.Errorf("range key len should be flagged, got: %+v", r.Findings)
	}
}

// ─── function & type decls ───────────────────────────────

func TestScan_TopLevelFuncShadowingFlagged(t *testing.T) {
	src := `package p
func len() int { return 0 }
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if !hasFinding(r, "len", "function") {
		t.Errorf("top-level func len should be flagged, got: %+v", r.Findings)
	}
}

// Method named like a predeclared is NOT flagged: methods are namespaced by receiver.
func TestScan_MethodNameNotFlagged(t *testing.T) {
	src := `package p
type T struct{}
func (t T) len() int { return 0 }
`
	r := Scan(map[string]string{"x.go": src}, nil)
	for _, f := range r.Findings {
		if f.Name == "len" && f.Kind == "function" {
			t.Errorf("method should NOT be flagged, got: %+v", f)
		}
	}
}

func TestScan_TypeShadowingErrorFlagged(t *testing.T) {
	src := `package p
type error struct{}
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if !hasFinding(r, "error", "type") {
		t.Errorf("type error should be flagged, got: %+v", r.Findings)
	}
}

// ─── ignore list ─────────────────────────────────────────

func TestScan_IgnoreListSuppresses(t *testing.T) {
	src := `package p
func F() {
	cap := 100
	_ = cap
}
`
	r := Scan(map[string]string{"x.go": src}, []string{"cap"})
	if len(r.Findings) != 0 {
		t.Errorf("ignored 'cap' should not be flagged, got: %+v", r.Findings)
	}
}

// ─── Go 1.21+ builtins ───────────────────────────────────

func TestScan_MinMaxClearShadowingFlagged(t *testing.T) {
	src := `package p
func F() {
	min := 1
	max := 10
	clear := false
	_, _, _ = min, max, clear
}
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if !hasFinding(r, "min", "variable") || !hasFinding(r, "max", "variable") || !hasFinding(r, "clear", "variable") {
		t.Errorf("Go 1.21+ builtins min/max/clear should be flagged, got: %+v", r.Findings)
	}
}

// ─── contract: skips & robustness ────────────────────────

func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
func F() {
	len := 5
	_ = len
}
`
	r := Scan(map[string]string{"x_test.go": src}, nil)
	if len(r.Findings) != 0 {
		t.Errorf("_test.go must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"README.md": "len := 5"}, nil)
	if r.FilesScanned != 0 {
		t.Errorf("non-go must not be scanned, FilesScanned=%d", r.FilesScanned)
	}
}

func TestScan_ParseErrorNoCrash(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("}, nil)
	found := false
	for _, f := range r.Findings {
		if f.Rule == "parse-error" {
			found = true
		}
	}
	if !found {
		t.Error("broken source should yield a parse-error finding, not a crash")
	}
}

func TestScan_EmptyInput(t *testing.T) {
	r := Scan(map[string]string{}, nil)
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Errorf("empty input should be empty report, got %+v", r)
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := `package p
func F() {
	len := 1
	new := 2
	_, _ = len, new
}
`
	a := Scan(map[string]string{"x.go": src}, nil)
	b := Scan(map[string]string{"x.go": src}, nil)
	if len(a.Findings) != len(b.Findings) {
		t.Fatalf("non-deterministic count: %d vs %d", len(a.Findings), len(b.Findings))
	}
	for i := range a.Findings {
		if a.Findings[i] != b.Findings[i] {
			t.Errorf("finding %d differs: %+v vs %+v", i, a.Findings[i], b.Findings[i])
		}
	}
}

func TestScan_SummaryCounts(t *testing.T) {
	src := `package p
func F(copy int) int {
	len := 5
	return copy + len
}
`
	r := Scan(map[string]string{"x.go": src}, nil)
	if r.Flagged != 2 {
		t.Errorf("Flagged: want 2, got %d", r.Flagged)
	}
}
