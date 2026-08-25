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
	// naked returns are an aggregate metric, NOT per-site findings (package
	// contract: blank-discard is the actionable finding; naked feeds the ratio).
	// nakedSrc has no discards / parse errors, so Findings must be empty.
	for _, f := range r.Findings {
		if f.Rule == "naked-error-return" {
			t.Errorf("naked returns must not produce findings (noise); got %+v", f)
		}
	}
	if len(r.Findings) != 0 {
		t.Errorf("Findings: want 0 (naked is metric-only) got %d: %+v", len(r.Findings), r.Findings)
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

// _test.go は対象外(v1.3.0)。
//
// 自リポジトリ実測: blank-discard の指摘 **396 件がすべて _test.go**、production は 0 件。
// テストで `_ = os.WriteFile(...)` や `defer func(){ _ = f.Close() }()` と書くのは
// 完全に通常であり、しかも `_ =` は「黙って捨てる」の **反対**——明示的に捨てる
// Go の作法そのもの。このリポジトリの他レンズ(errdiscard/complexity/nestdepth…)は
// 一様に _test.go を除外しており、errpolicy だけが揃っていなかった。
//
// 「技術的には正しいが誰も行動しない指摘」は effective false positive
// (Sadowski et al., CACM 2018)であり、偽陽性と同じ害がある。
func TestScan_SkipsTestFiles(t *testing.T) {
	files := map[string]string{
		"a_test.go": `package p

func TestX() {
	_ = doThing()
}

func doThing() error { return nil }
`,
	}
	rep := errpolicy.Scan(files)
	if len(rep.Findings) != 0 {
		t.Errorf("_test.go must not produce findings, got %d: %+v", len(rep.Findings), rep.Findings)
	}
	if rep.FilesScanned != 0 {
		t.Errorf("test files should not count as scanned, got %d", rep.FilesScanned)
	}
}

// production の blank discard は今も検出する(recall を捨てていないこと)。
func TestScan_StillFlagsProductionBlankDiscard(t *testing.T) {
	files := map[string]string{
		"a.go": `package p

func caller() {
	_ = doThing()
}

func doThing() error { return nil }
`,
	}
	rep := errpolicy.Scan(files)
	if rep.BlankDiscards != 1 {
		t.Errorf("production blank discard must still be reported, got %d", rep.BlankDiscards)
	}
}
