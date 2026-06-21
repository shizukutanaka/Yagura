package errwrap

import "testing"

func has(r Report, rule string) bool {
	for _, f := range r.Findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

func countRule(r Report, rule string) int {
	n := 0
	for _, f := range r.Findings {
		if f.Rule == rule {
			n++
		}
	}
	return n
}

// ─── non-wrapping-verb ───────────────────────────────────

func TestScan_ErrorfWithVForErrorFlagged(t *testing.T) {
	src := `package p
import "fmt"
func f(err error) error { return fmt.Errorf("failed: %v", err) }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "non-wrapping-verb") {
		t.Errorf("fmt.Errorf(%%v, err) should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_ErrorfWithWClean(t *testing.T) {
	src := `package p
import "fmt"
func f(err error) error { return fmt.Errorf("failed: %w", err) }
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("fmt.Errorf(%%w, err) should be clean, got: %+v", r.Findings)
	}
}

func TestScan_ErrorfNoErrorArgClean(t *testing.T) {
	src := `package p
import "fmt"
func f(n int) error { return fmt.Errorf("count is %d", n) }
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "non-wrapping-verb") {
		t.Errorf("no error arg should not be flagged, got: %+v", r.Findings)
	}
}

func TestScan_ErrorfNamedErrVarFlagged(t *testing.T) {
	src := `package p
import "fmt"
func f() error {
	readErr := doRead()
	return fmt.Errorf("read step: %s", readErr)
}
func doRead() error { return nil }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "non-wrapping-verb") {
		t.Errorf("named *Err var formatted with %%s should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_ErrorfMixedHasWClean(t *testing.T) {
	// Already wrapping one error with %w → not flagged (conservative).
	src := `package p
import "fmt"
func f(err error, x int) error { return fmt.Errorf("%w (code %d)", err, x) }
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "non-wrapping-verb") {
		t.Errorf("format already containing %%w should be clean, got: %+v", r.Findings)
	}
}

func TestScan_ErrorfNonLiteralFormatSkipped(t *testing.T) {
	src := `package p
import "fmt"
func f(format string, err error) error { return fmt.Errorf(format, err) }
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "non-wrapping-verb") {
		t.Errorf("non-literal format string can't be analyzed; must not flag, got: %+v", r.Findings)
	}
}

// ─── err-value-compare ───────────────────────────────────

func TestScan_ErrEqualsSentinelFlagged(t *testing.T) {
	src := `package p
import "io"
func f(err error) bool { return err == io.EOF }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "err-value-compare") {
		t.Errorf("err == io.EOF should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_ErrNotEqualSentinelFlagged(t *testing.T) {
	src := `package p
var ErrFoo = mkErr()
func mkErr() error { return nil }
func f(err error) bool { return err != ErrFoo }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "err-value-compare") {
		t.Errorf("err != ErrFoo should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_ErrEqualsNilClean(t *testing.T) {
	src := `package p
func f(err error) bool {
	if err != nil {
		return false
	}
	return err == nil
}
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "err-value-compare") {
		t.Errorf("err == nil / err != nil are idiomatic; must not flag, got: %+v", r.Findings)
	}
}

func TestScan_NonErrorComparisonClean(t *testing.T) {
	src := `package p
func f(n int) bool { return n == 5 }
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "err-value-compare") {
		t.Errorf("non-error comparison must not be flagged, got: %+v", r.Findings)
	}
}

// ─── err-type-assert ─────────────────────────────────────

func TestScan_ErrTypeAssertFlagged(t *testing.T) {
	src := `package p
type myErr struct{}
func (myErr) Error() string { return "" }
func f(err error) {
	_ = err.(myErr)
}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "err-type-assert") {
		t.Errorf("err.(T) should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_ErrTypeAssertCommaOkFlagged(t *testing.T) {
	src := `package p
import "go/scanner"
func f(err error) int {
	if el, ok := err.(scanner.ErrorList); ok && len(el) > 0 {
		return el[0].Pos.Line
	}
	return 0
}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "err-type-assert") {
		t.Errorf("comma-ok err.(T) should be flagged (wrapped errors silently miss), got: %+v", r.Findings)
	}
}

func TestScan_TypeSwitchNotFlagged(t *testing.T) {
	src := `package p
func f(err error) string {
	switch err.(type) {
	case nil:
		return "nil"
	default:
		return "other"
	}
}
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "err-type-assert") {
		t.Errorf("err.(type) switch must not be flagged, got: %+v", r.Findings)
	}
}

func TestScan_NonErrorAssertClean(t *testing.T) {
	src := `package p
func f(v any) int {
	if n, ok := v.(int); ok {
		return n
	}
	return 0
}
`
	r := Scan(map[string]string{"x.go": src})
	if has(r, "err-type-assert") {
		t.Errorf("non-error type assertion must not be flagged, got: %+v", r.Findings)
	}
}

// ─── contract: skips & robustness ────────────────────────

func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
import "fmt"
func f(err error) error { return fmt.Errorf("x: %v", err) }
`
	r := Scan(map[string]string{"x_test.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("_test.go must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"README.md": "fmt.Errorf(\"%v\", err)"})
	if r.FilesScanned != 0 {
		t.Errorf("non-go must not be scanned, FilesScanned=%d", r.FilesScanned)
	}
}

func TestScan_ParseErrorNoCrash(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("})
	if !has(r, "parse-error") {
		t.Error("broken source should yield a parse-error finding, not a crash")
	}
}

func TestScan_EmptyInput(t *testing.T) {
	r := Scan(map[string]string{})
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Errorf("empty input should be empty report, got %+v", r)
	}
}

func TestScan_FuncAttribution(t *testing.T) {
	src := `package p
import "fmt"
func MyFunc(err error) error { return fmt.Errorf("x: %v", err) }
`
	r := Scan(map[string]string{"x.go": src})
	found := false
	for _, f := range r.Findings {
		if f.Rule == "non-wrapping-verb" && f.Func == "MyFunc" {
			found = true
		}
	}
	if !found {
		t.Errorf("finding should be attributed to MyFunc, got: %+v", r.Findings)
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := `package p
import (
	"fmt"
	"io"
)
func a(err error) error { return fmt.Errorf("%v", err) }
func b(err error) bool { return err == io.EOF }
`
	x := Scan(map[string]string{"x.go": src})
	y := Scan(map[string]string{"x.go": src})
	if len(x.Findings) != len(y.Findings) {
		t.Fatalf("non-deterministic: %d vs %d", len(x.Findings), len(y.Findings))
	}
	for i := range x.Findings {
		if x.Findings[i] != y.Findings[i] {
			t.Errorf("finding %d differs", i)
		}
	}
}

func TestScan_SummaryCounts(t *testing.T) {
	src := `package p
import (
	"fmt"
	"io"
)
func a(err error) error { return fmt.Errorf("%v", err) }
func b(err error) bool { return err == io.EOF }
func c(err error) { _ = err.(interface{ Error() string }) }
`
	r := Scan(map[string]string{"x.go": src})
	if countRule(r, "non-wrapping-verb") != 1 {
		t.Errorf("want 1 non-wrapping-verb, got %d", countRule(r, "non-wrapping-verb"))
	}
	if countRule(r, "err-value-compare") != 1 {
		t.Errorf("want 1 err-value-compare, got %d", countRule(r, "err-value-compare"))
	}
	if countRule(r, "err-type-assert") != 1 {
		t.Errorf("want 1 err-type-assert, got %d", countRule(r, "err-type-assert"))
	}
	if r.Flagged != 3 {
		t.Errorf("Flagged: want 3, got %d", r.Flagged)
	}
}
