package coverage

import (
	"math"
	"testing"
)

func TestClassify_MixedTree(t *testing.T) {
	r := Classify([]string{"main.go", "app.rb", "README.md"})
	if r.TotalFiles != 3 {
		t.Errorf("TotalFiles=%d want 3", r.TotalFiles)
	}
	if r.Analyzable != 1 || r.ByLanguage["go"] != 1 {
		t.Errorf("expected 1 analyzable go file, got %d %v", r.Analyzable, r.ByLanguage)
	}
	if r.UncoveredSource != 1 || r.UncoveredByExt[".rb"] != 1 {
		t.Errorf("expected .rb as uncovered source, got %d %v", r.UncoveredSource, r.UncoveredByExt)
	}
	if r.NonSource != 1 {
		t.Errorf("README.md should be non-source, NonSource=%d", r.NonSource)
	}
	// coverage = analyzable / (analyzable + uncovered_source) = 1/2
	if math.Abs(r.CoverageRatio-0.5) > 1e-9 {
		t.Errorf("CoverageRatio=%v want 0.5", r.CoverageRatio)
	}
}

func TestClassify_AllCovered(t *testing.T) {
	r := Classify([]string{"a.go", "b.ts", "c.py"})
	if r.CoverageRatio != 1.0 {
		t.Errorf("all-covered should be ratio 1.0, got %v", r.CoverageRatio)
	}
	if r.UncoveredSource != 0 {
		t.Errorf("no uncovered source expected, got %d", r.UncoveredSource)
	}
}

func TestClassify_NoCodeIsFullyCovered(t *testing.T) {
	// docs/config only: there is no code to miss → trivially 1.0.
	r := Classify([]string{"README.md", "config.json"})
	if r.CoverageRatio != 1.0 {
		t.Errorf("no-code tree should be ratio 1.0, got %v", r.CoverageRatio)
	}
	if r.NonSource != 2 {
		t.Errorf("expected 2 non-source, got %d", r.NonSource)
	}
}

func TestClassify_UncoveredLanguages(t *testing.T) {
	r := Classify([]string{"x.php", "y.c", "z.sh", "w.cs"})
	if r.UncoveredSource != 4 {
		t.Errorf("php/c/sh/cs are uncovered source, got %d (%v)", r.UncoveredSource, r.UncoveredByExt)
	}
	if r.Analyzable != 0 || r.CoverageRatio != 0 {
		t.Errorf("no analyzable code → ratio 0, got analyzable=%d ratio=%v", r.Analyzable, r.CoverageRatio)
	}
}

func TestClassify_Empty(t *testing.T) {
	r := Classify(nil)
	if r.TotalFiles != 0 || r.CoverageRatio != 1.0 {
		t.Errorf("empty → 0 files, ratio 1.0; got %d %v", r.TotalFiles, r.CoverageRatio)
	}
}

func TestClassify_LanguageMapping(t *testing.T) {
	r := Classify([]string{"a.go", "b.tsx", "c.jsx", "d.py", "e.rs", "f.java"})
	for lang, want := range map[string]int{"go": 1, "ts": 1, "js": 1, "python": 1, "rust": 1, "java": 1} {
		if r.ByLanguage[lang] != want {
			t.Errorf("ByLanguage[%q]=%d want %d (%v)", lang, r.ByLanguage[lang], want, r.ByLanguage)
		}
	}
}

// ─── AST-lens tier (Socratic finding: Analyzable conflates two tiers) ──
//
// Analyzable counts files the polyglot sensor tier (qualitycheck/secretscan/
// testcoverage) can look at. But the 25+ go/ast quality lenses (complexity/
// cognit/nestdepth/.../hotspot/lensoverlap) only ever work on .go — a
// pure-Python project would read CoverageRatio=1.0 (misleadingly implying
// full "clean" confidence) while none of those lenses ever fire on it.

func TestClassify_ASTLensCoverageOnlyCountsGo(t *testing.T) {
	r := Classify([]string{"a.go", "b.py", "c.ts"})
	if r.Analyzable != 3 {
		t.Fatalf("expected all 3 sensor-tier analyzable, got %d", r.Analyzable)
	}
	if r.ASTLensAnalyzable != 1 {
		t.Errorf("expected only 1 AST-lens-tier analyzable (a.go), got %d", r.ASTLensAnalyzable)
	}
}

func TestClassify_ASTLensCoverageRatio_PureGoIsFullCoverage(t *testing.T) {
	r := Classify([]string{"a.go", "b.go"})
	if r.ASTLensCoverageRatio != 1.0 {
		t.Errorf("pure-Go tree should be AST-lens ratio 1.0, got %v", r.ASTLensCoverageRatio)
	}
}

func TestClassify_ASTLensCoverageRatio_PurePythonIsZero(t *testing.T) {
	// The exact scenario the finding describes: sensor-tier coverage reads
	// full confidence while the AST-lens tier reveals it never runs at all.
	r := Classify([]string{"a.py", "b.py"})
	if r.CoverageRatio != 1.0 {
		t.Fatalf("sensor-tier coverage should still read 1.0 (qualitycheck/secretscan do cover .py), got %v", r.CoverageRatio)
	}
	if r.ASTLensCoverageRatio != 0 {
		t.Errorf("AST-lens-tier coverage should be 0 for an all-Python tree, got %v", r.ASTLensCoverageRatio)
	}
}

func TestClassify_ASTLensCoverageRatio_NoCodeIsFullyCovered(t *testing.T) {
	r := Classify([]string{"README.md"})
	if r.ASTLensCoverageRatio != 1.0 {
		t.Errorf("no-code tree should be AST-lens ratio 1.0 too, got %v", r.ASTLensCoverageRatio)
	}
}

func TestClassify_ASTLensCoverageRatio_Empty(t *testing.T) {
	r := Classify(nil)
	if r.ASTLensCoverageRatio != 1.0 {
		t.Errorf("empty input should be AST-lens ratio 1.0, got %v", r.ASTLensCoverageRatio)
	}
}

func TestClassify_ASTLensCoverageRatio_MixedGoAndOther(t *testing.T) {
	// 1 go + 1 py + 1 rb(uncovered) => code=3, ast-lens-analyzable=1 => ratio 1/3
	r := Classify([]string{"a.go", "b.py", "c.rb"})
	want := 1.0 / 3.0
	if r.ASTLensCoverageRatio < want-1e-9 || r.ASTLensCoverageRatio > want+1e-9 {
		t.Errorf("ASTLensCoverageRatio=%v want %v", r.ASTLensCoverageRatio, want)
	}
}
