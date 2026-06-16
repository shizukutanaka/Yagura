package codehealth_test

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/codehealth"
)

func gradeOf(r codehealth.Report, pkg string) codehealth.PackageGrade {
	for _, p := range r.Packages {
		if p.Package == pkg {
			return p
		}
	}
	return codehealth.PackageGrade{}
}

func TestScore_PerfectPackage_GradeA(t *testing.T) {
	r := codehealth.Score([]codehealth.PackageSignals{
		{Package: "clean", Files: 3, ExportedTotal: 10, UndocumentedExports: 0},
	})
	g := gradeOf(r, "clean")
	if g.Score != 100 {
		t.Errorf("clean package score: want 100 got %d", g.Score)
	}
	if g.Grade != "A" {
		t.Errorf("clean grade: want A got %s", g.Grade)
	}
	if len(g.Reasons) != 0 {
		t.Errorf("clean reasons: want none got %v", g.Reasons)
	}
}

func TestScore_DocGapPenalty(t *testing.T) {
	// 5 of 10 exported undocumented → docGap 0.5 → penalty 12 (25*0.5).
	r := codehealth.Score([]codehealth.PackageSignals{
		{Package: "p", ExportedTotal: 10, UndocumentedExports: 5},
	})
	g := gradeOf(r, "p")
	if g.Score != 88 {
		t.Errorf("doc gap score: want 88 got %d", g.Score)
	}
	if len(g.Reasons) == 0 {
		t.Error("expected a doc-gap reason")
	}
}

func TestScore_ComplexityPenaltyCapped(t *testing.T) {
	// 100 high-complexity funcs → penalty capped at 30, not 400.
	r := codehealth.Score([]codehealth.PackageSignals{
		{Package: "p", HighComplexityFuncs: 100},
	})
	g := gradeOf(r, "p")
	if g.Score != 70 {
		t.Errorf("capped complexity score: want 70 got %d", g.Score)
	}
}

func TestScore_DeadAndRecvAndHollow(t *testing.T) {
	r := codehealth.Score([]codehealth.PackageSignals{
		{Package: "p", DeadDecls: 1, RecvIssues: 1, HollowTestFiles: 1},
	})
	g := gradeOf(r, "p")
	// -5 dead, -5 recv, -5 hollow = 85
	if g.Score != 85 {
		t.Errorf("score: want 85 got %d", g.Score)
	}
	if len(g.Reasons) != 3 {
		t.Errorf("reasons: want 3 got %d (%v)", len(g.Reasons), g.Reasons)
	}
}

func TestScore_ClampAtZero(t *testing.T) {
	r := codehealth.Score([]codehealth.PackageSignals{
		{Package: "p", ExportedTotal: 1, UndocumentedExports: 1,
			HighComplexityFuncs: 100, DeadDecls: 100, RecvIssues: 100, HollowTestFiles: 100},
	})
	g := gradeOf(r, "p")
	if g.Score != 0 {
		t.Errorf("score must clamp at 0; got %d", g.Score)
	}
	if g.Grade != "F" {
		t.Errorf("grade: want F got %s", g.Grade)
	}
}

func TestScore_GradeBands(t *testing.T) {
	cases := []struct {
		undoc, total int
		wantGrade    string
	}{
		{0, 10, "A"}, // 100
		{4, 10, "B"}, // -10 → 90? actually 25*0.4=10 → 90 = A. adjust below
	}
	_ = cases
	// Direct band check via dead-decl penalties for precision.
	mk := func(dead int) string {
		r := codehealth.Score([]codehealth.PackageSignals{{Package: "p", DeadDecls: dead}})
		return gradeOf(r, "p").Grade
	}
	// dead penalty 5 each, capped 20.
	if g := mk(0); g != "A" { // 100
		t.Errorf("0 dead: want A got %s", g)
	}
	if g := mk(2); g != "A" { // 90
		t.Errorf("2 dead (90): want A got %s", g)
	}
	if g := mk(3); g != "B" { // 85
		t.Errorf("3 dead (85): want B got %s", g)
	}
}

func TestScore_OverallAndSortWorstFirst(t *testing.T) {
	r := codehealth.Score([]codehealth.PackageSignals{
		{Package: "good", ExportedTotal: 10, UndocumentedExports: 0},
		{Package: "bad", DeadDecls: 4},
	})
	if len(r.Packages) != 2 {
		t.Fatalf("want 2 packages, got %d", len(r.Packages))
	}
	// worst first
	if r.Packages[0].Package != "bad" {
		t.Errorf("worst package should sort first; got %s", r.Packages[0].Package)
	}
	if r.OverallGrade == "" {
		t.Error("overall grade must be set")
	}
}

func TestScore_Empty(t *testing.T) {
	r := codehealth.Score(nil)
	if len(r.Packages) != 0 {
		t.Errorf("want 0 packages")
	}
	if r.OverallScore != 100 || r.OverallGrade != "A" {
		t.Errorf("empty repo is vacuously healthy; got %d/%s", r.OverallScore, r.OverallGrade)
	}
}

func TestScore_Deterministic(t *testing.T) {
	sigs := []codehealth.PackageSignals{
		{Package: "z", DeadDecls: 1},
		{Package: "a", DeadDecls: 1},
		{Package: "m", DeadDecls: 1},
	}
	first := codehealth.Score(sigs)
	for i := 0; i < 20; i++ {
		got := codehealth.Score(sigs)
		for j := range got.Packages {
			if got.Packages[j].Package != first.Packages[j].Package {
				t.Fatalf("run %d: order drift at %d", i, j)
			}
		}
	}
}

func TestAnalyze_Smoke(t *testing.T) {
	files := map[string]string{
		"pkg/a.go": `package pkg

// Good is documented.
func Good() int { return 1 }

func orphan() int { return 2 }
`,
	}
	r := codehealth.Analyze(files)
	g := gradeOf(r, "pkg")
	if g.Package != "pkg" {
		t.Fatalf("expected pkg grade, got %+v", r.Packages)
	}
	// orphan is dead → at least one reason; score < 100.
	if g.Score >= 100 {
		t.Errorf("pkg has dead code; score should be < 100, got %d", g.Score)
	}
}
