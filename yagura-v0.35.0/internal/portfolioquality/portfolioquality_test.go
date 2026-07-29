package portfolioquality

import (
	"errors"
	"testing"

	"github.com/shizukutanaka/yagura/internal/srcfiles"
)

// fakeReader serves canned trees keyed by dir, so tests need no real filesystem.
func fakeReader(trees map[string]srcfiles.Result, failing map[string]error) ReadFunc {
	return func(dir string) (srcfiles.Result, error) {
		if err, bad := failing[dir]; bad {
			return srcfiles.Result{}, err
		}
		return trees[dir], nil
	}
}

// clean is a tiny well-formed package; messy has an undocumented exported API
// plus a high-complexity function, so it must grade worse than clean.
const cleanSrc = `package a

// Add returns the sum of x and y.
func Add(x, y int) int { return x + y }
`

const messySrc = `package b

func Tangled(n int) int {
	if n > 1 { if n > 2 { if n > 3 { if n > 4 { if n > 5 { return 5 } } } } }
	for i := 0; i < n; i++ { if i%2 == 0 { n++ } else if i%3 == 0 { n-- } }
	switch n { case 1: return 1; case 2: return 2; case 3: return 3 }
	return n
}

func Undocumented(a, b, c, d, e, f int) (int, int, int, int) { return a, b, c, d }
`

func TestRank_WorstFirst(t *testing.T) {
	projects := []Project{
		{Slug: "cleanproj", LocalPath: "/clean"},
		{Slug: "messyproj", LocalPath: "/messy"},
	}
	read := fakeReader(map[string]srcfiles.Result{
		"/clean": {Files: map[string]string{"a.go": cleanSrc}},
		"/messy": {Files: map[string]string{"b.go": messySrc}},
	}, nil)

	rep := Rank(projects, read)
	if len(rep.Projects) != 2 {
		t.Fatalf("expected 2 scanned projects, got %d (%+v)", len(rep.Projects), rep)
	}
	// worst-first: the messy project must rank ahead of the clean one
	if rep.Projects[0].Slug != "messyproj" {
		t.Errorf("worst-first violated: got %q first, want messyproj (scores: %v)",
			rep.Projects[0].Slug, scores(rep))
	}
	if rep.Projects[0].Score > rep.Projects[1].Score {
		t.Errorf("ranking must be ascending by score (worst=lowest first): %v", scores(rep))
	}
	if rep.Projects[0].Grade == "" {
		t.Errorf("grade must be populated")
	}
}

func TestRank_ReportsUnscannableWithoutLocalPath(t *testing.T) {
	projects := []Project{
		{Slug: "haspath", LocalPath: "/clean"},
		{Slug: "nopath"}, // no LocalPath → cannot scan
	}
	read := fakeReader(map[string]srcfiles.Result{
		"/clean": {Files: map[string]string{"a.go": cleanSrc}},
	}, nil)

	rep := Rank(projects, read)
	if len(rep.Projects) != 1 {
		t.Errorf("only the project with a LocalPath is scannable, got %d", len(rep.Projects))
	}
	if len(rep.Unscannable) != 1 || rep.Unscannable[0].Slug != "nopath" {
		t.Fatalf("nopath must be reported as unscannable, got %+v", rep.Unscannable)
	}
	if rep.Unscannable[0].Reason == "" {
		t.Errorf("unscannable entries must carry a reason (never silently dropped)")
	}
}

func TestRank_ReportsReadFailureAsUnscannable(t *testing.T) {
	projects := []Project{{Slug: "broken", LocalPath: "/gone"}}
	read := fakeReader(nil, map[string]error{"/gone": errors.New("no such directory")})

	rep := Rank(projects, read)
	if len(rep.Projects) != 0 {
		t.Errorf("a failed read must not produce a ranked project")
	}
	if len(rep.Unscannable) != 1 || rep.Unscannable[0].Slug != "broken" {
		t.Fatalf("read failure must surface as unscannable, got %+v", rep.Unscannable)
	}
}

// TestRank_PropagatesIncompleteScan proves a truncated walk is flagged rather
// than being silently reported as a complete (clean) result — fail-open guard.
func TestRank_PropagatesIncompleteScan(t *testing.T) {
	projects := []Project{{Slug: "big", LocalPath: "/big"}}
	read := fakeReader(map[string]srcfiles.Result{
		"/big": {Files: map[string]string{"a.go": cleanSrc}, Truncated: true},
	}, nil)

	rep := Rank(projects, read)
	if len(rep.Projects) != 1 {
		t.Fatalf("truncated scan should still rank, got %d", len(rep.Projects))
	}
	if !rep.Projects[0].Incomplete {
		t.Errorf("truncated scan must set Incomplete so a partial scan is not read as clean")
	}
}

func TestRank_EmptyRegistry(t *testing.T) {
	rep := Rank(nil, fakeReader(nil, nil))
	if len(rep.Projects) != 0 || len(rep.Unscannable) != 0 {
		t.Errorf("empty registry must produce an empty report, got %+v", rep)
	}
	if rep.Scanned != 0 {
		t.Errorf("Scanned should be 0, got %d", rep.Scanned)
	}
}

// TestRank_DeterministicTieBreak pins slug-ascending order for equal scores.
func TestRank_DeterministicTieBreak(t *testing.T) {
	projects := []Project{
		{Slug: "zzz", LocalPath: "/a"},
		{Slug: "aaa", LocalPath: "/b"},
	}
	read := fakeReader(map[string]srcfiles.Result{
		"/a": {Files: map[string]string{"a.go": cleanSrc}},
		"/b": {Files: map[string]string{"a.go": cleanSrc}},
	}, nil)

	first := Rank(projects, read)
	second := Rank(projects, read)
	if first.Projects[0].Slug != second.Projects[0].Slug {
		t.Fatalf("ranking is not deterministic across runs")
	}
	if first.Projects[0].Score == first.Projects[1].Score && first.Projects[0].Slug != "aaa" {
		t.Errorf("equal scores must tie-break by slug ascending, got %v", scores(first))
	}
}

func scores(r Report) []string {
	out := make([]string, 0, len(r.Projects))
	for _, p := range r.Projects {
		out = append(out, p.Slug+"="+p.Grade)
	}
	return out
}
