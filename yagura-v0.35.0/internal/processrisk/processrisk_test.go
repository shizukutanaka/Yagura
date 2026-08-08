package processrisk

import (
	"math"
	"testing"

	"github.com/shizukutanaka/yagura/internal/churn"
	"github.com/shizukutanaka/yagura/internal/ownership"
)

func chFile(path string, rel float64, count int) churn.FileRisk {
	return churn.FileRisk{Path: path, RelativeChurn: rel, ChurnCount: count, SizeLOC: 100}
}

func ownFile(path string, own float64, minor, total int) ownership.FileOwnership {
	return ownership.FileOwnership{Path: path, Ownership: own, Minor: minor, Total: total}
}

// TestScore_ProcessSignalsDominate is the core research claim (Majumder/Mody/
// Menzies EMSE 2022, replicating Rahman & Devanbu ICSE 2013): product metrics
// like complexity are near-random (AUC ~54%) while process metrics are strong
// (AUC ~95%). A file that is calm and well-owned must NOT outrank a churning,
// poorly-owned file just because it is complex.
func TestScore_ProcessSignalsDominate(t *testing.T) {
	ch := []churn.FileRisk{
		chFile("calm_but_complex.go", 0.01, 1),
		chFile("churning_shared.go", 5.0, 40),
	}
	// give the calm file huge complexity, the risky file none
	ch[0].Complexity = 90
	ch[1].Complexity = 1

	own := []ownership.FileOwnership{
		ownFile("calm_but_complex.go", 1.0, 0, 1),
		ownFile("churning_shared.go", 0.2, 6, 8),
	}
	rep := Score(ch, own)
	if rep.Files[0].Path != "churning_shared.go" {
		t.Errorf("process signals must dominate product metrics: got %q first (%v)",
			rep.Files[0].Path, paths(rep))
	}
}

// TestScore_ComplexityIsReportedButNotScored: complexity must be visible for
// human judgement, yet contribute nothing to the ranking — otherwise we would
// be re-importing the near-random signal the literature warns about.
func TestScore_ComplexityIsReportedButNotScored(t *testing.T) {
	base := []churn.FileRisk{chFile("a.go", 1.0, 5), chFile("b.go", 1.0, 5)}
	own := []ownership.FileOwnership{ownFile("a.go", 0.5, 1, 2), ownFile("b.go", 0.5, 1, 2)}

	low := Score(withComplexity(base, map[string]int{"a.go": 1, "b.go": 1}), own)
	high := Score(withComplexity(base, map[string]int{"a.go": 99, "b.go": 99}), own)

	if !closeTo(low.Files[0].Score, high.Files[0].Score) {
		t.Errorf("complexity changed the score (%v vs %v) — it must not participate",
			low.Files[0].Score, high.Files[0].Score)
	}
	// but it must still be surfaced
	found := false
	for _, f := range high.Files {
		if f.Complexity == 99 {
			found = true
		}
	}
	if !found {
		t.Errorf("complexity must still be reported for human judgement")
	}
}

// TestScore_RankNormalizationAvoidsArbitraryScales: signals are on wildly
// different scales (churn ratio ~0-6, contributor counts 1-20). Percentile
// ranking keeps any one signal from swamping the others by unit choice alone.
func TestScore_RankNormalizationAvoidsArbitraryScales(t *testing.T) {
	ch := []churn.FileRisk{
		chFile("huge_churn.go", 1000.0, 2), // absurd scale on one signal only
		chFile("many_minors.go", 0.5, 2),
	}
	own := []ownership.FileOwnership{
		ownFile("huge_churn.go", 1.0, 0, 1),
		ownFile("many_minors.go", 0.1, 9, 10), // worst on the ownership signals
	}
	rep := Score(ch, own)
	for _, f := range rep.Files {
		if f.Score < 0 || f.Score > 1 {
			t.Errorf("score must be normalized to [0,1], got %v for %s", f.Score, f.Path)
		}
	}
	// the file that is worst on 3 of 4 process signals should win over the one
	// that is extreme on a single signal
	if rep.Files[0].Path != "many_minors.go" {
		t.Errorf("rank normalization failed; a single extreme signal dominated: %v", paths(rep))
	}
}

// TestScore_ReasonsExplainTheRanking keeps the output actionable rather than
// an unexplained number.
func TestScore_ReasonsExplainTheRanking(t *testing.T) {
	ch := []churn.FileRisk{chFile("bad.go", 9.0, 50), chFile("ok.go", 0.1, 1)}
	own := []ownership.FileOwnership{ownFile("bad.go", 0.15, 8, 10), ownFile("ok.go", 1.0, 0, 1)}
	rep := Score(ch, own)
	if len(rep.Files[0].Reasons) == 0 {
		t.Fatalf("top-ranked file must carry reasons")
	}
}

// TestScore_MissingOwnershipDataStillRanks: churn without ownership (e.g. a
// single-author repo) must not crash or silently zero the file out.
func TestScore_MissingOwnershipDataStillRanks(t *testing.T) {
	ch := []churn.FileRisk{chFile("only_churn.go", 2.0, 10)}
	rep := Score(ch, nil)
	if len(rep.Files) != 1 || rep.Files[0].Path != "only_churn.go" {
		t.Fatalf("file with churn but no ownership data was dropped: %+v", rep.Files)
	}
	if rep.Files[0].HasOwnership {
		t.Errorf("HasOwnership must be false when no ownership record exists")
	}
}

func TestScore_Empty(t *testing.T) {
	rep := Score(nil, nil)
	if len(rep.Files) != 0 || rep.Riskiest != "" {
		t.Errorf("empty input must give an empty report: %+v", rep)
	}
}

// TestScore_Deterministic pins stable ordering for equal scores.
func TestScore_Deterministic(t *testing.T) {
	ch := []churn.FileRisk{chFile("zzz.go", 1.0, 5), chFile("aaa.go", 1.0, 5)}
	own := []ownership.FileOwnership{ownFile("zzz.go", 0.5, 1, 2), ownFile("aaa.go", 0.5, 1, 2)}
	a, b := Score(ch, own), Score(ch, own)
	if a.Files[0].Path != b.Files[0].Path {
		t.Fatal("ranking is not deterministic")
	}
	if a.Files[0].Path != "aaa.go" {
		t.Errorf("ties must break by path ascending, got %v", paths(a))
	}
}

func withComplexity(in []churn.FileRisk, cx map[string]int) []churn.FileRisk {
	out := make([]churn.FileRisk, len(in))
	copy(out, in)
	for i := range out {
		out[i].Complexity = cx[out[i].Path]
	}
	return out
}

func paths(r Report) []string {
	out := []string{}
	for _, f := range r.Files {
		out = append(out, f.Path)
	}
	return out
}

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
