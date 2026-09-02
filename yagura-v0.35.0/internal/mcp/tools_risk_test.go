package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/project"
)

func TestRiskTriage_RanksAndExplains(t *testing.T) {
	tool := buildRiskTriageTool(newDeps(t))
	res := mustCall(t, tool, map[string]any{
		"findings": []map[string]any{
			{"cve": "CVE-2026-1000", "cvss": 9.8, "internet_exposed": true, "known_exploited": true},
			{"cve": "CVE-2026-2000", "cvss": 4.2},
		},
		"asset_priority": 5,
		"tags":           []string{"production", "pii"},
		"dependents":     3,
	})
	b, _ := json.Marshal(res)
	var out struct {
		Triaged []struct {
			CVE      string `json:"cve"`
			Score    int    `json:"score"`
			Priority string `json:"priority"`
			Factors  []struct {
				Name string `json:"name"`
			} `json:"factors"`
			Recommendation string `json:"recommendation"`
		} `json:"triaged"`
		Summary map[string]int `json:"summary"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("json: %v\n%s", err, b)
	}
	if len(out.Triaged) != 2 {
		t.Fatalf("expected 2 triaged, got %d", len(out.Triaged))
	}
	// highest risk first; the exploited+exposed critical must lead and be NOW.
	if out.Triaged[0].CVE != "CVE-2026-1000" || out.Triaged[0].Priority != "NOW" {
		t.Errorf("expected critical exploited CVE first as NOW, got %+v", out.Triaged[0])
	}
	if len(out.Triaged[0].Factors) == 0 {
		t.Error("triage must include a rationale (factors)")
	}
	if out.Summary["NOW"] < 1 {
		t.Errorf("summary should count a NOW, got %+v", out.Summary)
	}
}

func TestRiskTriage_SlugFillsAssetContext(t *testing.T) {
	d := newDeps(t)
	// register two assets: "core" with a dependent "web" → core has blast radius 1.
	if err := d.Registry.Add(sampleProject("core", func(p *project.Project) {
		p.Priority = 5
		p.Tags = []string{"production"}
	})); err != nil {
		t.Fatal(err)
	}
	if err := d.Registry.Add(sampleProject("web", func(p *project.Project) {
		p.DependsOn = []string{"core"}
	})); err != nil {
		t.Fatal(err)
	}
	tool := buildRiskTriageTool(d)
	res := mustCall(t, tool, map[string]any{
		"slug":     "core",
		"findings": []map[string]any{{"cve": "CVE-2026-3000", "cvss": 7.5}},
	})
	b, _ := json.Marshal(res)
	var out struct {
		Asset struct {
			Slug          string `json:"slug"`
			AssetPriority int    `json:"asset_priority"`
			Dependents    int    `json:"dependents"`
		} `json:"asset"`
		Triaged []struct {
			Factors []struct {
				Name string `json:"name"`
			} `json:"factors"`
		} `json:"triaged"`
	}
	_ = json.Unmarshal(b, &out)
	if out.Asset.Slug != "core" || out.Asset.AssetPriority != 5 {
		t.Errorf("slug should fill asset context from registry, got %+v", out.Asset)
	}
	if out.Asset.Dependents != 1 {
		t.Errorf("blast radius should come from the graph (1 dependent), got %d", out.Asset.Dependents)
	}
	// the filled priority + blast radius must appear as rationale factors.
	names := map[string]bool{}
	for _, f := range out.Triaged[0].Factors {
		names[f.Name] = true
	}
	if !names["asset_priority"] || !names["blast_radius"] {
		t.Errorf("expected asset_priority + blast_radius factors from registry/graph, got %+v", out.Triaged[0].Factors)
	}
}

func TestRiskTriage_NoFindings(t *testing.T) {
	tool := buildRiskTriageTool(newDeps(t))
	b, _ := json.Marshal(map[string]any{"findings": []any{}})
	if _, err := tool.Handler(context.Background(), b); err == nil {
		t.Error("expected error for empty findings")
	}
}

func TestRiskTriage_SlugNotFoundWarns(t *testing.T) {
	tool := buildRiskTriageTool(newDeps(t)) // empty registry
	res := mustCall(t, tool, map[string]any{
		"slug":           "does-not-exist",
		"asset_priority": 4,
		"findings":       []map[string]any{{"cve": "CVE-2026-9", "cvss": 7.0}},
	})
	b, _ := json.Marshal(res)
	var out struct {
		Warnings []string `json:"warnings"`
		Asset    struct {
			Slug string `json:"slug"`
		} `json:"asset"`
	}
	_ = json.Unmarshal(b, &out)
	if out.Asset.Slug != "" {
		t.Errorf("unresolved slug should leave asset.slug empty, got %q", out.Asset.Slug)
	}
	if len(out.Warnings) == 0 || !strings.Contains(out.Warnings[0], "not found in registry") {
		t.Errorf("expected a not-found warning, got %+v", out.Warnings)
	}
}

func TestRiskTriage_WeightsOverride(t *testing.T) {
	tool := buildRiskTriageTool(newDeps(t))
	args := map[string]any{
		"findings": []map[string]any{{"cve": "CVE-1", "cvss": 9.8, "known_exploited": true}},
		// drastically down-weight so the same finding is no longer NOW.
		"weights": map[string]any{"sev_critical": 1, "known_exploited": 1, "band_now": 90},
	}
	res := mustCall(t, tool, args)
	b, _ := json.Marshal(res)
	var out struct {
		Triaged []struct {
			Priority string `json:"priority"`
			Score    int    `json:"score"`
		} `json:"triaged"`
	}
	_ = json.Unmarshal(b, &out)
	if out.Triaged[0].Priority == "NOW" {
		t.Errorf("down-weighted critical+KEV should not be NOW, got %+v", out.Triaged[0])
	}
	// invalid weights → error.
	bad, _ := json.Marshal(map[string]any{
		"findings": []map[string]any{{"cve": "x", "cvss": 5}},
		"weights":  "not-an-object",
	})
	if _, err := tool.Handler(tCtx(), bad); err == nil {
		t.Error("expected error for invalid weights override")
	}
}
