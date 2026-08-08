// tools_guides.go: v0.32 bilateral harness の Guide(feedforward)系 MCP tool。
//
// yagura_agents_md / yagura_feature_list / yagura_harness_coverage と
// Plan.md 抽出 helper(extractSection / extractDoDItems /
// planStateToFeatureInput — 後者は progress_file からも参照される)。
// tools.go の topic 別分割(Roadmap #1)の一環。登録順は不変。
// 共有 infra(version() / atomicWriteFile)は tools.go に残す。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shizukutanaka/yagura/internal/agentmd"
	"github.com/shizukutanaka/yagura/internal/featurelist"
	"github.com/shizukutanaka/yagura/internal/plantracker"
)

// ─── v0.32.0: Bilateral harness — guides (feedforward) ────────────
//
// Martin Fowler's harness taxonomy: Computational × Inferential × Guide ×
// Sensor. yagura v0.31 had 8 sensors but 0 guides (feedback-only). These
// tools add the missing Inferential Guide axis.

func buildAgentsMdTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_agents_md",
		Title:       "Generate Agent Guide",
		Annotations: &ToolAnnotations{ReadOnlyHint: false, DestructiveHint: true, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Generate AGENTS.md for a registered project from Plan.md + registry facts. Cross-tool: Claude Code / Codex / Cursor.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":          map[string]any{"type": "string"},
				"include_rules": map[string]any{"type": "boolean", "description": "Include house rules section (default true)"},
				"write":         map[string]any{"type": "boolean", "description": "Also write to {local_path}/AGENTS.md (v0.33.0)"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug         string `json:"slug"`
				IncludeRules *bool  `json:"include_rules"`
				Write        bool   `json:"write"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug required"}
			}
			p, err := d.Registry.Get(in.Slug)
			if err != nil {
				return nil, &ToolError{Code: "not_found", Message: "project not registered"}
			}
			facts := agentmd.ProjectFacts{
				Slug:         p.Slug,
				DisplayName:  p.DisplayName,
				Repository:   p.Repository,
				Language:     p.Language,
				Stage:        string(p.Stage),
				LocalPath:    p.LocalPath,
				Tags:         p.Tags,
				DependsOn:    p.DependsOn,
				CIStatus:     string(p.CIStatus),
				OpenIssues:   p.OpenIssues,
				OpenPRs:      p.OpenPRs,
				VulnCritical: p.VulnCritical,
				VulnHigh:     p.VulnHigh,
				GeneratedBy:  "yagura " + version(),
			}
			// Plan.md があれば parse して description/phases を埋める
			// (DoD / Purpose / Scope の細粒度抽出は plantracker 拡張待ち)
			if p.LocalPath != "" {
				if content, _, err := loadPlanMd(p.LocalPath); err == nil {
					enrichFactsFromPlan(&facts, content)
				}
			}
			body := agentmd.Generate(facts)
			result := map[string]any{
				"slug":     in.Slug,
				"body":     body,
				"length":   len(body),
				"filename": "AGENTS.md",
			}
			if in.Write && p.LocalPath != "" {
				path := filepath.Join(p.LocalPath, "AGENTS.md")
				if err := atomicWriteFile(path, []byte(body), 0o644); err != nil {
					return nil, &ToolError{Code: "write_failed", Message: err.Error()}
				}
				result["written_to"] = path
			}
			return result, nil
		},
	}
}

// enrichFactsFromPlan は Plan.md 本文から phases / description / scope / DoD を
// 抽出して facts に詰める。plantracker は全 ## を Phase とみなすので、真の Phase
// ("Phase"/"フェーズ" header)配下のみを phases に採用する。
func enrichFactsFromPlan(facts *agentmd.ProjectFacts, content string) {
	state := plantracker.Parse(content)
	for _, ph := range state.Phases {
		if !isPhaseSection(ph.Name) {
			continue
		}
		facts.Phases = append(facts.Phases,
			fmt.Sprintf("%s (%d/%d)", ph.Name, ph.CompletedTasks, ph.TotalTasks))
	}
	facts.Description = extractSection(content, []string{"目的", "Purpose"})
	facts.Scope = extractSection(content, []string{"スコープ", "Scope"})
	facts.DoD = extractDoDItems(content)
}

func buildFeatureListTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_feature_list",
		Title:       "Generate Feature List",
		Annotations: &ToolAnnotations{ReadOnlyHint: false, DestructiveHint: true, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Convert Plan.md into Anthropic-style feature-list.json for long-running agent harnesses.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":  map[string]any{"type": "string"},
				"write": map[string]any{"type": "boolean", "description": "Also write to {local_path}/feature-list.json (v0.33.0)"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug  string `json:"slug"`
				Write bool   `json:"write"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug required"}
			}
			p, err := d.Registry.Get(in.Slug)
			if err != nil {
				return nil, &ToolError{Code: "not_found", Message: "project not registered"}
			}
			if p.LocalPath == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "project has no local_path; cannot read Plan.md"}
			}
			content, _, err := loadPlanMd(p.LocalPath)
			if err != nil {
				return nil, &ToolError{Code: "not_found", Message: "Plan.md not found", Cause: err}
			}
			state := plantracker.Parse(content)
			pin := planStateToFeatureInput(in.Slug, content, state)
			fl := featurelist.Build(pin, nil)
			result := map[string]any{
				"slug":         in.Slug,
				"feature_list": fl,
				"stats":        fl.Stats,
				"filename":     "feature-list.json",
			}
			if in.Write {
				raw, err := featurelist.Marshal(fl)
				if err != nil {
					return nil, &ToolError{Code: "write_failed", Message: err.Error()}
				}
				path := filepath.Join(p.LocalPath, "feature-list.json")
				if err := atomicWriteFile(path, raw, 0o644); err != nil {
					return nil, &ToolError{Code: "write_failed", Message: err.Error()}
				}
				result["written_to"] = path
			}
			return result, nil
		},
	}
}

// ─── v0.32.0: Harness coverage self-audit ────────────────────────
//
// Fowler taxonomy: report which Computational × Inferential × Guide × Sensor
// quadrants yagura covers for this portfolio. Surfaces the formerly hidden
// "yagura has 0 guides" gap — and confirms it's resolved.

func buildHarnessCoverageTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_harness_coverage",
		Title:       "Harness Coverage Report",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Self-audit: which Fowler taxonomy quadrants does yagura cover? Returns guides/sensors × computational/inferential matrix.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			// 自分自身が提供する harness 要素を列挙する。
			// 既存 MCP tool を taxonomy にマップ:
			matrix := map[string]map[string][]string{
				"guide": {
					"computational": {
						"yagura_feature_list (Plan.md → feature-list.json scaffold)",
					},
					"inferential": {
						"yagura_agents_md (AGENTS.md scaffold for Claude Code/Codex/Cursor)",
						"yagura_harness_recommend (per-project guidance)",
						"yagura_skill_audit / yagura_subagent_audit (skill scaffolding)",
					},
				},
				"sensor": {
					"computational": {
						"yagura_quality_check (static code analysis)",
						"yagura_secretscan (secret detection)",
						"yagura_gha_audit (workflow audit)",
						"yagura_pin_drift (dep pin drift)",
						"yagura_ai_verify (AI-generated code patterns)",
						"yagura_test_audit (source-test coverage)",
						"yagura_vulns (OSV.dev)",
						"yagura_scorecard (OpenSSF)",
						"yagura_sbom (CycloneDX)",
					},
					"inferential": {
						"(intentionally none — ADR-0001 zero-dep precludes LLM-as-judge in-process)",
					},
				},
			}
			counts := map[string]int{}
			for axis, ci := range matrix {
				for kind, tools := range ci {
					counts[axis+"_"+kind] = len(tools)
				}
			}
			return map[string]any{
				"taxonomy_source":       "https://martinfowler.com/articles/harness-engineering.html",
				"matrix":                matrix,
				"counts":                counts,
				"feedback_only_warning": false, // v0.32 で解消
				"note":                  "yagura intentionally leaves the inferential-sensor quadrant empty (LLM-as-judge would violate ADR-0001 zero-deps). External Claude/GPT review can be plugged in via Claude Code subagents and reported back via /hooks/claude-code.",
			}, nil
		},
	}
}

// extractSection は Plan.md の本文から `## <header>` 直下の段落(空行か次の
// ## まで)を抜き出す。複数候補があれば最初に見つかった方を返す。
//
// v0.32 で agentmd が Purpose/Scope を埋めるために必要。plantracker は
// section の有無 (HasPurpose/HasScope) のみ持ち、本文は持っていなかった。
func extractSection(content string, headers []string) string {
	lines := strings.Split(content, "\n")
	for _, h := range headers {
		for i, line := range lines {
			ts := strings.TrimSpace(line)
			if !strings.HasPrefix(ts, "##") {
				continue
			}
			rest := strings.TrimSpace(strings.TrimLeft(ts, "#"))
			if !strings.EqualFold(rest, h) {
				continue
			}
			// 次の ## まで集める
			var body []string
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "##") {
					break
				}
				body = append(body, lines[j])
			}
			out := strings.TrimSpace(strings.Join(body, "\n"))
			if out != "" {
				return out
			}
		}
	}
	return ""
}

// extractDoDItems は DoD / 完了定義 / Definition of Done section の bullet を
// list で返す。featurelist の acceptance_criteria + agentmd の DoD 表示に使う。
func extractDoDItems(content string) []string {
	body := extractSection(content, []string{"完了定義", "Definition of Done", "DoD"})
	if body == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(body, "\n") {
		ts := strings.TrimSpace(line)
		// "- [ ] xxx", "- [x] xxx", "- xxx" 全て対象
		if !strings.HasPrefix(ts, "- ") && !strings.HasPrefix(ts, "* ") {
			continue
		}
		item := strings.TrimSpace(ts[2:])
		// checkbox を除去
		if strings.HasPrefix(item, "[ ]") || strings.HasPrefix(item, "[x]") || strings.HasPrefix(item, "[X]") {
			item = strings.TrimSpace(item[3:])
		}
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// planStateToFeatureInput は plantracker.PlanState + 元 content から
// featurelist.PlanInput を組み立てる。
//
// plantracker は phase 単位の集計のみ持ち、個別 task title は捨てているので、
// content を再 scan して checkbox 行を拾う。
//
// "フェーズ" / "Phase" header の子のみを feature とみなす(他の section の
// checkbox は features にしない — DoD は acceptance_criteria 側に集約済)。
func planStateToFeatureInput(project, content string, state plantracker.PlanState) featurelist.PlanInput {
	pin := featurelist.PlanInput{
		Project: project,
		DoD:     extractDoDItems(content),
	}
	lines := strings.Split(content, "\n")
	for i, ph := range state.Phases {
		if !isPhaseSection(ph.Name) {
			continue
		}
		startLine := ph.LineStart // 1-indexed (plantracker convention)
		endLine := len(lines)
		if i+1 < len(state.Phases) {
			endLine = state.Phases[i+1].LineStart - 1
		}
		phIn := featurelist.PhaseInput{Name: ph.Name, Tasks: extractPhaseTasks(lines, startLine, endLine)}
		if len(phIn.Tasks) > 0 {
			pin.Phases = append(pin.Phases, phIn)
		}
	}
	return pin
}

// isPhaseSection は header 名が真の Phase("Phase"/"フェーズ"を含む)かを判定する。
func isPhaseSection(name string) bool {
	return strings.Contains(strings.ToLower(name), "phase") || strings.Contains(name, "フェーズ")
}

// extractPhaseTasks は lines[startLine:endLine] から checkbox 行を TaskInput に変換する。
func extractPhaseTasks(lines []string, startLine, endLine int) []featurelist.TaskInput {
	var tasks []featurelist.TaskInput
	for j := startLine; j < endLine && j < len(lines); j++ {
		item, done, ok := parseCheckboxLine(strings.TrimSpace(lines[j]))
		if !ok || item == "" {
			continue
		}
		tasks = append(tasks, featurelist.TaskInput{Title: item, Done: done})
	}
	return tasks
}

// parseCheckboxLine は "- [x] foo" / "* [ ] bar" 形式の checkbox 行を解析する。
// 戻り値: (item text, done?, このフォーマットに合致したか)。
func parseCheckboxLine(ts string) (item string, done bool, ok bool) {
	if !strings.HasPrefix(ts, "- [") && !strings.HasPrefix(ts, "* [") {
		return "", false, false
	}
	switch {
	case strings.HasPrefix(ts, "- [x]") || strings.HasPrefix(ts, "* [x]") ||
		strings.HasPrefix(ts, "- [X]") || strings.HasPrefix(ts, "* [X]"):
		return strings.TrimSpace(ts[5:]), true, true
	case strings.HasPrefix(ts, "- [ ]") || strings.HasPrefix(ts, "* [ ]"):
		return strings.TrimSpace(ts[5:]), false, true
	default:
		return "", false, false
	}
}
