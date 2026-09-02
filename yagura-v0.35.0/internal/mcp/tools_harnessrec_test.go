// tools_harnessrec_test.go — tests for yagura_harness_recommend handler.
// Covers: language override, slug→language registry lookup, generic fallback,
// and the invalid-input / no-language error paths (handler was at 8.3% coverage).
package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shizukutanaka/yagura/internal/harness"
	"github.com/shizukutanaka/yagura/internal/project"
)

func TestHarnessRecommend_LanguageOverride(t *testing.T) {
	tool := buildHarnessRecommendTool(newDeps(t))
	res := mustCall(t, tool, map[string]any{"language": "go"}).(harness.Recommendation)
	if res.Language != "go" {
		t.Errorf("Language = %q, want go", res.Language)
	}
	if res.ClaudeMd == "" {
		t.Error("expected non-empty CLAUDE.md scaffold for go")
	}
}

func TestHarnessRecommend_SlugLookupFromRegistry(t *testing.T) {
	d := newDeps(t)
	// Register a project whose language drives the recommendation.
	if err := d.Registry.Add(sampleProject("rustproj", func(p *project.Project) { p.Language = "rust" })); err != nil {
		t.Fatal(err)
	}
	tool := buildHarnessRecommendTool(d)
	res := mustCall(t, tool, map[string]any{"slug": "rustproj"}).(harness.Recommendation)
	if res.Language != "rust" {
		t.Errorf("Language = %q, want rust (looked up from registry)", res.Language)
	}
}

func TestHarnessRecommend_LanguageBeatsSlug(t *testing.T) {
	d := newDeps(t)
	if err := d.Registry.Add(sampleProject("goproj")); err != nil { // Go
		t.Fatal(err)
	}
	tool := buildHarnessRecommendTool(d)
	// Explicit language must win over the slug's registered language.
	res := mustCall(t, tool, map[string]any{"slug": "goproj", "language": "python"}).(harness.Recommendation)
	if res.Language != "python" {
		t.Errorf("Language = %q, want python (explicit override)", res.Language)
	}
}

func TestHarnessRecommend_UnknownLanguageGenericFallback(t *testing.T) {
	tool := buildHarnessRecommendTool(newDeps(t))
	res := mustCall(t, tool, map[string]any{"language": "cobol"}).(harness.Recommendation)
	if res.Language != "cobol" {
		t.Errorf("Language = %q, want cobol (generic fallback preserves name)", res.Language)
	}
}

func TestHarnessRecommend_UnknownSlugNoLanguage_Errors(t *testing.T) {
	tool := buildHarnessRecommendTool(newDeps(t))
	// Slug not in registry and no language → invalid_input.
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"slug":"ghost"}`))
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestHarnessRecommend_NeitherSlugNorLanguage_Errors(t *testing.T) {
	tool := buildHarnessRecommendTool(newDeps(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestHarnessRecommend_BadJSON_Errors(t *testing.T) {
	tool := buildHarnessRecommendTool(newDeps(t))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{`))
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}
