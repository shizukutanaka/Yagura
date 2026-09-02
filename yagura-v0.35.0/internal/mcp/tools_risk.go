// tools_risk.go: yagura_risk_triage — Cyber Risk Reasoning Layer。
//
// 「AIセキュリティは検知ロジックから複合判断へ」— CVSS 単体ではなく、脆弱性深刻度 +
// 資産の業務重要度 + 到達可能性 + 攻撃可能性 + 横展開 blast radius + パッチ業務影響を
// 合わせて修正優先度を出す(internal/riskreason)。Yagura は LLM を使わず rule-based /
// deterministic に判断し、根拠(factors)と評価できなかった文脈(unknowns)を必ず返す
// ので、SOC/CSIRT/脆弱性管理/経営が検証できる(Human-in-the-Loop / audit 前提)。
//
// findings(CVE+CVSS 等、yagura_vulns の出力を流せる)を入力に取り、slug が registry に
// あれば asset_priority/stage/tags を自動補完し、projectgraph の Impact で blast radius
// (依存元数)を埋める。これが記事の言う「技術ログ・脆弱性・到達性・業務影響を 1 つの
// 説明可能な判断にまとめる」層にあたる。

package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shizukutanaka/yagura/internal/projectgraph"
	"github.com/shizukutanaka/yagura/internal/riskreason"
)

// assetContext は risk triage の資産文脈(caller 提供 or registry 解決)。
type assetContext struct {
	priority     int
	stage        string
	tags         []string
	dependents   int
	resolvedSlug string
}

// resolveAssetContext は slug が registry にあれば priority/stage/tags/dependents を
// 上書き補完する。解決できなければ caller 提供値のままにし、warning を返す。
func resolveAssetContext(d Deps, slug string, asset *assetContext) []string {
	if slug == "" {
		return nil
	}
	if d.Registry != nil {
		if p, err := d.Registry.Get(slug); err == nil && p != nil {
			asset.resolvedSlug = p.Slug
			asset.priority = p.Priority
			asset.stage = string(p.Stage)
			asset.tags = p.Tags
			g := projectgraph.Build(toGraphProjects(d.Registry.List()))
			asset.dependents = g.Impact(p.Slug).ImpactCount
			return nil
		}
	}
	// slug が解決しないと caller 提供の文脈を黙って使ってしまうため明示する。
	return []string{fmt.Sprintf(
		"slug %q not found in registry; using caller-supplied asset context (priority/stage/tags/dependents)", slug)}
}

func buildRiskTriageTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_risk_triage",
		Title:       "Risk Triage",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Composite fix-priority for CVEs (asset+reach+exploit, with rationale).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"findings": map[string]any{
					"type":        "array",
					"description": "Vulnerabilities to triage. Each: {cve, cvss?, severity?, internet_exposed?, auth_required?, waf_protected?, known_exploited?, public_exploit?, patch_blocks_business?}.",
					"items":       map[string]any{"type": "object"},
				},
				"slug": map[string]any{
					"type":        "string",
					"description": "Registered asset slug — auto-fills asset_priority/stage/tags and blast radius (dependents) from the registry + dependency graph.",
				},
				// slug が無い/未登録のときに資産文脈を明示する任意フィールド。
				"asset_priority": map[string]any{"type": "integer"},
				"stage":          map[string]any{"type": "string"},
				"tags":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"dependents":     map[string]any{"type": "integer"},
				"weights": map[string]any{
					"type":        "object",
					"description": "Optional partial override of scoring weights (merged over defaults), e.g. {\"known_exploited\": 40, \"band_now\": 80}.",
				},
			},
			"required": []string{"findings"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Findings []struct {
					CVE                 string  `json:"cve"`
					CVSS                float64 `json:"cvss"`
					Severity            string  `json:"severity"`
					InternetExposed     *bool   `json:"internet_exposed"`
					AuthRequired        *bool   `json:"auth_required"`
					WAFProtected        *bool   `json:"waf_protected"`
					KnownExploited      *bool   `json:"known_exploited"`
					PublicExploit       *bool   `json:"public_exploit"`
					EPSS                float64 `json:"epss"`
					Automatable         *bool   `json:"automatable"`
					PatchBlocksBusiness *bool   `json:"patch_blocks_business"`
				} `json:"findings"`
				Slug          string          `json:"slug"`
				AssetPriority int             `json:"asset_priority"`
				Stage         string          `json:"stage"`
				Tags          []string        `json:"tags"`
				Dependents    int             `json:"dependents"`
				Weights       json.RawMessage `json:"weights"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Findings) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "at least one finding is required"}
			}
			// 任意の weights override: DefaultWeights() の上に部分 JSON を被せる
			// (未指定 field は既定のまま)。組織のリスク許容度に合わせた custom rule。
			weights := riskreason.DefaultWeights()
			if len(in.Weights) > 0 {
				if err := json.Unmarshal(in.Weights, &weights); err != nil {
					return nil, &ToolError{Code: "invalid_input", Message: "invalid weights override: " + err.Error()}
				}
			}

			// slug があれば registry + graph から資産文脈を補完。
			asset := assetContext{priority: in.AssetPriority, stage: in.Stage, tags: in.Tags, dependents: in.Dependents}
			warnings := resolveAssetContext(d, in.Slug, &asset)

			inputs := make([]riskreason.Input, 0, len(in.Findings))
			for _, f := range in.Findings {
				inputs = append(inputs, riskreason.Input{
					CVE: f.CVE, CVSS: f.CVSS, Severity: f.Severity,
					AssetPriority: asset.priority, Stage: asset.stage, Tags: asset.tags,
					InternetExposed: f.InternetExposed, AuthRequired: f.AuthRequired, WAFProtected: f.WAFProtected,
					KnownExploited: f.KnownExploited, PublicExploit: f.PublicExploit,
					EPSS: f.EPSS, Automatable: f.Automatable,
					Dependents: asset.dependents, PatchBlocksBusiness: f.PatchBlocksBusiness,
				})
			}
			results := riskreason.ScoreAllWith(inputs, weights)

			// priority 別サマリ(決定論)。
			summary := map[string]int{}
			for _, r := range results {
				summary[string(r.Priority)]++
			}
			out := map[string]any{
				"asset": map[string]any{
					"slug":           asset.resolvedSlug,
					"asset_priority": asset.priority,
					"stage":          asset.stage,
					"dependents":     asset.dependents,
				},
				"triaged": results,
				"summary": summary,
			}
			if len(warnings) > 0 {
				out["warnings"] = warnings
			}
			return out, nil
		},
	}
}
