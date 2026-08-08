package today_test

import (
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/today"
)

func now() time.Time { return time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC) }

func TestRank_ScoreComponents(t *testing.T) {
	ps := []*project.Project{
		{Slug: "p", DisplayName: "P", Stage: project.StageActive, Priority: 4, OpenPRs: 2, CIStatus: project.CIStatusFailing,
			LatestActivity: now().AddDate(0, 0, -20)},
	}
	items := today.Rank(ps, now(), 0)
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	// 4*10 + 2*3 + 20 (CI) + 5 (stale) = 71
	if items[0].Score != 71 {
		t.Errorf("score: want 71 got %v", items[0].Score)
	}
	// reasons: high priority, open PRs, CI failing, stale
	if len(items[0].Reasons) != 4 {
		t.Errorf("reasons: want 4 got %v", items[0].Reasons)
	}
	if items[0].DaysIdle != 20 {
		t.Errorf("days idle: want 20 got %d", items[0].DaysIdle)
	}
}

func TestRank_OnlyActiveAndMaintenance(t *testing.T) {
	ps := []*project.Project{
		{Slug: "a", Stage: project.StageActive, Priority: 1},
		{Slug: "m", Stage: project.StageMaintenance, Priority: 1},
		{Slug: "paused", Stage: project.StagePaused, Priority: 5},
		{Slug: "arch", Stage: project.StageArchived, Priority: 5},
	}
	items := today.Rank(ps, now(), 0)
	if len(items) != 2 {
		t.Fatalf("want 2 (active+maintenance only), got %d", len(items))
	}
	for _, it := range items {
		if it.Slug == "paused" || it.Slug == "arch" {
			t.Errorf("non-active/maintenance leaked in: %s", it.Slug)
		}
	}
}

func TestRank_StaleOnlyForActive(t *testing.T) {
	// maintenance project idle 30d must NOT get the stale bonus (active-only).
	ps := []*project.Project{
		{Slug: "m", Stage: project.StageMaintenance, LatestActivity: now().AddDate(0, 0, -30)},
	}
	items := today.Rank(ps, now(), 0)
	if items[0].Score != 0 {
		t.Errorf("maintenance idle must not score stale; got %v", items[0].Score)
	}
}

func TestRank_SortDescThenSlug(t *testing.T) {
	ps := []*project.Project{
		{Slug: "z", Stage: project.StageActive, Priority: 1}, // 10
		{Slug: "a", Stage: project.StageActive, Priority: 1}, // 10 (tie → slug asc)
		{Slug: "big", Stage: project.StageActive, Priority: 3}, // 30
	}
	items := today.Rank(ps, now(), 0)
	if items[0].Slug != "big" {
		t.Errorf("highest score first: want big got %s", items[0].Slug)
	}
	if items[1].Slug != "a" || items[2].Slug != "z" {
		t.Errorf("tie should break by slug asc: got %s,%s", items[1].Slug, items[2].Slug)
	}
}

func TestRank_Limit(t *testing.T) {
	ps := []*project.Project{
		{Slug: "a", Stage: project.StageActive, Priority: 5},
		{Slug: "b", Stage: project.StageActive, Priority: 4},
		{Slug: "c", Stage: project.StageActive, Priority: 3},
	}
	items := today.Rank(ps, now(), 2)
	if len(items) != 2 {
		t.Errorf("limit 2: want 2 got %d", len(items))
	}
}

func TestRank_ReasonsNonNil(t *testing.T) {
	// a zero-signal project must still have a non-nil (empty) reasons slice,
	// so JSON renders [] not null (matches the original MCP output).
	ps := []*project.Project{{Slug: "x", Stage: project.StageActive}}
	items := today.Rank(ps, now(), 0)
	if items[0].Reasons == nil {
		t.Error("reasons must be non-nil empty slice")
	}
}

func TestRank_Empty(t *testing.T) {
	if items := today.Rank(nil, now(), 5); len(items) != 0 {
		t.Errorf("want 0 items, got %d", len(items))
	}
}
