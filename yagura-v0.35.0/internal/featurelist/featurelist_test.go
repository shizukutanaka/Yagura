package featurelist

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
}

// ─── slug ─────────────────────────────────────────

func TestSlug_BasicLowercase(t *testing.T) {
	if got := slug("Hello World"); got != "hello-world" {
		t.Errorf("slug: %s", got)
	}
}

func TestSlug_StripsPunctuation(t *testing.T) {
	if got := slug("Add: feature, please!"); got != "add-feature-please" {
		t.Errorf("slug: %s", got)
	}
}

func TestSlug_CollapsesMultipleSeparators(t *testing.T) {
	if got := slug("a   b___c"); got != "a-b-c" {
		t.Errorf("slug: %s", got)
	}
}

func TestSlug_StripsLeadingTrailingHyphens(t *testing.T) {
	if got := slug("!!hello!!"); got != "hello" {
		t.Errorf("slug: %s", got)
	}
}

func TestSlug_FallbackForEmpty(t *testing.T) {
	if got := slug("!!!"); got != "task" {
		t.Errorf("empty -> task: %s", got)
	}
	if got := slug(""); got != "task" {
		t.Errorf("empty string -> task: %s", got)
	}
}

func TestSlug_Truncates(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := slug(long)
	if len(got) > 50 {
		t.Errorf("slug should be <=50 chars: %d", len(got))
	}
}

// ─── uniqueSlug ─────────────────────────────────

func TestUniqueSlug_AppendsCounter(t *testing.T) {
	used := map[string]int{}
	if got := uniqueSlug("Fix bug", used); got != "fix-bug" {
		t.Errorf("first: %s", got)
	}
	if got := uniqueSlug("Fix bug", used); got != "fix-bug-2" {
		t.Errorf("second: %s", got)
	}
	if got := uniqueSlug("Fix bug", used); got != "fix-bug-3" {
		t.Errorf("third: %s", got)
	}
}

// ─── Build ────────────────────────────────────────

func TestBuild_SinglePhaseSingleTask(t *testing.T) {
	in := PlanInput{
		Project: "breeze",
		Phases: []PhaseInput{
			{Name: "v11 hardening", Tasks: []TaskInput{{Title: "Audit MLS", Done: false}}},
		},
		DoD: []string{"All tests green"},
	}
	fl := Build(in, fixedNow)
	if len(fl.Features) != 1 {
		t.Fatalf("features count: %d", len(fl.Features))
	}
	f := fl.Features[0]
	if f.Title != "Audit MLS" {
		t.Errorf("title: %s", f.Title)
	}
	if f.ID != "audit-mls" {
		t.Errorf("id: %s", f.ID)
	}
	if f.Phase != "v11 hardening" {
		t.Errorf("phase: %s", f.Phase)
	}
	if f.Status != "pending" {
		t.Errorf("status: %s", f.Status)
	}
	if len(f.AcceptanceCriteria) != 1 || f.AcceptanceCriteria[0] != "All tests green" {
		t.Errorf("DoD not attached: %v", f.AcceptanceCriteria)
	}
}

func TestBuild_DoneStatusForCheckedTasks(t *testing.T) {
	in := PlanInput{
		Phases: []PhaseInput{{Name: "alpha", Tasks: []TaskInput{
			{Title: "first", Done: true},
			{Title: "second", Done: false},
		}}},
	}
	fl := Build(in, fixedNow)
	if fl.Features[0].Status != "done" {
		t.Errorf("first should be done")
	}
	if fl.Features[1].Status != "pending" {
		t.Errorf("second should be pending")
	}
}

func TestBuild_StatsComputed(t *testing.T) {
	in := PlanInput{
		Phases: []PhaseInput{{Tasks: []TaskInput{
			{Title: "a", Done: true},
			{Title: "b", Done: true},
			{Title: "c", Done: false},
			{Title: "d", Done: false},
			{Title: "e", Done: false},
		}}},
	}
	fl := Build(in, fixedNow)
	if fl.Stats.Total != 5 {
		t.Errorf("total: %d", fl.Stats.Total)
	}
	if fl.Stats.Done != 2 {
		t.Errorf("done: %d", fl.Stats.Done)
	}
	if fl.Stats.Pending != 3 {
		t.Errorf("pending: %d", fl.Stats.Pending)
	}
}

func TestBuild_DoDAttachedToEveryFeature(t *testing.T) {
	in := PlanInput{
		Phases: []PhaseInput{{Tasks: []TaskInput{
			{Title: "a", Done: false},
			{Title: "b", Done: false},
		}}},
		DoD: []string{"crit-1", "crit-2"},
	}
	fl := Build(in, fixedNow)
	for _, f := range fl.Features {
		if len(f.AcceptanceCriteria) != 2 {
			t.Errorf("DoD missing for %s: %v", f.ID, f.AcceptanceCriteria)
		}
	}
}

func TestBuild_DuplicateTitlesGetUniqueIDs(t *testing.T) {
	in := PlanInput{
		Phases: []PhaseInput{{Tasks: []TaskInput{
			{Title: "Refactor", Done: false},
			{Title: "Refactor", Done: false},
			{Title: "Refactor", Done: false},
		}}},
	}
	fl := Build(in, fixedNow)
	ids := map[string]bool{}
	for _, f := range fl.Features {
		if ids[f.ID] {
			t.Errorf("duplicate id: %s", f.ID)
		}
		ids[f.ID] = true
	}
	if !ids["refactor"] || !ids["refactor-2"] || !ids["refactor-3"] {
		t.Errorf("expected refactor / refactor-2 / refactor-3, got %v", ids)
	}
}

func TestBuild_GeneratedAtFromNowFn(t *testing.T) {
	fl := Build(PlanInput{}, fixedNow)
	if !fl.GeneratedAt.Equal(fixedNow().UTC()) {
		t.Errorf("GeneratedAt not from NowFn: %v", fl.GeneratedAt)
	}
}

func TestBuild_NilNowFnUsesTimeNow(t *testing.T) {
	fl := Build(PlanInput{}, nil)
	if fl.GeneratedAt.IsZero() {
		t.Error("GeneratedAt should be set when NowFn is nil")
	}
}

func TestBuild_EmptyInputProducesEmptyFeatureList(t *testing.T) {
	fl := Build(PlanInput{}, fixedNow)
	if len(fl.Features) != 0 {
		t.Errorf("empty plan should produce zero features")
	}
	if fl.Stats.Total != 0 {
		t.Errorf("stats: %+v", fl.Stats)
	}
}

func TestBuild_PreservesPhaseOrder(t *testing.T) {
	in := PlanInput{Phases: []PhaseInput{
		{Name: "z", Tasks: []TaskInput{{Title: "zt"}}},
		{Name: "a", Tasks: []TaskInput{{Title: "at"}}},
		{Name: "m", Tasks: []TaskInput{{Title: "mt"}}},
	}}
	fl := Build(in, fixedNow)
	if fl.Features[0].Phase != "z" || fl.Features[1].Phase != "a" || fl.Features[2].Phase != "m" {
		t.Errorf("phase order not preserved: %+v", fl.Features)
	}
}

// ─── Marshal ─────────────────────────────────────

func TestMarshal_RoundTrip(t *testing.T) {
	in := PlanInput{
		Project: "breeze",
		Phases:  []PhaseInput{{Name: "alpha", Tasks: []TaskInput{{Title: "first", Done: true}}}},
		DoD:     []string{"green CI"},
	}
	fl := Build(in, fixedNow)
	raw, err := Marshal(fl)
	if err != nil {
		t.Fatal(err)
	}
	var back FeatureList
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if back.Features[0].ID != fl.Features[0].ID {
		t.Errorf("ID mismatch: %s vs %s", back.Features[0].ID, fl.Features[0].ID)
	}
	if back.Stats.Done != 1 {
		t.Errorf("stats lost on round-trip: %+v", back.Stats)
	}
}

func TestMarshal_DeterministicForSameInput(t *testing.T) {
	in := PlanInput{
		Project: "x",
		Phases:  []PhaseInput{{Tasks: []TaskInput{{Title: "do it"}}}},
	}
	a, _ := Marshal(Build(in, fixedNow))
	b, _ := Marshal(Build(in, fixedNow))
	if string(a) != string(b) {
		t.Error("marshal output not deterministic")
	}
}
