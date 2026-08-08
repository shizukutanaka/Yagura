package ownership

import (
	"math"
	"testing"

	"github.com/shizukutanaka/yagura/internal/churn"
)

// commit builds a churn.Commit touching paths, authored by author/email.
func commit(author, email string, paths ...string) churn.Commit {
	c := churn.Commit{Author: author, Email: email}
	for _, p := range paths {
		c.Files = append(c.Files, churn.FileChange{Path: p, Added: 1})
	}
	return c
}

// TestAnalyze_BirdMetrics reproduces the paper's own worked example shape:
// one dominant owner plus a long tail of minor contributors.
func TestAnalyze_BirdMetrics(t *testing.T) {
	var commits []churn.Commit
	// top owner: 6 of 10 commits => Ownership 0.6, a major contributor
	for i := 0; i < 6; i++ {
		commits = append(commits, commit("Top", "top@x", "a.go"))
	}
	// second major: 2 of 10 = 20% (>= 5%)
	for i := 0; i < 2; i++ {
		commits = append(commits, commit("Second", "second@x", "a.go"))
	}
	// two minors at 1/10 = 10%... that is still >= 5%, so use a wider base:
	// add 30 more Top commits so 1-commit contributors fall under 5%.
	for i := 0; i < 30; i++ {
		commits = append(commits, commit("Top", "top@x", "a.go"))
	}
	commits = append(commits, commit("MinorA", "a@x", "a.go"))
	commits = append(commits, commit("MinorB", "b@x", "a.go"))

	rep := Analyze(commits, nil)
	f := findFile(t, rep, "a.go")

	// total commits = 6+2+30+1+1 = 40 ; Top = 36 => 0.9
	if !closeTo(f.Ownership, 0.9) {
		t.Errorf("Ownership = %v, want 0.9", f.Ownership)
	}
	if f.TopOwner != "Top" {
		t.Errorf("TopOwner = %q, want Top", f.TopOwner)
	}
	if f.Total != 4 {
		t.Errorf("Total contributors = %d, want 4", f.Total)
	}
	// MinorA and MinorB are 1/40 = 2.5% < 5% => minor
	if f.Minor != 2 {
		t.Errorf("Minor = %d, want 2 (contributors under the 5%% threshold)", f.Minor)
	}
	// Top (90%) and Second (5%) are >= 5% => major
	if f.Major != 2 {
		t.Errorf("Major = %d, want 2", f.Major)
	}
	if f.Minor+f.Major != f.Total {
		t.Errorf("Minor+Major must equal Total: %d+%d != %d", f.Minor, f.Major, f.Total)
	}
}

// TestMinorThresholdIsFivePercent pins the paper's threshold explicitly so a
// future tweak cannot silently drift away from the cited research.
func TestMinorThresholdIsFivePercent(t *testing.T) {
	if MinorThreshold != 0.05 {
		t.Errorf("MinorThreshold = %v, want 0.05 (Bird et al., FSE 2011)", MinorThreshold)
	}
}

// TestAnalyze_RanksLowOwnershipFirst: the paper's finding is that LOW ownership
// (and many minor contributors) associates with more defects, so the riskiest
// file must sort first.
func TestAnalyze_RanksLowOwnershipFirst(t *testing.T) {
	var commits []churn.Commit
	// owned.go: single author => ownership 1.0 (safe)
	for i := 0; i < 10; i++ {
		commits = append(commits, commit("Solo", "solo@x", "owned.go"))
	}
	// shared.go: 10 different authors, 1 commit each => ownership 0.1 (risky)
	for i := 0; i < 10; i++ {
		name := string(rune('A' + i))
		commits = append(commits, commit(name, name+"@x", "shared.go"))
	}
	rep := Analyze(commits, nil)
	if rep.Files[0].Path != "shared.go" {
		t.Errorf("low-ownership file must rank first, got %q", rep.Files[0].Path)
	}
	if rep.Riskiest != "shared.go" {
		t.Errorf("Riskiest = %q, want shared.go", rep.Riskiest)
	}
}

// TestAnalyze_AIAuthorship is the 2026 extension (NOT from the paper): report
// what share of a file's commits came from AI agents, and the top *human*
// owner separately.
func TestAnalyze_AIAuthorship(t *testing.T) {
	commits := []churn.Commit{
		commit("Claude", "noreply@anthropic.com", "ai.go"),
		commit("Claude", "noreply@anthropic.com", "ai.go"),
		commit("Claude", "noreply@anthropic.com", "ai.go"),
		commit("Human", "dev@example.com", "ai.go"),
	}
	rep := Analyze(commits, nil)
	f := findFile(t, rep, "ai.go")
	if !closeTo(f.AIProportion, 0.75) {
		t.Errorf("AIProportion = %v, want 0.75", f.AIProportion)
	}
	if f.TopHumanOwner != "Human" {
		t.Errorf("TopHumanOwner = %q, want Human", f.TopHumanOwner)
	}
	if !closeTo(f.HumanOwnership, 0.25) {
		t.Errorf("HumanOwnership = %v, want 0.25", f.HumanOwnership)
	}
}

// TestAnalyze_FullyAIAuthoredHasNoHumanOwner: a file no human ever touched must
// report zero human ownership rather than defaulting to the AI as "the owner".
func TestAnalyze_FullyAIAuthoredHasNoHumanOwner(t *testing.T) {
	commits := []churn.Commit{
		commit("Claude", "noreply@anthropic.com", "pure.go"),
		commit("Devin AI", "devin-ai-integration[bot]@users.noreply.github.com", "pure.go"),
	}
	rep := Analyze(commits, nil)
	f := findFile(t, rep, "pure.go")
	if !closeTo(f.AIProportion, 1.0) {
		t.Errorf("AIProportion = %v, want 1.0", f.AIProportion)
	}
	if f.HumanOwnership != 0 || f.TopHumanOwner != "" {
		t.Errorf("no human touched the file; got owner=%q ownership=%v", f.TopHumanOwner, f.HumanOwnership)
	}
}

func TestIsAIAuthor(t *testing.T) {
	cases := []struct {
		name, email string
		want        bool
	}{
		{"Claude", "noreply@anthropic.com", true},
		{"Devin AI", "devin-ai-integration[bot]@users.noreply.github.com", true},
		{"dependabot[bot]", "support@github.com", true},
		{"Shizuku Tanaka", "app.shizukutanaka@gmail.com", false},
		{"yagura", "irosai.ume@gmail.com", false},
	}
	for _, c := range cases {
		if got := IsAIAuthor(c.name, c.email); got != c.want {
			t.Errorf("IsAIAuthor(%q,%q) = %v, want %v", c.name, c.email, got, c.want)
		}
	}
}

// TestAnalyze_FilterRestrictsToKnownFiles keeps deleted/vendored paths out.
func TestAnalyze_FilterRestrictsToKnownFiles(t *testing.T) {
	commits := []churn.Commit{commit("A", "a@x", "keep.go", "gone.go")}
	rep := Analyze(commits, map[string]bool{"keep.go": true})
	if len(rep.Files) != 1 || rep.Files[0].Path != "keep.go" {
		t.Errorf("filter ignored: %+v", rep.Files)
	}
}

func TestAnalyze_Empty(t *testing.T) {
	rep := Analyze(nil, nil)
	if len(rep.Files) != 0 || rep.Riskiest != "" {
		t.Errorf("empty input must give empty report, got %+v", rep)
	}
}

func findFile(t *testing.T, r Report, path string) FileOwnership {
	t.Helper()
	for _, f := range r.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("file %q not in report %+v", path, r.Files)
	return FileOwnership{}
}

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
