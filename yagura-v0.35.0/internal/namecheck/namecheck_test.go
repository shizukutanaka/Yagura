package namecheck

import "testing"

func has(r Report, fn, rule string) bool {
	for _, f := range r.Findings {
		if f.Func == fn && f.Rule == rule {
			return true
		}
	}
	return false
}

// ─── predicate-not-bool ──────────────────────────────────

func TestScan_PredicateReturningIntFlagged(t *testing.T) {
	src := `package p
func isReady() int { return 1 }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "isReady", "predicate-not-bool") {
		t.Error("isReady returning int should be flagged predicate-not-bool")
	}
}

func TestScan_PredicateReturningBoolClean(t *testing.T) {
	src := `package p
func isReady() bool { return true }
func HasCritical() (bool, error) { return false, nil }
func canRun() bool { return true }
func shouldRetry() bool { return false }
func mustExist() bool { return true }
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("bool predicates should be clean, got %d findings: %+v", len(r.Findings), r.Findings)
	}
}

// Word-boundary: "Hash" must NOT be read as "has" predicate.
func TestScan_HashNotAPredicate(t *testing.T) {
	src := `package p
func Hash() string { return "" }
func Hashing() int { return 0 }
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("Hash/Hashing are not predicates, got: %+v", r.Findings)
	}
}

// First result bool is enough even with multiple returns.
func TestScan_PredicateBoolFirstMultiReturnClean(t *testing.T) {
	src := `package p
func isValid() (bool, string) { return true, "" }
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("first-return bool predicate should be clean, got: %+v", r.Findings)
	}
}

// Predicate with no return values at all → flagged (promises a bool, gives none).
func TestScan_PredicateNoReturnFlagged(t *testing.T) {
	src := `package p
func isReady() {}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "isReady", "predicate-not-bool") {
		t.Error("isReady with no return should be flagged")
	}
}

// ─── getter-no-return ────────────────────────────────────

func TestScan_GetterNoReturnFlagged(t *testing.T) {
	src := `package p
func GetName() {}
func getValue() {}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "GetName", "getter-no-return") {
		t.Error("GetName returning nothing should be flagged getter-no-return")
	}
	if !has(r, "getValue", "getter-no-return") {
		t.Error("getValue returning nothing should be flagged getter-no-return")
	}
}

func TestScan_GetterWithReturnClean(t *testing.T) {
	src := `package p
func GetName() string { return "" }
func getCount() (int, error) { return 0, nil }
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("getters with returns should be clean, got: %+v", r.Findings)
	}
}

// "Getter" word boundary: a func literally named "Get" with no suffix is excluded.
func TestScan_BareGetExcluded(t *testing.T) {
	src := `package p
func Get() {}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("bare Get (no word boundary) should be excluded, got: %+v", r.Findings)
	}
}

// ─── constructor-no-return ───────────────────────────────

func TestScan_ConstructorNoReturnFlagged(t *testing.T) {
	src := `package p
func NewServer() {}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "NewServer", "constructor-no-return") {
		t.Error("NewServer returning nothing should be flagged constructor-no-return")
	}
}

func TestScan_ConstructorWithReturnClean(t *testing.T) {
	src := `package p
func NewServer() *Server { return nil }
type Server struct{}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("constructor with return should be clean, got: %+v", r.Findings)
	}
}

// ─── methods named with (Recv).Method ────────────────────

func TestScan_MethodNameQualified(t *testing.T) {
	src := `package p
type T struct{}
func (t T) isOpen() int { return 0 }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "(T).isOpen", "predicate-not-bool") {
		t.Errorf("method should be named (T).isOpen, got: %+v", r.Findings)
	}
}

// ─── contract: skips & robustness ────────────────────────

func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
func isReady() int { return 1 }
`
	r := Scan(map[string]string{"x_test.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("_test.go must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_TestFuncSkipped(t *testing.T) {
	src := `package p
import "testing"
func TestIsReady(t *testing.T) {}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("TestXxx must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"README.md": "func isReady() int"})
	if r.FilesScanned != 0 {
		t.Errorf("non-go must not be scanned, FilesScanned=%d", r.FilesScanned)
	}
}

func TestScan_ParseErrorNoCrash(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("})
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
	r := Scan(map[string]string{})
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Errorf("empty input should be empty report, got %+v", r)
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := `package p
func isA() int { return 0 }
func GetB() {}
func NewC() {}
`
	a := Scan(map[string]string{"x.go": src})
	b := Scan(map[string]string{"x.go": src})
	if len(a.Findings) != len(b.Findings) {
		t.Fatalf("non-deterministic finding count: %d vs %d", len(a.Findings), len(b.Findings))
	}
	for i := range a.Findings {
		if a.Findings[i] != b.Findings[i] {
			t.Errorf("finding %d differs: %+v vs %+v", i, a.Findings[i], b.Findings[i])
		}
	}
}

func TestScan_FunctionLiteralIgnored(t *testing.T) {
	src := `package p
func wrap() {
	f := func() int { return 0 }
	_ = f
}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("function literals must be ignored, got: %+v", r.Findings)
	}
}

func TestScan_SummaryCounts(t *testing.T) {
	src := `package p
func isA() int { return 0 }
func GetB() {}
func NewC() {}
func clean() bool { return true }
`
	r := Scan(map[string]string{"x.go": src})
	if r.FuncsScanned != 4 {
		t.Errorf("FuncsScanned: want 4, got %d", r.FuncsScanned)
	}
	if r.Flagged != 3 {
		t.Errorf("Flagged: want 3, got %d", r.Flagged)
	}
}
