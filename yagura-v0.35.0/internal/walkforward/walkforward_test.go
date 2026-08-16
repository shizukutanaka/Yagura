package walkforward

import (
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/churn"
	"github.com/shizukutanaka/yagura/internal/defectdataset"
)

func at(day int) time.Time { return time.Date(2026, 1, day, 12, 0, 0, 0, time.UTC) }

func commit(day int, subject string, paths ...string) churn.Commit {
	c := churn.Commit{When: at(day), Subject: subject, Author: "Dev", Email: "dev@x"}
	for _, p := range paths {
		c.Files = append(c.Files, churn.FileChange{Path: p, Added: 10, Deleted: 2})
	}
	return c
}

func sizes(paths ...string) map[string]int {
	m := map[string]int{}
	for _, p := range paths {
		m[p] = 100
	}
	return m
}

// history builds a stream where hot.go is churned early and fixed late — a
// signal a time-aware evaluator should be able to pick up.
func history() ([]churn.Commit, map[string]int) {
	var cs []churn.Commit
	for d := 1; d <= 12; d++ {
		switch {
		case d%3 == 0:
			cs = append(cs, commit(d, "fix: repair hot", "hot.go"))
		default:
			cs = append(cs, commit(d, "feat: work on hot", "hot.go", "cold.go"))
		}
	}
	return cs, sizes("hot.go", "cold.go")
}

// TestRun_PreservesOrder is the central guard. Falessi et al. (EMSE 2020) show
// that validation which ignores temporal order gives materially different (and
// misleading) numbers. Every fold must train strictly on commits that precede
// its own label window.
func TestRun_PreservesOrder(t *testing.T) {
	cs, sz := history()
	rep := Run(cs, sz, nil, Options{Folds: 3})
	if len(rep.Folds) == 0 {
		t.Fatal("expected folds")
	}
	for i, f := range rep.Folds {
		if f.FeatureEnd.After(f.LabelStart) {
			t.Errorf("fold %d leaks: feature window ends %v, after label window starts %v",
				i, f.FeatureEnd, f.LabelStart)
		}
		if f.FeatureCommits == 0 || f.LabelCommits == 0 {
			t.Errorf("fold %d has an empty window: feat=%d label=%d", i, f.FeatureCommits, f.LabelCommits)
		}
	}
	// windows must march forward, never revisit
	for i := 1; i < len(rep.Folds); i++ {
		if !rep.Folds[i].LabelStart.After(rep.Folds[i-1].LabelStart) {
			t.Errorf("fold %d label window did not advance past fold %d", i, i-1)
		}
	}
}

// TestRun_InvertedScorerLosesToBaseline: an evaluator that cannot fail proves
// nothing. Ranking by the *negation* of a real signal must not beat random.
func TestRun_InvertedScorerLosesToBaseline(t *testing.T) {
	cs, sz := history()
	good := Scorer{Name: "churn_count", Of: func(r defectdataset.Row) float64 { return float64(r.ChurnCount) }}
	bad := Scorer{Name: "inverted", Of: func(r defectdataset.Row) float64 { return -float64(r.ChurnCount) }}
	rep := Run(cs, sz, nil, Options{Folds: 3, Scorers: []Scorer{good, bad}})

	g, okg := rep.PerScorer["churn_count"]
	b, okb := rep.PerScorer["inverted"]
	if !okg || !okb {
		t.Fatalf("both scorers must be reported, got %v", rep.PerScorer)
	}
	if b.MeanLift > g.MeanLift {
		t.Errorf("an inverted scorer beat the real one: inverted=%.2f good=%.2f", b.MeanLift, g.MeanLift)
	}
}

// TestRun_FoldsWithoutPositivesAreSkipped: a fold whose label window contains
// no fixes cannot score anything; averaging a fabricated 0 into the result
// would understate the signal, so such folds are excluded and counted.
func TestRun_FoldsWithoutPositivesAreSkipped(t *testing.T) {
	var cs []churn.Commit
	for d := 1; d <= 9; d++ {
		cs = append(cs, commit(d, "feat: add another feature", "a.go")) // deliberately no fix keyword
	}
	rep := Run(cs, sizes("a.go"), nil, Options{Folds: 3})
	if rep.Valid {
		t.Errorf("with no fixes in any label window the run cannot be valid: %+v", rep)
	}
	if rep.SkippedFolds == 0 {
		t.Errorf("folds without positives must be counted as skipped")
	}
	if rep.Note == "" {
		t.Errorf("an invalid run must explain itself")
	}
}

// TestRun_ClampsFolds keeps tiny histories from producing degenerate windows.
func TestRun_ClampsFolds(t *testing.T) {
	cs := []churn.Commit{commit(1, "feat", "a.go"), commit(2, "fix: a", "a.go")}
	rep := Run(cs, sizes("a.go"), nil, Options{Folds: 50})
	for _, f := range rep.Folds {
		if f.FeatureCommits == 0 || f.LabelCommits == 0 {
			t.Errorf("degenerate fold produced: %+v", f)
		}
	}
}

func TestRun_Deterministic(t *testing.T) {
	cs, sz := history()
	a := Run(cs, sz, nil, Options{Folds: 3})
	b := Run(cs, sz, nil, Options{Folds: 3})
	if len(a.Folds) != len(b.Folds) {
		t.Fatalf("fold count differs across runs: %d vs %d", len(a.Folds), len(b.Folds))
	}
	if a.Best != b.Best {
		t.Errorf("Best differs across runs: %q vs %q", a.Best, b.Best)
	}
	for name, x := range a.PerScorer {
		if y := b.PerScorer[name]; y.MeanPrecision != x.MeanPrecision {
			t.Errorf("scorer %s not deterministic: %v vs %v", name, x.MeanPrecision, y.MeanPrecision)
		}
	}
}

// TestRun_DefaultScorersCoverBothMetricFamilies: the point of comparing under
// one protocol is to see process vs product side by side.
func TestRun_DefaultScorersIncludeProcessAndProduct(t *testing.T) {
	cs, sz := history()
	rep := Run(cs, sz, map[string]int{"hot.go": 20, "cold.go": 1}, Options{Folds: 3})
	for _, want := range []string{"relative_churn", "churn_count", "complexity"} {
		if _, ok := rep.PerScorer[want]; !ok {
			t.Errorf("default scorers must include %q, got %v", want, keys(rep.PerScorer))
		}
	}
}

func TestRun_Empty(t *testing.T) {
	rep := Run(nil, nil, nil, Options{Folds: 3})
	if rep.Valid {
		t.Errorf("empty history cannot be valid")
	}
	if len(rep.Folds) != 0 {
		t.Errorf("empty history must produce no folds")
	}
}

func keys(m map[string]ScorerResult) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
