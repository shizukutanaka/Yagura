package project

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProject_Validate(t *testing.T) {
	cases := []struct {
		name      string
		p         *Project
		wantError bool
		errSubstr string
	}{
		{"valid minimal", &Project{Slug: "mihari", DisplayName: "M", Repository: "shizukutanaka/mihari"}, false, ""},
		{"valid full", &Project{
			Slug: "yagura", DisplayName: "Yagura", Repository: "github.com/shizukutanaka/yagura",
			LocalPath: "/home/m/projects/yagura", Language: "Go",
			Tags: []string{"daemon"}, Stage: StageActive, Priority: 5,
		}, false, ""},

		{"empty slug", &Project{DisplayName: "X", Repository: "a/b"}, true, "slug"},
		{"uppercase slug", &Project{Slug: "Mihari", DisplayName: "X", Repository: "a/b"}, true, "slug"},
		{"hyphen start", &Project{Slug: "-bad", DisplayName: "X", Repository: "a/b"}, true, "slug"},
		{"path traversal", &Project{Slug: "../etc", DisplayName: "X", Repository: "a/b"}, true, "slug"},
		{"slug with slash", &Project{Slug: "a/b", DisplayName: "X", Repository: "a/b"}, true, "slug"},
		{"slug too long", &Project{Slug: strings.Repeat("a", 51), DisplayName: "X", Repository: "a/b"}, true, "slug"},
		{"empty display_name", &Project{Slug: "x", Repository: "a/b"}, true, "display_name"},
		{"empty repository", &Project{Slug: "x", DisplayName: "X"}, true, "repository"},
		{"bad repo format", &Project{Slug: "x", DisplayName: "X", Repository: "noslash"}, true, "repository"},
		{"unknown stage", &Project{Slug: "x", DisplayName: "X", Repository: "a/b", Stage: "?"}, true, "stage"},
		{"priority negative", &Project{Slug: "x", DisplayName: "X", Repository: "a/b", Priority: -1}, true, "priority"},
		{"priority too high", &Project{Slug: "x", DisplayName: "X", Repository: "a/b", Priority: 6}, true, "priority"},
		{"local_path traversal", &Project{Slug: "x", DisplayName: "X", Repository: "a/b", LocalPath: "../../etc"}, true, "local_path"},
		{"invalid phase", &Project{Slug: "x", DisplayName: "X", Repository: "a/b",
			Sprint: &Sprint{Phase: "?"}}, true, "phase"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.p.Validate()
			if (err != nil) != c.wantError {
				t.Errorf("wantError=%v got %v (err=%v)", c.wantError, err != nil, err)
			}
			if c.wantError && c.errSubstr != "" && err != nil &&
				!strings.Contains(err.Error(), c.errSubstr) {
				t.Errorf("err should contain %q, got %q", c.errSubstr, err)
			}
		})
	}
}

func TestProject_Validate_DefaultsStage(t *testing.T) {
	p := &Project{Slug: "x", DisplayName: "X", Repository: "a/b"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Stage != StageActive {
		t.Errorf("default Stage should be active, got %q", p.Stage)
	}
}

func TestProject_Validate_UsesErrorsJoin(t *testing.T) {
	p := &Project{Slug: "BAD"}
	err := p.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	type unwrapper interface{ Unwrap() []error }
	if _, ok := err.(unwrapper); !ok {
		t.Errorf("Validate should return errors.Join-style error, got %T", err)
	}
	_ = errors.Is(err, errors.New("dummy")) // compile check
}

func TestProject_OwnerRepo(t *testing.T) {
	cases := []struct {
		repo, wantOwner, wantRepo string
	}{
		{"shizukutanaka/mihari", "shizukutanaka", "mihari"},
		{"github.com/shizukutanaka/mihari", "shizukutanaka", "mihari"},
		{"", "", ""},
		{"single", "", ""},
		{"a/b/c", "a", "b/c"},
	}
	for _, c := range cases {
		p := &Project{Repository: c.repo}
		o, r := p.OwnerRepo()
		if o != c.wantOwner || r != c.wantRepo {
			t.Errorf("OwnerRepo(%q) = (%q, %q), want (%q, %q)",
				c.repo, o, r, c.wantOwner, c.wantRepo)
		}
	}
}

func TestProject_IsActive(t *testing.T) {
	cases := []struct {
		stage  Stage
		active bool
	}{
		{StageActive, true}, {StageMaintenance, false},
		{StagePaused, false}, {StageArchived, false}, {Stage(""), false},
	}
	for _, c := range cases {
		p := &Project{Stage: c.stage}
		if got := p.IsActive(); got != c.active {
			t.Errorf("Stage=%q IsActive()=%v want %v", c.stage, got, c.active)
		}
	}
}

func TestProject_IsScannable(t *testing.T) {
	cases := []struct {
		stage     Stage
		scannable bool
	}{
		{StageActive, true}, {StageMaintenance, true},
		{StagePaused, false}, {StageArchived, false},
	}
	for _, c := range cases {
		p := &Project{Stage: c.stage}
		if got := p.IsScannable(); got != c.scannable {
			t.Errorf("Stage=%q IsScannable()=%v want %v", c.stage, got, c.scannable)
		}
	}
}

func TestProject_StaleAge(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	p := &Project{}
	if age := p.StaleAge(now); age != 0 {
		t.Errorf("zero activity should give 0, got %v", age)
	}
	p.LatestActivity = now.AddDate(0, 0, -30)
	if age := p.StaleAge(now); age < 29*24*time.Hour || age > 31*24*time.Hour {
		t.Errorf("expected ~30 days, got %v", age)
	}
}

func TestProject_HasTag(t *testing.T) {
	p := &Project{Tags: []string{"daemon", "Go", "MCP"}}
	cases := []struct {
		tag string
		has bool
	}{
		{"daemon", true}, {"go", true}, {"MCP", true}, {"GO", true},
		{"rust", false}, {"", false},
	}
	for _, c := range cases {
		if got := p.HasTag(c.tag); got != c.has {
			t.Errorf("HasTag(%q) = %v, want %v", c.tag, got, c.has)
		}
	}
}

func TestNextPhase(t *testing.T) {
	cases := []struct {
		cur, next SprintPhase
	}{
		{PhaseThink, PhasePlan}, {PhasePlan, PhaseBuild},
		{PhaseBuild, PhaseReview}, {PhaseReview, PhaseTest},
		{PhaseTest, PhaseShip}, {PhaseShip, PhaseReflect},
		{PhaseReflect, PhaseThink}, {SprintPhase("unknown"), PhaseThink},
	}
	for _, c := range cases {
		if got := NextPhase(c.cur); got != c.next {
			t.Errorf("NextPhase(%q) = %q, want %q", c.cur, got, c.next)
		}
	}
}

func TestSortBySlug(t *testing.T) {
	ps := []*Project{{Slug: "zebra"}, {Slug: "alpha"}, {Slug: "mihari"}}
	SortBySlug(ps)
	want := []string{"alpha", "mihari", "zebra"}
	for i, p := range ps {
		if p.Slug != want[i] {
			t.Errorf("[%d] got %q want %q", i, p.Slug, want[i])
		}
	}
}

func TestSortByActivity(t *testing.T) {
	now := time.Now()
	ps := []*Project{
		{Slug: "old", LatestActivity: now.AddDate(0, 0, -30)},
		{Slug: "new", LatestActivity: now},
		{Slug: "mid", LatestActivity: now.AddDate(0, 0, -7)},
	}
	SortByActivity(ps)
	want := []string{"new", "mid", "old"}
	for i, p := range ps {
		if p.Slug != want[i] {
			t.Errorf("[%d] got %q want %q", i, p.Slug, want[i])
		}
	}
}

func TestSortByActivity_StableForTiedTimes(t *testing.T) {
	t0 := time.Now()
	ps := []*Project{
		{Slug: "zebra", LatestActivity: t0},
		{Slug: "alpha", LatestActivity: t0},
	}
	SortByActivity(ps)
	if ps[0].Slug != "alpha" {
		t.Errorf("tied times should sort by slug, got %s, %s", ps[0].Slug, ps[1].Slug)
	}
}

// ─── TotalVulns / HasCriticalSecurityIssue ───────────────────

func TestTotalVulns(t *testing.T) {
	p := Project{
		VulnCritical: 1,
		VulnHigh:     2,
		VulnMedium:   3,
		VulnLow:      4,
	}
	if got := p.TotalVulns(); got != 10 {
		t.Errorf("expected 10, got %d", got)
	}
	empty := Project{}
	if got := empty.TotalVulns(); got != 0 {
		t.Errorf("empty should be 0, got %d", got)
	}
}

func TestHasCriticalSecurityIssue(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		p    Project
		want bool
	}{
		{
			"zero project",
			Project{},
			false,
		},
		{
			"critical vuln",
			Project{VulnCritical: 1},
			true,
		},
		{
			"high vuln",
			Project{VulnHigh: 1},
			true,
		},
		{
			"medium vuln only — not critical",
			Project{VulnMedium: 5},
			false,
		},
		{
			"low scorecard with scan",
			Project{ScorecardScore: 3.0, ScorecardAt: now},
			true,
		},
		{
			"low scorecard but no scan yet",
			Project{ScorecardScore: 3.0}, // ScorecardAt zero
			false,
		},
		{
			"healthy scorecard",
			Project{ScorecardScore: 8.0, ScorecardAt: now},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.HasCriticalSecurityIssue(); got != tt.want {
				t.Errorf("got %v want %v", got, tt.want)
			}
		})
	}
}

// ─── MatchesQuery ─────────────────────────────────────────────

func TestMatchesQuery(t *testing.T) {
	p := &Project{
		Slug:        "breeze",
		DisplayName: "Breeze App",
		Repository:  "github.com/org/breeze",
		Notes:       "Handles payments",
		Tags:        []string{"backend", "finance"},
	}
	cases := []struct {
		q    string
		want bool
	}{
		{"", true},                // empty always matches
		{"breeze", true},          // slug match
		{"BREEZE", true},          // case-insensitive
		{"app", true},             // display_name match
		{"payments", true},        // notes match
		{"github.com", true},      // repository match
		{"finance", true},         // tag match
		{"backend", true},         // tag match
		{"golang", false},         // no match anywhere
		{"   ", true},             // whitespace-only → trimmed to empty → always true
	}
	for _, tc := range cases {
		t.Run(tc.q, func(t *testing.T) {
			if got := p.MatchesQuery(tc.q); got != tc.want {
				t.Errorf("MatchesQuery(%q) = %v, want %v", tc.q, got, tc.want)
			}
		})
	}
}

// ─── DaysSince ───────────────────────────────────────────────

func TestDaysSince(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		t    time.Time
		want int
	}{
		{"zero time returns -1", time.Time{}, -1},
		{"same day returns 0", now.Add(-1 * time.Hour), 0},
		{"1 day ago", now.Add(-25 * time.Hour), 1},
		{"7 days ago", now.Add(-7 * 24 * time.Hour), 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DaysSince(tc.t, now); got != tc.want {
				t.Errorf("DaysSince = %d, want %d", got, tc.want)
			}
		})
	}
}
