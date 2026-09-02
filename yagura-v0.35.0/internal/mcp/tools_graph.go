// tools_graph.go: extracted from tools.go in v0.29 (gstack-style topic split).
//
// All tools in this file are registered via RegisterDefaultTools in tools.go.
// See CLAUDE.md Workflows for the registration pattern.

package mcp

import (
	"context"
	"encoding/json"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/projectgraph"
)

func buildGraphNeighborsTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_graph_neighbors",
		Title:       "Graph Neighbors Walk",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Graph walk from slug. Returns direct + N-hop deps/dependents.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":  map[string]any{"type": "string", "description": "project slug to explore from"},
				"depth": map[string]any{"type": "integer", "description": "max hops (default 2, max 10)", "minimum": 1},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug  string `json:"slug"`
				Depth int    `json:"depth"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug required"}
			}
			if in.Depth < 1 {
				in.Depth = 2
			}
			if in.Depth > 10 {
				in.Depth = 10
			}
			g := projectgraph.Build(toGraphProjects(d.Registry.List()))
			return g.Neighbors(in.Slug, in.Depth), nil
		},
	}
}

func buildGraphImpactTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_graph_impact",
		Title:       "Project Impact Graph",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Project impact (transitive reverse deps). Cycle-aware.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug": map[string]any{"type": "string"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug string `json:"slug"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug required"}
			}
			g := projectgraph.Build(toGraphProjects(d.Registry.List()))
			return g.Impact(in.Slug), nil
		},
	}
}

func buildGraphStatsTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_graph_stats",
		Title:       "Graph Statistics",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Graph stats: nodes/edges/hubs + dangling deps.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			g := projectgraph.Build(toGraphProjects(d.Registry.List()))
			return map[string]any{
				"stats":    g.Stats(),
				"dangling": g.Dangling(),
			}, nil
		},
	}
}

func toGraphProjects(ps []*project.Project) []projectgraph.Project {
	out := make([]projectgraph.Project, 0, len(ps))
	for _, p := range ps {
		out = append(out, projectgraph.Project{
			Slug:      p.Slug,
			DependsOn: p.DependsOn,
		})
	}
	return out
}
