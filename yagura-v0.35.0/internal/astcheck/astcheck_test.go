package astcheck

import (
	"strings"
	"testing"
)

func ruleSet(fs []Finding) map[string]int {
	m := map[string]int{}
	for _, f := range fs {
		m[f.Rule]++
	}
	return m
}

// ─── os-exit-library: needs package context (regex cannot do this) ───────

func TestScanFile_OsExitInLibrary(t *testing.T) {
	src := `package foo
import "os"
func Boom() { os.Exit(1) }
`
	got := ruleSet(ScanFile("foo.go", src))
	if got["os-exit-library"] != 1 {
		t.Errorf("expected 1 os-exit-library finding in a non-main package, got %d", got["os-exit-library"])
	}
}

func TestScanFile_OsExitInMain_OK(t *testing.T) {
	src := `package main
import "os"
func main() { os.Exit(1) }
`
	if n := ruleSet(ScanFile("main.go", src))["os-exit-library"]; n != 0 {
		t.Errorf("os.Exit in package main is fine, got %d findings", n)
	}
}

func TestScanFile_OsExitInTest_OK(t *testing.T) {
	src := `package foo
import "os"
func TestX() { os.Exit(1) }
`
	if n := ruleSet(ScanFile("foo_test.go", src))["os-exit-library"]; n != 0 {
		t.Errorf("os.Exit in *_test.go is allowed, got %d findings", n)
	}
}

// ─── empty-nil-branch: needs block structure (regex cannot do this) ──────

func TestScanFile_EmptyNilBranch(t *testing.T) {
	src := `package foo
func F() {
	err := do()
	if err != nil {
	}
}
func do() error { return nil }
`
	if n := ruleSet(ScanFile("foo.go", src))["empty-nil-branch"]; n != 1 {
		t.Errorf("expected 1 empty-nil-branch finding, got %d", n)
	}
}

func TestScanFile_NonEmptyNilBranch_OK(t *testing.T) {
	src := `package foo
func F() error {
	err := do()
	if err != nil {
		return err
	}
	return nil
}
func do() error { return nil }
`
	if n := ruleSet(ScanFile("foo.go", src))["empty-nil-branch"]; n != 0 {
		t.Errorf("a handled err != nil branch should not flag, got %d", n)
	}
}

// ─── parse errors are surfaced, not silently skipped ─────────────────────

func TestScanFile_ParseError(t *testing.T) {
	got := ruleSet(ScanFile("broken.go", "package foo\nfunc F( {"))
	if got["parse-error"] < 1 {
		t.Errorf("expected a parse-error finding for unparseable Go, got %v", got)
	}
}

// ─── ScanFiles: Go-only, deterministic ───────────────────────────────────

func TestScanFiles_SkipsNonGo(t *testing.T) {
	files := map[string]string{
		"a.go":   "package foo\nimport \"os\"\nfunc B(){ os.Exit(1) }\n",
		"b.txt":  "os.Exit(1) here is just text",
		"c.json": "{}",
	}
	res := ScanFiles(files)
	if res.FilesScanned != 1 {
		t.Errorf("only a.go is Go; FilesScanned=%d want 1", res.FilesScanned)
	}
	if res.ByRule["os-exit-library"] != 1 {
		t.Errorf("expected the .go os.Exit flagged, non-go ignored; got %v", res.ByRule)
	}
}

// TestScanFiles_Deterministic: output must be byte-stable across the
// randomized map iteration (total-order sort), per the repo invariant.
func TestScanFiles_Deterministic(t *testing.T) {
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		files[n+".go"] = "package foo\nimport \"os\"\nfunc X(){ os.Exit(1) }\n"
	}
	sig := func(r Result) string {
		var sb strings.Builder
		for _, f := range r.Findings {
			sb.WriteString(f.File)
			sb.WriteByte('\n')
		}
		return sb.String()
	}
	want := sig(ScanFiles(files))
	for i := 0; i < 30; i++ {
		if got := sig(ScanFiles(files)); got != want {
			t.Fatalf("non-deterministic ScanFiles order:\n%s\nvs\n%s", want, got)
		}
	}
}

// ─── defer-in-loop: needs loop+func scope tracking (regex cannot) ─────────

func TestScanFile_DeferInForLoop(t *testing.T) {
	src := `package foo
func F(xs []string) {
	for i := 0; i < len(xs); i++ {
		defer cleanup()
	}
}
func cleanup() {}
`
	if n := ruleSet(ScanFile("foo.go", src))["defer-in-loop"]; n != 1 {
		t.Errorf("expected 1 defer-in-loop finding, got %d", n)
	}
}

func TestScanFile_DeferInRangeLoop(t *testing.T) {
	src := `package foo
func F(xs []string) {
	for range xs {
		defer cleanup()
	}
}
func cleanup() {}
`
	if n := ruleSet(ScanFile("foo.go", src))["defer-in-loop"]; n != 1 {
		t.Errorf("expected 1 defer-in-loop finding for range loop, got %d", n)
	}
}

func TestScanFile_DeferNotInLoop_OK(t *testing.T) {
	src := `package foo
func F() {
	defer cleanup()
}
func cleanup() {}
`
	if n := ruleSet(ScanFile("foo.go", src))["defer-in-loop"]; n != 0 {
		t.Errorf("a top-level defer must not flag, got %d", n)
	}
}

// A defer inside a closure that is invoked each iteration is the idiomatic fix
// (the closure returns every iteration), so it must NOT be flagged.
func TestScanFile_DeferInClosureInLoop_OK(t *testing.T) {
	src := `package foo
func F(xs []string) {
	for range xs {
		func() {
			defer cleanup()
		}()
	}
}
func cleanup() {}
`
	if n := ruleSet(ScanFile("foo.go", src))["defer-in-loop"]; n != 0 {
		t.Errorf("defer in a per-iteration closure is fine, got %d", n)
	}
}

// ─── error-string-compare: needs to see .Error() as a == operand (regex can't) ──

func TestScanFile_ErrorStringCompareEq(t *testing.T) {
	src := `package foo
func F(err error) bool {
	return err.Error() == "not found"
}
`
	if n := ruleSet(ScanFile("foo.go", src))["error-string-compare"]; n != 1 {
		t.Errorf("expected 1 error-string-compare finding, got %d", n)
	}
}

func TestScanFile_ErrorStringCompareNeq(t *testing.T) {
	src := `package foo
func F(err error) {
	if err.Error() != "x" {
		_ = err
	}
}
`
	if n := ruleSet(ScanFile("foo.go", src))["error-string-compare"]; n != 1 {
		t.Errorf("expected 1 error-string-compare finding for !=, got %d", n)
	}
}

// .Error() used for logging (not in a == / != comparison) must NOT flag.
func TestScanFile_ErrorStringForLogging_OK(t *testing.T) {
	src := `package foo
import "fmt"
func F(err error) {
	fmt.Println(err.Error())
}
`
	if n := ruleSet(ScanFile("foo.go", src))["error-string-compare"]; n != 0 {
		t.Errorf("err.Error() for logging is fine, got %d", n)
	}
}

func TestScanFile_ErrorsIs_OK(t *testing.T) {
	src := `package foo
import "errors"
var ErrX = errors.New("x")
func F(err error) bool {
	return errors.Is(err, ErrX)
}
`
	if n := ruleSet(ScanFile("foo.go", src))["error-string-compare"]; n != 0 {
		t.Errorf("errors.Is must not flag, got %d", n)
	}
}

// ─── panic-in-library (v0.39.0): panic in non-main, non-test package ─────

func TestScanFiles_PanicInLibrary(t *testing.T) {
	src := `package mylib
func DoThing(x int) {
	if x < 0 {
		panic("negative x")
	}
}
`
	got := ruleSet(ScanFile("mylib.go", src))
	if got["panic-in-library"] != 1 {
		t.Errorf("expected 1 panic-in-library finding in non-main package, got %d", got["panic-in-library"])
	}
}

func TestScanFiles_PanicInMain(t *testing.T) {
	src := `package main
func main() {
	panic("unrecoverable")
}
`
	got := ruleSet(ScanFile("main.go", src))
	if got["panic-in-library"] != 0 {
		t.Errorf("panic in package main must not flag panic-in-library, got %d", got["panic-in-library"])
	}
}

func TestScanFiles_PanicInTest_OK(t *testing.T) {
	src := `package mylib
func TestSomething() {
	panic("test panic")
}
`
	got := ruleSet(ScanFile("mylib_test.go", src))
	if got["panic-in-library"] != 0 {
		t.Errorf("panic in _test.go must not flag, got %d", got["panic-in-library"])
	}
}

// ─── bare-goroutine (v0.39.0): anonymous goroutine without context ────────

func TestScanFiles_BareGoroutine(t *testing.T) {
	src := `package foo
func Start() {
	go func() {
		doWork()
	}()
}
func doWork() {}
`
	got := ruleSet(ScanFile("foo.go", src))
	if got["bare-goroutine"] != 1 {
		t.Errorf("expected 1 bare-goroutine finding, got %d", got["bare-goroutine"])
	}
}

func TestScanFiles_BareGoroutine_WithContext(t *testing.T) {
	src := `package foo
import "context"
func Start(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		}
	}()
}
`
	got := ruleSet(ScanFile("foo.go", src))
	if got["bare-goroutine"] != 0 {
		t.Errorf("goroutine that references ctx must not flag bare-goroutine, got %d", got["bare-goroutine"])
	}
}

func TestScanFiles_NamedGoroutine_OK(t *testing.T) {
	src := `package foo
func Start() {
	go worker()
}
func worker() {}
`
	got := ruleSet(ScanFile("foo.go", src))
	if got["bare-goroutine"] != 0 {
		t.Errorf("named goroutine (not a func literal) must not flag, got %d", got["bare-goroutine"])
	}
}

// ─── blank-error-discard (v0.43.0): _ = call() silences return value ──────

func TestScanFile_BlankErrorDiscard_Library(t *testing.T) {
	src := `package foo
func F() {
	_ = doSomething()
}
func doSomething() error { return nil }
`
	got := ruleSet(ScanFile("foo.go", src))
	if got["blank-error-discard"] != 1 {
		t.Errorf("expected 1 blank-error-discard in library code, got %d", got["blank-error-discard"])
	}
}

func TestScanFile_BlankErrorDiscard_Main_OK(t *testing.T) {
	// package main is allowed (e.g. defer conn.Close() → _ = conn.Close() is acceptable)
	src := `package main
func main() {
	_ = doSomething()
}
func doSomething() error { return nil }
`
	got := ruleSet(ScanFile("main.go", src))
	if got["blank-error-discard"] != 0 {
		t.Errorf("blank-error-discard must not flag package main, got %d", got["blank-error-discard"])
	}
}

func TestScanFile_BlankErrorDiscard_Test_OK(t *testing.T) {
	// _test.go files are allowed
	src := `package foo
func TestX(t *testing.T) {
	_ = doSomething()
}
func doSomething() error { return nil }
`
	got := ruleSet(ScanFile("foo_test.go", src))
	if got["blank-error-discard"] != 0 {
		t.Errorf("blank-error-discard must not flag _test.go, got %d", got["blank-error-discard"])
	}
}

func TestScanFile_BlankErrorDiscard_DeferClose_OK(t *testing.T) {
	// _ = f.Close() inside a defer is a common idiom — NOT flagged because it's
	// inside a FuncLit passed to defer. But _ = f.Close() at statement level is flagged.
	// This test verifies the statement-level case IS flagged.
	src := `package foo
import "os"
func G() {
	f, _ := os.Open("x")
	_ = f.Close()
}
`
	got := ruleSet(ScanFile("foo.go", src))
	if got["blank-error-discard"] < 1 {
		t.Errorf("expected at least 1 blank-error-discard for _ = f.Close(), got %d", got["blank-error-discard"])
	}
}

func TestScanFile_BlankErrorDiscard_MultiBlank_Flagged(t *testing.T) {
	// _ = call() with single blank on LHS should be flagged
	src := `package mylib
func H() {
	_ = riskyOp()
}
func riskyOp() (int, error) { return 0, nil }
`
	got := ruleSet(ScanFile("mylib.go", src))
	if got["blank-error-discard"] != 1 {
		t.Errorf("expected 1 blank-error-discard for _ = riskyOp(), got %d", got["blank-error-discard"])
	}
}

// ─── bare-goroutine: lifecycle detection improvements ────────────────────────

func TestScanFile_BareGoroutine_NoLifecycle_Flagged(t *testing.T) {
	src := `package mylib
import "fmt"
func F() {
	go func() { fmt.Println("hi") }()
}
`
	got := ruleSet(ScanFile("lib.go", src))
	if got["bare-goroutine"] != 1 {
		t.Errorf("expected 1 bare-goroutine for plain anon goroutine, got %d", got["bare-goroutine"])
	}
}

func TestScanFile_BareGoroutine_TestFile_OK(t *testing.T) {
	src := `package mylib
import "fmt"
func F() {
	go func() { fmt.Println("hi") }()
}
`
	// test file suffix skips the rule
	got := ruleSet(ScanFile("lib_test.go", src))
	if got["bare-goroutine"] != 0 {
		t.Errorf("bare-goroutine should not fire in test files, got %d", got["bare-goroutine"])
	}
}

func TestScanFile_BareGoroutine_WithParams_OK(t *testing.T) {
	src := `package mylib
func F() {
	go func(n int) { _ = n }(42)
}
`
	got := ruleSet(ScanFile("lib.go", src))
	if got["bare-goroutine"] != 0 {
		t.Errorf("parametric goroutine should not fire bare-goroutine, got %d", got["bare-goroutine"])
	}
}

func TestScanFile_BareGoroutine_WithWaitGroup_OK(t *testing.T) {
	src := `package mylib
import "sync"
func F(wg *sync.WaitGroup) {
	go func() { defer wg.Done() }()
}
`
	got := ruleSet(ScanFile("lib.go", src))
	if got["bare-goroutine"] != 0 {
		t.Errorf("WaitGroup goroutine should not fire bare-goroutine, got %d", got["bare-goroutine"])
	}
}

func TestScanFile_BareGoroutine_WithChannel_OK(t *testing.T) {
	src := `package mylib
func F() chan struct{} {
	ch := make(chan struct{}, 1)
	go func() { ch <- struct{}{} }()
	return ch
}
`
	got := ruleSet(ScanFile("lib.go", src))
	if got["bare-goroutine"] != 0 {
		t.Errorf("channel-sync goroutine should not fire bare-goroutine, got %d", got["bare-goroutine"])
	}
}
