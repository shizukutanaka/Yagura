package ctxcheck

import "testing"

func has(r Report, fn, rule string) bool {
	for _, f := range r.Findings {
		if f.Func == fn && f.Rule == rule {
			return true
		}
	}
	return false
}

// ─── context-not-first ───────────────────────────────────

func TestScan_CtxSecondParamFlagged(t *testing.T) {
	src := `package p
import "context"
func Do(w int, ctx context.Context) {}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "Do", "context-not-first") {
		t.Errorf("ctx as 2nd param should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_CtxFirstParamClean(t *testing.T) {
	src := `package p
import "context"
func Do(ctx context.Context, w int) {}
func Only(ctx context.Context) {}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("ctx-first should be clean, got: %+v", r.Findings)
	}
}

func TestScan_NoCtxParamClean(t *testing.T) {
	src := `package p
func Do(w int, s string) {}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("no-ctx function should be clean, got: %+v", r.Findings)
	}
}

// Method: receiver does not count as a parameter; ctx must be first actual param.
func TestScan_MethodCtxFirstClean(t *testing.T) {
	src := `package p
import "context"
type T struct{}
func (t T) Do(ctx context.Context, x int) {}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("method with ctx-first should be clean, got: %+v", r.Findings)
	}
}

func TestScan_MethodCtxSecondFlagged(t *testing.T) {
	src := `package p
import "context"
type T struct{}
func (t T) Do(x int, ctx context.Context) {}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "(T).Do", "context-not-first") {
		t.Errorf("method with ctx-second should be flagged (T).Do, got: %+v", r.Findings)
	}
}

// Canonical exception: *testing.T/B/F as first param, ctx second, is allowed
// (test helper pattern). Even though we skip _test.go, helpers can live in
// production files.
func TestScan_TestingHelperExceptionClean(t *testing.T) {
	src := `package p
import (
	"context"
	"testing"
)
func helper(t *testing.T, ctx context.Context) {}
func benchHelper(b *testing.B, ctx context.Context) {}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("testing.T/B helper with ctx-second should be exempt, got: %+v", r.Findings)
	}
}

// Multiple ctx params: flagged if the FIRST occurrence is not at position 0.
func TestScan_PointerOrAliasNotConfused(t *testing.T) {
	// A param named ctx but of a different type must NOT trigger; only real context.Context.
	src := `package p
func Do(w int, ctx string) {}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("non-context type named ctx must not be flagged, got: %+v", r.Findings)
	}
}

// context imported under a different local name is conservatively not detected
// (no type resolution) — must not crash and must not false-positive.
func TestScan_AliasedImportNotDetected(t *testing.T) {
	src := `package p
import ctxpkg "context"
func Do(w int, ctx ctxpkg.Context) {}
`
	r := Scan(map[string]string{"x.go": src})
	// We only match the literal selector context.Context; aliased import yields no finding.
	if has(r, "Do", "context-not-first") {
		t.Errorf("aliased context import should be conservatively skipped, got: %+v", r.Findings)
	}
}

// ─── contained-ctx ───────────────────────────────────────

func TestScan_StructWithCtxFieldFlagged(t *testing.T) {
	src := `package p
import "context"
type Server struct {
	ctx context.Context
	n   int
}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "Server", "contained-ctx") {
		t.Errorf("struct with context field should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_StructWithoutCtxFieldClean(t *testing.T) {
	src := `package p
type Server struct {
	n int
	s string
}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("struct without context field should be clean, got: %+v", r.Findings)
	}
}

func TestScan_EmptyStructClean(t *testing.T) {
	src := `package p
type Empty struct{}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("empty struct should be clean, got: %+v", r.Findings)
	}
}

// Embedded context.Context (anonymous field) also counts.
func TestScan_EmbeddedCtxFlagged(t *testing.T) {
	src := `package p
import "context"
type Wrapper struct {
	context.Context
}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "Wrapper", "contained-ctx") {
		t.Errorf("embedded context.Context should be flagged, got: %+v", r.Findings)
	}
}

// ─── contract: skips & robustness ────────────────────────

func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
import "context"
func Do(w int, ctx context.Context) {}
`
	r := Scan(map[string]string{"x_test.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("_test.go must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"README.md": "func Do(w int, ctx context.Context)"})
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
import "context"
func A(w int, ctx context.Context) {}
type S struct{ ctx context.Context }
`
	a := Scan(map[string]string{"x.go": src})
	b := Scan(map[string]string{"x.go": src})
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
import "context"
func A(w int, ctx context.Context) {}
type S struct{ ctx context.Context }
func Clean(ctx context.Context) {}
`
	r := Scan(map[string]string{"x.go": src})
	if r.Flagged != 2 {
		t.Errorf("Flagged: want 2, got %d", r.Flagged)
	}
}
