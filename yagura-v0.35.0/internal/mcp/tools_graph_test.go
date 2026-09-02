package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/projectgraph"
)

// graphDeps registers app → lib (app depends on lib) plus an isolated node.
func graphDeps(t *testing.T) Deps {
	t.Helper()
	d := newDeps(t)
	_ = d.Registry.Add(sampleProject("app", func(p *project.Project) { p.DependsOn = []string{"lib"} }))
	_ = d.Registry.Add(sampleProject("lib"))
	_ = d.Registry.Add(sampleProject("solo"))
	return d
}

func TestGraphNeighbors(t *testing.T) {
	d := graphDeps(t)
	tool := buildGraphNeighborsTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "lib"}).(projectgraph.NeighborsResult)
	// app depends on lib → lib's direct dependent is app
	found := false
	for _, dep := range r.DirectDependents {
		if dep == "app" {
			found = true
		}
	}
	if !found {
		t.Errorf("lib should have app as a direct dependent, got %+v", r.DirectDependents)
	}
}

func TestGraphNeighbors_DepthClampedAndDefaulted(t *testing.T) {
	d := graphDeps(t)
	tool := buildGraphNeighborsTool(d)
	// depth omitted → default 2
	r := mustCall(t, tool, map[string]any{"slug": "app"}).(projectgraph.NeighborsResult)
	if r.Depth != 2 {
		t.Errorf("default depth = %d, want 2", r.Depth)
	}
	// depth over the max → clamped to 10
	r2 := mustCall(t, tool, map[string]any{"slug": "app", "depth": 99}).(projectgraph.NeighborsResult)
	if r2.Depth != 10 {
		t.Errorf("depth 99 should clamp to 10, got %d", r2.Depth)
	}
}

func TestGraphImpact(t *testing.T) {
	d := graphDeps(t)
	tool := buildGraphImpactTool(d)
	r := mustCall(t, tool, map[string]any{"slug": "lib"}).(projectgraph.ImpactResult)
	// changing lib impacts app
	if r.ImpactCount < 1 {
		t.Errorf("lib impact_count = %d, want >= 1", r.ImpactCount)
	}
	found := false
	for _, s := range r.TransitiveImpact {
		if s == "app" {
			found = true
		}
	}
	if !found {
		t.Errorf("lib impact should include app, got %+v", r.TransitiveImpact)
	}
}

func TestGraphStats(t *testing.T) {
	d := graphDeps(t)
	tool := buildGraphStatsTool(d)
	r := mustCall(t, tool, struct{}{}).(map[string]any)
	if _, ok := r["stats"]; !ok {
		t.Error("expected a stats key")
	}
	if _, ok := r["dangling"]; !ok {
		t.Error("expected a dangling key")
	}
}

func TestGraphTools_SlugRequired(t *testing.T) {
	d := graphDeps(t)
	for name, tool := range map[string]*Tool{
		"neighbors": buildGraphNeighborsTool(d),
		"impact":    buildGraphImpactTool(d),
	} {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(map[string]any{})
			if _, err := tool.Handler(context.Background(), b); !IsCode(err, "invalid_input") {
				t.Errorf("%s: expected invalid_input for missing slug, got %v", name, err)
			}
		})
	}
}
