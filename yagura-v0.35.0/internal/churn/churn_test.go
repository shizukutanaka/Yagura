package churn

import (
	"math"
	"testing"
)

// sample git log --numstat output: 3 commits across 2 ISO weeks.
const sampleLog = `a1b2c3|2026-01-05T10:00:00+09:00
10	4	hot.go
2	0	cold.go

d4e5f6|2026-01-06T11:00:00+09:00
20	6	hot.go

7a8b9c|2026-01-15T09:00:00+09:00
5	1	hot.go
`

func TestParse_ExtractsCommitsAndNumstat(t *testing.T) {
	commits, err := Parse(sampleLog)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(commits))
	}
	if commits[0].Hash != "a1b2c3" {
		t.Errorf("hash = %q", commits[0].Hash)
	}
	if len(commits[0].Files) != 2 {
		t.Fatalf("commit 0 should touch 2 files, got %d", len(commits[0].Files))
	}
	f := commits[0].Files[0]
	if f.Path != "hot.go" || f.Added != 10 || f.Deleted != 4 {
		t.Errorf("file0 = %+v, want hot.go +10 -4", f)
	}
}

// TestParse_SkipsBinaryFiles: git prints "-\t-\tpath" for binaries; they carry
// no LOC signal and must not corrupt the churn totals.
func TestParse_SkipsBinaryFiles(t *testing.T) {
	commits, err := Parse("aaa|2026-01-05T10:00:00Z\n-\t-\timage.png\n3\t1\tcode.go\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || len(commits[0].Files) != 1 {
		t.Fatalf("binary line should be skipped, got %+v", commits)
	}
	if commits[0].Files[0].Path != "code.go" {
		t.Errorf("kept the wrong file: %+v", commits[0].Files)
	}
}

func TestParse_EmptyLogIsNotAnError(t *testing.T) {
	commits, err := Parse("")
	if err != nil {
		t.Fatalf("empty log should not error: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("expected no commits, got %d", len(commits))
	}
}

// TestAnalyze_RelativeMeasures pins the Nagappan & Ball M1-M8 formulas.
func TestAnalyze_RelativeMeasures(t *testing.T) {
	commits, _ := Parse(sampleLog)
	// hot.go is small (50 LOC) but heavily churned; cold.go is large but calm.
	sizes := map[string]int{"hot.go": 50, "cold.go": 150}
	rep := Analyze(commits, sizes, nil)

	// churned = 10+2+20+5 = 37 ; deleted = 4+0+6+1 = 11 ; total LOC = 200
	m := rep.Measures
	wantM1 := 37.0 / 200.0
	if !close(m.M1, wantM1) {
		t.Errorf("M1 (churned/total) = %v, want %v", m.M1, wantM1)
	}
	wantM2 := 11.0 / 200.0
	if !close(m.M2, wantM2) {
		t.Errorf("M2 (deleted/total) = %v, want %v", m.M2, wantM2)
	}
	// files churned = 2 (hot.go, cold.go); file count = 2
	if !close(m.M3, 1.0) {
		t.Errorf("M3 (filesChurned/fileCount) = %v, want 1", m.M3)
	}
	// churn count = 4 file-touches / 2 files churned = 2
	if !close(m.M4, 2.0) {
		t.Errorf("M4 (churnCount/filesChurned) = %v, want 2", m.M4)
	}
	// weeks of churn = 2 distinct ISO weeks / file count 2 = 1
	if !close(m.M5, 1.0) {
		t.Errorf("M5 (weeksOfChurn/fileCount) = %v, want 1", m.M5)
	}
	// lines worked on = 37+11 = 48 ; / weeks 2 = 24
	if !close(m.M6, 24.0) {
		t.Errorf("M6 (linesWorkedOn/weeks) = %v, want 24", m.M6)
	}
	wantM7 := 37.0 / 11.0
	if !close(m.M7, wantM7) {
		t.Errorf("M7 (churned/deleted) = %v, want %v", m.M7, wantM7)
	}
	// lines worked on 48 / churn count 4 = 12
	if !close(m.M8, 12.0) {
		t.Errorf("M8 (linesWorkedOn/churnCount) = %v, want 12", m.M8)
	}
}

// TestAnalyze_RanksByRelativeNotAbsoluteChurn is the paper's central finding:
// absolute churn is a poor predictor, relative churn is highly predictive.
// big.go has MORE absolute churn but is huge; small.go churns less in absolute
// terms but almost entirely relative to its size — small.go must rank first.
func TestAnalyze_RanksByRelativeNotAbsoluteChurn(t *testing.T) {
	log := "c1|2026-02-02T10:00:00Z\n100\t0\tbig.go\n40\t0\tsmall.go\n"
	commits, _ := Parse(log)
	sizes := map[string]int{"big.go": 5000, "small.go": 50}

	rep := Analyze(commits, sizes, nil)
	if len(rep.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(rep.Files))
	}
	if rep.Files[0].Path != "small.go" {
		t.Errorf("relative churn must outrank absolute: got %q first (%.3f) vs %q (%.3f)",
			rep.Files[0].Path, rep.Files[0].RelativeChurn,
			rep.Files[1].Path, rep.Files[1].RelativeChurn)
	}
	if rep.Files[0].ChurnedLOC >= rep.Files[1].ChurnedLOC {
		t.Errorf("test setup broken: small.go should have LESS absolute churn")
	}
}

// TestAnalyze_RiskCombinesChurnWithComplexity is the Tornhill hotspot rule:
// the genuinely problematic code is complex code that changes often. Equal
// relative churn + higher complexity must rank higher.
func TestAnalyze_RiskCombinesChurnWithComplexity(t *testing.T) {
	log := "c1|2026-02-02T10:00:00Z\n10\t0\tsimple.go\n10\t0\tgnarly.go\n"
	commits, _ := Parse(log)
	sizes := map[string]int{"simple.go": 100, "gnarly.go": 100}
	complexity := map[string]int{"simple.go": 1, "gnarly.go": 25}

	rep := Analyze(commits, sizes, complexity)
	if rep.Files[0].Path != "gnarly.go" {
		t.Errorf("complex+churning file must rank first, got %q (risks: %v)",
			rep.Files[0].Path, risks(rep))
	}
	if rep.Files[0].RiskScore <= rep.Files[1].RiskScore {
		t.Errorf("risk must be ordered descending: %v", risks(rep))
	}
	if rep.Hotspot != "gnarly.go" {
		t.Errorf("Hotspot = %q, want gnarly.go", rep.Hotspot)
	}
}

// TestAnalyze_NoComplexityFallsBackToChurnOnly keeps the lens usable when the
// caller has no complexity data (churn alone still ranks).
func TestAnalyze_NoComplexityFallsBackToChurnOnly(t *testing.T) {
	commits, _ := Parse("c1|2026-02-02T10:00:00Z\n50\t0\ta.go\n1\t0\tb.go\n")
	rep := Analyze(commits, map[string]int{"a.go": 100, "b.go": 100}, nil)
	if rep.Files[0].Path != "a.go" {
		t.Errorf("without complexity, churn alone should rank: %v", risks(rep))
	}
}

func TestAnalyze_UnknownFileSizeIsSkippedNotZeroDivided(t *testing.T) {
	commits, _ := Parse("c1|2026-02-02T10:00:00Z\n10\t0\tghost.go\n")
	rep := Analyze(commits, map[string]int{}, nil) // ghost.go has no size
	for _, f := range rep.Files {
		if math.IsInf(f.RelativeChurn, 0) || math.IsNaN(f.RelativeChurn) {
			t.Errorf("division by zero leaked into %+v", f)
		}
	}
	if rep.Skipped != 1 {
		t.Errorf("file with unknown size should be counted as skipped, got %d", rep.Skipped)
	}
}

func TestAnalyze_EmptyInputs(t *testing.T) {
	rep := Analyze(nil, nil, nil)
	if len(rep.Files) != 0 || rep.Hotspot != "" {
		t.Errorf("empty input must yield an empty report, got %+v", rep)
	}
}

func close(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func risks(r Report) []string {
	out := []string{}
	for _, f := range r.Files {
		out = append(out, f.Path)
	}
	return out
}
