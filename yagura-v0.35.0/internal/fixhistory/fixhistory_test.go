package fixhistory

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/churn"
)

func TestIsFixCommit_RecognizesFixes(t *testing.T) {
	fixes := []string{
		"fix: nil deref in parser",
		"Fix crash when config is empty",
		"fix(mcp): stale handshake version",
		"bugfix: off-by-one in ranking",
		"Resolve panic on shutdown",
		"correct the churn denominator",
		"hotfix for release blocker",
		"patch: guard against zero division",
	}
	for _, s := range fixes {
		if !IsFixCommit(s) {
			t.Errorf("IsFixCommit(%q) = false, want true", s)
		}
	}
}

// TestIsFixCommit_AvoidsWordFragments guards the classic weakness of
// message-based fix identification: substring matching turns "prefix",
// "suffix" and "fixture" into false fixes and quietly corrupts the ground
// truth this package exists to provide.
func TestIsFixCommit_AvoidsWordFragments(t *testing.T) {
	notFixes := []string{
		"add prefix to generated ids",
		"rename suffix handling",
		"add test fixtures for registry",
		"refactor: extract helper",
		"feat: add ownership metrics",
		"docs: update CHANGELOG",
		"chore: bump version",
	}
	for _, s := range notFixes {
		if IsFixCommit(s) {
			t.Errorf("IsFixCommit(%q) = true, want false (word-fragment false positive)", s)
		}
	}
}

// TestIsFixCommit_IgnoresRevertAndMerge keeps bookkeeping commits out.
func TestIsFixCommit_IgnoresRevertAndMerge(t *testing.T) {
	for _, s := range []string{`Revert "fix: something"`, "Merge branch 'fix/foo'"} {
		if IsFixCommit(s) {
			t.Errorf("IsFixCommit(%q) = true, want false", s)
		}
	}
}

func commitWith(subject string, paths ...string) churn.Commit {
	c := churn.Commit{Subject: subject}
	for _, p := range paths {
		c.Files = append(c.Files, churn.FileChange{Path: p, Added: 1})
	}
	return c
}

func TestAnalyze_CountsFixesPerFile(t *testing.T) {
	commits := []churn.Commit{
		commitWith("fix: bad parse", "a.go", "b.go"),
		commitWith("feat: new thing", "a.go"),
		commitWith("fix: another", "a.go"),
	}
	rep := Analyze(commits)
	if rep.FixCommits != 2 {
		t.Errorf("FixCommits = %d, want 2", rep.FixCommits)
	}
	if rep.TotalCommits != 3 {
		t.Errorf("TotalCommits = %d, want 3", rep.TotalCommits)
	}
	if rep.FixesByFile["a.go"] != 2 {
		t.Errorf("a.go fixes = %d, want 2", rep.FixesByFile["a.go"])
	}
	if rep.FixesByFile["b.go"] != 1 {
		t.Errorf("b.go fixes = %d, want 1", rep.FixesByFile["b.go"])
	}
	if rep.MostFixed != "a.go" {
		t.Errorf("MostFixed = %q, want a.go", rep.MostFixed)
	}
}

// TestValidate_PerfectRankingScoresHigh: if the risk ranking names exactly the
// most-fixed files, precision@K must be 1.
func TestValidate_PerfectRankingScoresHigh(t *testing.T) {
	fixes := map[string]int{"hot.go": 10, "warm.go": 5, "cold.go": 0, "icy.go": 0}
	ranking := []string{"hot.go", "warm.go", "cold.go", "icy.go"}
	v := Validate(ranking, fixes, 2)
	if v.PrecisionAtK != 1.0 {
		t.Errorf("precision@2 = %v, want 1.0 (%+v)", v.PrecisionAtK, v)
	}
	if v.Hits != 2 {
		t.Errorf("Hits = %d, want 2", v.Hits)
	}
}

// TestValidate_InvertedRankingScoresLow proves the metric can actually fail —
// a validator that always reports success validates nothing.
func TestValidate_InvertedRankingScoresLow(t *testing.T) {
	fixes := map[string]int{"hot.go": 10, "warm.go": 5, "cold.go": 0, "icy.go": 0}
	inverted := []string{"icy.go", "cold.go", "warm.go", "hot.go"}
	v := Validate(inverted, fixes, 2)
	if v.PrecisionAtK != 0.0 {
		t.Errorf("precision@2 for an inverted ranking = %v, want 0", v.PrecisionAtK)
	}
}

// TestValidate_ReportsBaseline: precision@K is meaningless without the rate a
// random ranking would achieve, so the baseline must be reported alongside.
func TestValidate_ReportsBaseline(t *testing.T) {
	fixes := map[string]int{"a.go": 3, "b.go": 0, "c.go": 0, "d.go": 0}
	v := Validate([]string{"a.go", "b.go", "c.go", "d.go"}, fixes, 1)
	// 1 of 4 files was ever fixed → a random pick lands on it 25% of the time
	if v.BaselinePrecision <= 0 || v.BaselinePrecision > 1 {
		t.Fatalf("baseline out of range: %v", v.BaselinePrecision)
	}
	if v.BaselinePrecision != 0.25 {
		t.Errorf("baseline = %v, want 0.25", v.BaselinePrecision)
	}
	if v.Lift <= 1.0 {
		t.Errorf("a perfect top-1 pick should beat the baseline, lift = %v", v.Lift)
	}
}

// TestValidate_NoFixesIsHonest: a repo with no identifiable fix commits cannot
// validate anything, and must say so rather than reporting a fake score.
func TestValidate_NoFixesIsHonest(t *testing.T) {
	v := Validate([]string{"a.go", "b.go"}, map[string]int{}, 2)
	if v.Valid {
		t.Errorf("validation must be marked invalid with no fix data: %+v", v)
	}
	if v.Note == "" {
		t.Errorf("must explain why validation is unavailable")
	}
}

func TestValidate_KLargerThanRanking(t *testing.T) {
	fixes := map[string]int{"a.go": 1}
	v := Validate([]string{"a.go"}, fixes, 10)
	if v.K != 1 {
		t.Errorf("K must clamp to the ranking length, got %d", v.K)
	}
}

func TestAnalyze_Empty(t *testing.T) {
	rep := Analyze(nil)
	if rep.FixCommits != 0 || rep.MostFixed != "" {
		t.Errorf("empty input must give an empty report: %+v", rep)
	}
}
