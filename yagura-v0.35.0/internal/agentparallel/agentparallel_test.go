package agentparallel

import (
	"reflect"
	"testing"
)

func mkTasks(ids ...string) []Task {
	ts := make([]Task, len(ids))
	for i, id := range ids {
		ts[i] = Task{ID: id, Weight: 1}
	}
	return ts
}

func assignmentFor(p Plan, agent string) (Assignment, bool) {
	for _, a := range p.Assignments {
		if a.Agent == agent {
			return a, true
		}
	}
	return Assignment{}, false
}

func TestPlan_EvenSplitBalanced(t *testing.T) {
	// 4 unit tasks, 2 equal-capacity agents → 2 each.
	agents := []Agent{
		{Name: "a", Tier: TierMid, CapacityPct: 100, MaxConcurrency: 1},
		{Name: "b", Tier: TierMid, CapacityPct: 100, MaxConcurrency: 1},
	}
	p := PlanDataParallel(mkTasks("t1", "t2", "t3", "t4"), agents, 0)
	if p.FanOutWidth != 2 {
		t.Fatalf("expected fan-out 2, got %d", p.FanOutWidth)
	}
	for _, name := range []string{"a", "b"} {
		as, ok := assignmentFor(p, name)
		if !ok || len(as.Tasks) != 2 {
			t.Errorf("agent %s should get 2 tasks, got %+v", name, as)
		}
	}
	if !p.Barrier {
		t.Error("data-parallel plan should have a barrier")
	}
}

func TestPlan_CapacityWeighted(t *testing.T) {
	// agent "big" has 3x the capacity of "small" → should get ~3x the load.
	agents := []Agent{
		{Name: "big", Tier: TierMid, CapacityPct: 90, MaxConcurrency: 4},
		{Name: "small", Tier: TierMid, CapacityPct: 30, MaxConcurrency: 4},
	}
	p := PlanDataParallel(mkTasks("t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"), agents, 0)
	big, _ := assignmentFor(p, "big")
	small, _ := assignmentFor(p, "small")
	if !(len(big.Tasks) > len(small.Tasks)) {
		t.Errorf("higher-capacity agent should carry more: big=%d small=%d", len(big.Tasks), len(small.Tasks))
	}
	if len(big.Tasks)+len(small.Tasks) != 8 {
		t.Errorf("all 8 tasks must be assigned, got %d", len(big.Tasks)+len(small.Tasks))
	}
}

func TestPlan_Deterministic(t *testing.T) {
	agents := []Agent{
		{Name: "b", Tier: TierMid, CapacityPct: 50, MaxConcurrency: 2},
		{Name: "a", Tier: TierMid, CapacityPct: 50, MaxConcurrency: 2},
		{Name: "c", Tier: TierMid, CapacityPct: 50, MaxConcurrency: 2},
	}
	tasks := mkTasks("x", "y", "z", "p", "q")
	p1 := PlanDataParallel(tasks, agents, 0)
	p2 := PlanDataParallel(tasks, agents, 0)
	if !reflect.DeepEqual(p1, p2) {
		t.Errorf("plan must be deterministic:\n%+v\n%+v", p1, p2)
	}
	// agents are reported in name order regardless of input order.
	for i := 1; i < len(p1.Assignments); i++ {
		if p1.Assignments[i-1].Agent > p1.Assignments[i].Agent {
			t.Errorf("assignments not sorted by agent name: %+v", p1.Assignments)
		}
	}
}

func TestPlan_TierRouting(t *testing.T) {
	// a strong task must only go to the opus-tier agent.
	agents := []Agent{
		{Name: "haiku", Tier: TierCheap, CapacityPct: 100, MaxConcurrency: 4},
		{Name: "opus", Tier: TierStrong, CapacityPct: 100, MaxConcurrency: 4},
	}
	tasks := []Task{
		{ID: "hard", Weight: 1, MinTier: TierStrong},
		{ID: "easy1", Weight: 1},
		{ID: "easy2", Weight: 1},
	}
	p := PlanDataParallel(tasks, agents, 0)
	opus, _ := assignmentFor(p, "opus")
	foundHard := false
	for _, id := range opus.Tasks {
		if id == "hard" {
			foundHard = true
		}
	}
	if !foundHard {
		t.Errorf("strong task must be routed to opus-tier agent, got %+v", p.Assignments)
	}
	// haiku must never receive the hard task.
	haiku, _ := assignmentFor(p, "haiku")
	for _, id := range haiku.Tasks {
		if id == "hard" {
			t.Error("cheap-tier agent received a strong-tier task")
		}
	}
}

func TestPlan_Unassignable(t *testing.T) {
	// no agent meets the strong requirement → task is unassigned, not a crash.
	agents := []Agent{{Name: "haiku", Tier: TierCheap, CapacityPct: 100, MaxConcurrency: 1}}
	tasks := []Task{{ID: "needs-opus", Weight: 1, MinTier: TierStrong}}
	p := PlanDataParallel(tasks, agents, 0)
	if len(p.Unassigned) != 1 || p.Unassigned[0] != "needs-opus" {
		t.Errorf("expected needs-opus unassigned, got %+v", p.Unassigned)
	}
}

func TestPlan_AllAgentsExhausted(t *testing.T) {
	agents := []Agent{{Name: "a", Tier: TierMid, CapacityPct: 0, MaxConcurrency: 1}}
	p := PlanDataParallel(mkTasks("t1", "t2"), agents, 0)
	if p.FanOutWidth != 0 || len(p.Unassigned) != 2 {
		t.Errorf("exhausted agents → everything unassigned, got %+v", p)
	}
}

func TestPlan_Waves(t *testing.T) {
	// one agent, concurrency 2, 5 tasks → ceil(5/2)=3 waves.
	agents := []Agent{{Name: "a", Tier: TierMid, CapacityPct: 100, MaxConcurrency: 2}}
	p := PlanDataParallel(mkTasks("t1", "t2", "t3", "t4", "t5"), agents, 0)
	as, _ := assignmentFor(p, "a")
	if as.Waves != 3 {
		t.Errorf("expected 3 waves, got %d", as.Waves)
	}
	if p.EstWaves != 3 {
		t.Errorf("expected est_waves 3, got %d", p.EstWaves)
	}
}

func TestPlan_GlobalConcurrencyCap(t *testing.T) {
	// 2 agents able to run 4 in parallel, but global cap of 1 serializes to 8 waves.
	agents := []Agent{
		{Name: "a", Tier: TierMid, CapacityPct: 100, MaxConcurrency: 2},
		{Name: "b", Tier: TierMid, CapacityPct: 100, MaxConcurrency: 2},
	}
	p := PlanDataParallel(mkTasks("t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"), agents, 1)
	if p.EstWaves < 8 {
		t.Errorf("global cap of 1 should serialize to >=8 waves, got %d", p.EstWaves)
	}
	if len(p.Notes) == 0 {
		t.Error("expected a note about the global concurrency cap")
	}
}

func TestPlan_Empty(t *testing.T) {
	p := PlanDataParallel(nil, nil, 0)
	if p.FanOutWidth != 0 || len(p.Assignments) != 0 || len(p.Unassigned) != 0 {
		t.Errorf("empty input → empty plan, got %+v", p)
	}
	if p.Strategy != "data" || !p.Barrier {
		t.Errorf("plan metadata should still be set: %+v", p)
	}
}

func TestPlan_DefaultWeightAndConcurrency(t *testing.T) {
	// weight<=0 → 1, concurrency<=0 → 1.
	agents := []Agent{{Name: "a", Tier: TierMid, CapacityPct: 100, MaxConcurrency: 0}}
	tasks := []Task{{ID: "t1"}, {ID: "t2"}} // zero weight
	p := PlanDataParallel(tasks, agents, 0)
	as, _ := assignmentFor(p, "a")
	if as.LoadWeight != 2 {
		t.Errorf("zero weights should default to 1 each (load 2), got %v", as.LoadWeight)
	}
	if as.Waves != 2 {
		t.Errorf("concurrency<=0 defaults to 1 → 2 waves, got %d", as.Waves)
	}
}
