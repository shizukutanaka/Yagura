package errpolicy_test

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/errpolicy"
)

const wrappedSrc = `package foo

import "fmt"

func Do() error {
	if err := step(); err != nil {
		return fmt.Errorf("do step: %w", err)
	}
	return nil
}
`

const nakedSrc = `package foo

func Do() error {
	if err := step(); err != nil {
		return err
	}
	return nil
}

func Multi() (int, error) {
	v, err := compute()
	if err != nil {
		return 0, err
	}
	return v, nil
}
`

const discardSrc = `package foo

func Do() {
	_ = risky()
	x, _ := pair()
	_ = x
}
`

const freshErrorSrc = `package foo

import "errors"

func Do() error {
	if bad() {
		return errors.New("bad state")
	}
	return nil
}
`

const noErrFuncSrc = `package foo

func Add(a, b int) int {
	return a + b
}
`

func TestScan_WrappedReturn(t *testing.T) {
	r := errpolicy.Scan(map[string]string{"a.go": wrappedSrc})
	if r.WrappedReturns != 1 {
		t.Errorf("WrappedReturns: want 1 got %d", r.WrappedReturns)
	}
	if r.NakedReturns != 0 {
		t.Errorf("NakedReturns: want 0 got %d", r.NakedReturns)
	}
	if r.WrapRatio != 1.0 {
		t.Errorf("WrapRatio: want 1.0 got %f", r.WrapRatio)
	}
}

func TestScan_NakedReturn(t *testing.T) {
	r := errpolicy.Scan(map[string]string{"b.go": nakedSrc})
	if r.NakedReturns != 2 {
		t.Errorf("NakedReturns: want 2 got %d", r.NakedReturns)
	}
	if r.WrappedReturns != 0 {
		t.Errorf("WrappedReturns: want 0 got %d", r.WrappedReturns)
	}
	if r.WrapRatio != 0.0 {
		t.Errorf("WrapRatio: want 0.0 got %f", r.WrapRatio)
	}
	// naked returns should each produce a low-severity finding
	naked := 0
	for _, f := range r.Findings {
		if f.Rule == "naked-error-return" {
			naked++
		}
	}
	if naked != 2 {
		t.Errorf("naked-error-return findings: want 2 got %d", naked)
	}
}

func TestScan_BlankDiscard(t *testing.T) {
	r := errpolicy.Scan(map[string]string{"c.go": discardSrc})
	// `_ = risky()` is a pure blank-discard of a call result → flagged.
	// `x, _ := pair()` mixes a real binding, not a pure discard → not flagged.
	if r.BlankDiscards != 1 {
		t.Errorf("BlankDiscards: want 1 got %d", r.BlankDiscards)
	}
	found := false
	for _, f := range r.Findings {
		if f.Rule == "error-discarded" {
			found = true
		}
	}
	if !found {
		t.Error("expected an error-discarded finding")
	}
}

func TestScan_FreshErrorNotNaked(t *testing.T) {
	r := errpolicy.Scan(map[string]string{"d.go": freshErrorSrc})
	// errors.New(...) is a freshly-constructed error, not a naked pass-through.
	if r.NakedReturns != 0 {
		t.Errorf("NakedReturns: want 0 got %d", r.NakedReturns)
	}
	if r.WrappedReturns != 0 {
		t.Errorf("WrappedReturns: want 0 (errors.New is fresh, not wrapped) got %d", r.WrappedReturns)
	}
}

func TestScan_NoErrorFunc_Ignored(t *testing.T) {
	r := errpolicy.Scan(map[string]string{"e.go": noErrFuncSrc})
	if r.NakedReturns != 0 || r.WrappedReturns != 0 {
		t.Errorf("non-error func should produce no error returns: naked=%d wrapped=%d", r.NakedReturns, r.WrappedReturns)
	}
	if r.FilesScanned != 1 {
		t.Errorf("FilesScanned: want 1 got %d", r.FilesScanned)
	}
}

func TestScan_Empty(t *testing.T) {
	r := errpolicy.Scan(map[string]string{})
	if r.FilesScanned != 0 {
		t.Errorf("FilesScanned: want 0 got %d", r.FilesScanned)
	}
	if r.WrapRatio != 0 {
		t.Errorf("WrapRatio: want 0 got %f", r.WrapRatio)
	}
}

func TestScan_MixedWrapRatio(t *testing.T) {
	// wrappedSrc: 1 wrapped, 0 naked; nakedSrc: 0 wrapped, 2 naked
	// ratio = 1 / (1 + 2) = 0.333...
	r := errpolicy.Scan(map[string]string{
		"a.go": wrappedSrc,
		"b.go": nakedSrc,
	})
	if r.WrappedReturns != 1 || r.NakedReturns != 2 {
		t.Fatalf("counts: wrapped=%d naked=%d", r.WrappedReturns, r.NakedReturns)
	}
	want := 1.0 / 3.0
	if r.WrapRatio < want-0.001 || r.WrapRatio > want+0.001 {
		t.Errorf("WrapRatio: want ~%f got %f", want, r.WrapRatio)
	}
}

func TestScan_FuncLitErrorReturn(t *testing.T) {
	src := `package foo

import "fmt"

func outer() {
	fn := func() error {
		if bad() {
			return fmt.Errorf("inner: %w", baseErr)
		}
		return nil
	}
	_ = fn
}
`
	r := errpolicy.Scan(map[string]string{"f.go": src})
	if r.WrappedReturns != 1 {
		t.Errorf("WrappedReturns in func literal: want 1 got %d", r.WrappedReturns)
	}
}

func TestScan_Deterministic(t *testing.T) {
	files := map[string]string{
		"z.go": nakedSrc,
		"a.go": wrappedSrc,
		"m.go": discardSrc,
	}
	first := errpolicy.Scan(files)
	for i := 0; i < 20; i++ {
		got := errpolicy.Scan(files)
		if len(got.Findings) != len(first.Findings) {
			t.Fatalf("run %d: finding count drift %d vs %d", i, len(got.Findings), len(first.Findings))
		}
		for j := range got.Findings {
			if got.Findings[j] != first.Findings[j] {
				t.Fatalf("run %d: finding[%d] mismatch %+v vs %+v", i, j, got.Findings[j], first.Findings[j])
			}
		}
	}
}

func TestScan_ParseErrorSurfaced(t *testing.T) {
	r := errpolicy.Scan(map[string]string{"broken.go": "package foo\nfunc {{{"})
	found := false
	for _, f := range r.Findings {
		if f.Rule == "parse-error" {
			found = true
		}
	}
	if !found {
		t.Error("parse error should be surfaced, not silently dropped")
	}
}

func TestScan_FmtErrorfWithoutVerb_NotWrapped(t *testing.T) {
	// fmt.Errorf("static message") with no %w does not chain the cause.
	src := `package foo

import "fmt"

func Do() error {
	if bad() {
		return fmt.Errorf("something failed")
	}
	return nil
}
`
	r := errpolicy.Scan(map[string]string{"g.go": src})
	if r.WrappedReturns != 0 {
		t.Errorf("Errorf without %%w should not count as wrapped: got %d", r.WrappedReturns)
	}
}
