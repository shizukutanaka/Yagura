package projectgraph

import (
	"reflect"
	"strings"
	"testing"
)

// ─── Build / 基本構造 ───────────────────────────────────────

func TestBuild_EmptyProjects(t *testing.T) {
	g := Build(nil)
	if g.Stats().NodeCount != 0 {
		t.Errorf("empty: node count should be 0")
	}
}

func TestBuild_SingleProjectNoDeps(t *testing.T) {
	g := Build([]Project{{Slug: "alpha"}})
	s := g.Stats()
	if s.NodeCount != 1 {
		t.Errorf("node count: got %d, want 1", s.NodeCount)
	}
	if s.EdgeCount != 0 {
		t.Errorf("edge count: got %d, want 0", s.EdgeCount)
	}
	if s.IsolatedCount != 1 {
		t.Errorf("isolated count: got %d, want 1", s.IsolatedCount)
	}
}

func TestBuild_LinearChain(t *testing.T) {
	// alpha → beta → gamma
	g := Build([]Project{
		{Slug: "alpha", DependsOn: []string{"beta"}},
		{Slug: "beta", DependsOn: []string{"gamma"}},
		{Slug: "gamma"},
	})
	s := g.Stats()
	if s.NodeCount != 3 || s.EdgeCount != 2 {
		t.Errorf("got nodes=%d edges=%d, want nodes=3 edges=2", s.NodeCount, s.EdgeCount)
	}
	if s.RootCount != 1 || s.LeafCount != 1 {
		t.Errorf("got roots=%d leaves=%d, want both=1", s.RootCount, s.LeafCount)
	}
}

// ─── Neighbors ─────────────────────────────────────────────

func TestNeighbors_Direct(t *testing.T) {
	g := Build([]Project{
		{Slug: "alpha", DependsOn: []string{"beta", "charlie"}},
		{Slug: "beta"},
		{Slug: "charlie"},
	})
	r := g.Neighbors("alpha", 1)
	if !reflect.DeepEqual(r.DirectDeps, []string{"beta", "charlie"}) {
		t.Errorf("direct deps: got %v, want [beta, charlie]", r.DirectDeps)
	}
	if len(r.DirectDependents) != 0 {
		t.Errorf("direct dependents: got %v, want []", r.DirectDependents)
	}
}

func TestNeighbors_Transitive(t *testing.T) {
	// alpha → beta → gamma, alpha → delta
	g := Build([]Project{
		{Slug: "alpha", DependsOn: []string{"beta", "delta"}},
		{Slug: "beta", DependsOn: []string{"gamma"}},
		{Slug: "gamma"},
		{Slug: "delta"},
	})
	r := g.Neighbors("alpha", 2)
	if !reflect.DeepEqual(r.DirectDeps, []string{"beta", "delta"}) {
		t.Errorf("direct: got %v", r.DirectDeps)
	}
	if !reflect.DeepEqual(r.TransitiveDeps, []string{"gamma"}) {
		t.Errorf("transitive (depth 2 only): got %v, want [gamma]", r.TransitiveDeps)
	}
}

func TestNeighbors_NonExistentSlug(t *testing.T) {
	g := Build([]Project{{Slug: "alpha"}})
	r := g.Neighbors("nonexistent", 5)
	if len(r.DirectDeps) != 0 || len(r.DirectDependents) != 0 {
		t.Errorf("nonexistent slug should return empty result, got %+v", r)
	}
}

// ─── Impact ─────────────────────────────────────────────────

func TestImpact_DirectAndTransitive(t *testing.T) {
	// breeze 変更 → SDK 変更 → app 変更, breeze 変更 → web 変更
	g := Build([]Project{
		{Slug: "breeze"},
		{Slug: "sdk", DependsOn: []string{"breeze"}},
		{Slug: "app", DependsOn: []string{"sdk"}},
		{Slug: "web", DependsOn: []string{"breeze"}},
	})
	r := g.Impact("breeze")
	if !reflect.DeepEqual(r.DirectImpact, []string{"sdk", "web"}) {
		t.Errorf("direct impact: got %v, want [sdk, web]", r.DirectImpact)
	}
	// transitive: sdk, web, app
	if r.ImpactCount != 3 {
		t.Errorf("impact count: got %d, want 3", r.ImpactCount)
	}
}

func TestImpact_NoImpact(t *testing.T) {
	g := Build([]Project{
		{Slug: "alpha"},
		{Slug: "beta"},
	})
	r := g.Impact("alpha")
	if r.ImpactCount != 0 {
		t.Errorf("isolated: expected 0 impact, got %d", r.ImpactCount)
	}
}

// ─── Cycle 検出 ────────────────────────────────────────────

func TestImpact_DetectsCycle(t *testing.T) {
	// alpha → beta → alpha
	g := Build([]Project{
		{Slug: "alpha", DependsOn: []string{"beta"}},
		{Slug: "beta", DependsOn: []string{"alpha"}},
	})
	r := g.Impact("alpha")
	if !r.HasCycle {
		t.Error("expected HasCycle=true")
	}
}

// ─── Dangling deps(graph drift)─────────────────────────────

func TestDangling_DetectsMissing(t *testing.T) {
	g := Build([]Project{
		{Slug: "alpha", DependsOn: []string{"ghost-project", "beta"}},
		{Slug: "beta"},
	})
	d := g.Dangling()
	if len(d) != 1 {
		t.Fatalf("dangling: got %d, want 1", len(d))
	}
	if d[0].From != "alpha" || d[0].To != "ghost-project" {
		t.Errorf("dangling: got %+v", d[0])
	}
}

// TestDangling_DeterministicOrder pins that Dangling() is sorted by (From, To)
// regardless of input project order. Every other list Build produces (forwards,
// reverses, slugs) is explicitly sorted; dangling must be too so the output does
// not depend on the caller's project ordering (Deterministic output rule).
func TestDangling_DeterministicOrder(t *testing.T) {
	// Projects deliberately given out of slug order, each with multiple missing
	// deps in non-sorted declaration order.
	g := Build([]Project{
		{Slug: "zeta", DependsOn: []string{"miss-y", "miss-x"}},
		{Slug: "alpha", DependsOn: []string{"miss-b", "miss-a"}},
	})
	d := g.Dangling()
	want := []DanglingDep{
		{From: "alpha", To: "miss-a"},
		{From: "alpha", To: "miss-b"},
		{From: "zeta", To: "miss-x"},
		{From: "zeta", To: "miss-y"},
	}
	if len(d) != len(want) {
		t.Fatalf("dangling count: got %d, want %d (%+v)", len(d), len(want), d)
	}
	for i := range want {
		if d[i] != want[i] {
			t.Errorf("dangling[%d] = %+v, want %+v (full: %+v)", i, d[i], want[i], d)
		}
	}
}

// ─── Stats(集計)───────────────────────────────────────────

func TestStats_MostDependedOn(t *testing.T) {
	// breeze に 3 つのプロジェクトが依存
	g := Build([]Project{
		{Slug: "breeze"},
		{Slug: "a", DependsOn: []string{"breeze"}},
		{Slug: "b", DependsOn: []string{"breeze"}},
		{Slug: "c", DependsOn: []string{"breeze"}},
	})
	s := g.Stats()
	if s.MostDependedOn != "breeze" {
		t.Errorf("most depended on: got %q, want breeze", s.MostDependedOn)
	}
	if s.MaxFanIn != 3 {
		t.Errorf("max fan-in: got %d, want 3", s.MaxFanIn)
	}
}

// ─── 大規模 graph(realistic portfolio scale)─────────────────

func TestNeighbors_RealisticPortfolio(t *testing.T) {
	// m's actual stack の縮約版: breeze をハブにする構成
	g := Build([]Project{
		{Slug: "breeze"},
		{Slug: "breeze-sdk", DependsOn: []string{"breeze"}},
		{Slug: "breeze-cf-worker", DependsOn: []string{"breeze"}},
		{Slug: "tile", DependsOn: []string{"breeze"}},
		{Slug: "tessera"},
		{Slug: "izanagi"},
		{Slug: "yagura"},
		{Slug: "anatomy3d"},
		{Slug: "nukamiso", DependsOn: []string{"anatomy3d"}},
	})
	r := g.Impact("breeze")
	// breeze の影響範囲: breeze-sdk, breeze-cf-worker, tile
	if r.ImpactCount != 3 {
		t.Errorf("breeze impact: got %d, want 3", r.ImpactCount)
	}
	// dangling は無し
	if len(g.Dangling()) != 0 {
		t.Errorf("no dangling expected, got %v", g.Dangling())
	}
}

// ─── 深さ制限の境界 ────────────────────────────────────────

func TestNeighbors_DepthZero(t *testing.T) {
	g := Build([]Project{
		{Slug: "alpha", DependsOn: []string{"beta"}},
		{Slug: "beta"},
	})
	r := g.Neighbors("alpha", 0)
	// depth=0 は depth=1 に正規化される(direct のみ)
	if !reflect.DeepEqual(r.DirectDeps, []string{"beta"}) {
		t.Errorf("depth=0 should give direct only, got %v", r.DirectDeps)
	}
}

func TestString_Format(t *testing.T) {
	g := Build([]Project{
		{Slug: "a", DependsOn: []string{"b"}},
		{Slug: "b", DependsOn: []string{"c"}},
		{Slug: "c"},
	})
	s := g.String()
	if s == "" {
		t.Fatal("String() returned empty")
	}
	if !strings.Contains(s, "nodes=3") {
		t.Errorf("expected nodes=3 in %q", s)
	}
	if !strings.Contains(s, "edges=2") {
		t.Errorf("expected edges=2 in %q", s)
	}
}

func TestCountEdges_Empty(t *testing.T) {
	if n := countEdges(nil); n != 0 {
		t.Errorf("countEdges(nil) = %d, want 0", n)
	}
}

// 空の dangling は `null` ではなく `[]`(v1.3.3)。理由は alertfix と同じ:
// null は「参照切れが無い」と「調べていない」を区別できない。
func TestGraph_EmptyDanglingIsEmptySliceNotNil(t *testing.T) {
	g := Build(nil)
	if got := g.Dangling(); got == nil {
		t.Error("Dangling() must return an empty slice, not nil — null in JSON conflates " +
			"'no dangling refs' with 'not checked'")
	}
}
