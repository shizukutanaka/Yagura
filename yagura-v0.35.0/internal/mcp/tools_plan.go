// tools_plan.go: Plan.md aware orchestration 系 MCP tool(v0.24–v0.25)。
//
// yagura_plan_status / yagura_release_radar と専用 helper(loadPlanMd /
// scanProjectAICode / pickReason)。m's harness G1.P の必須記載項目を
// 23+ projects 横断で計測し release 順をランク付けする層。tools.go の
// topic 別分割(Roadmap #1)の一環。登録順は不変。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shizukutanaka/yagura/internal/aiverify"
	"github.com/shizukutanaka/yagura/internal/plantracker"
)

// ─── yagura_plan_status / yagura_release_radar (v0.24.0) ─────
//
// Plan.md aware portfolio orchestration. m's harness G1.P で defined された
// 必須記載項目 (目的/スコープ/フェーズ/DoD) を 23+ projects 横断で計測し、
// "次に release すべき project" を機械的にランク付け。

func buildPlanStatusTool(d Deps, cache plantracker.CacheLike) *Tool {
	return &Tool{
		Name:        "yagura_plan_status",
		Description: "[G] Plan.md progress for project. Parses checkboxes + required sections (目的/スコープ/フェーズ/DoD).",
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
			p, err := d.Registry.Get(in.Slug)
			if err != nil {
				return nil, &ToolError{Code: "not_found", Message: "project not registered"}
			}
			content, path, err := loadPlanMd(p.LocalPath)
			if err != nil {
				return map[string]any{
					"slug":    in.Slug,
					"plan_md": "",
					"error":   err.Error(),
				}, nil
			}
			state, _ := plantracker.ParseCached(content, cache)
			return map[string]any{
				"slug":    in.Slug,
				"plan_md": path,
				"state":   state,
				"summary": state.Summary(),
			}, nil
		},
	}
}

// loadPlanMd は LocalPath 配下から Plan.md / PLAN.md / plan.md を順に試行する。
//
// 戻り値: (content, found path, error)。LocalPath 空 or 全候補無し→ error。
// path traversal 防止: LocalPath は registry validate 済み(プロジェクトの project.Validate)、
// ファイル名は固定 list なので新たな攻撃ベクタなし。
func loadPlanMd(localPath string) (string, string, error) {
	if localPath == "" {
		return "", "", fmt.Errorf("project has no local_path")
	}
	candidates := []string{"Plan.md", "PLAN.md", "plan.md"}
	for _, name := range candidates {
		full := filepath.Join(localPath, name)
		if data, err := os.ReadFile(full); err == nil {
			return string(data), full, nil
		}
	}
	return "", "", fmt.Errorf("no Plan.md / PLAN.md / plan.md found in %s", localPath)
}

func buildReleaseRadarTool(d Deps, cache plantracker.CacheLike) *Tool {
	return &Tool{
		Name:        "yagura_release_radar",
		Description: "[S] Cross-project release readiness ranking. Aggregates Plan/CI/issues/quality/AI-risk.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit":     map[string]any{"type": "integer"},
				"scan_code": map[string]any{"type": "boolean"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Limit    int  `json:"limit"`
				ScanCode bool `json:"scan_code"` // v0.25: true なら aiverify scan
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, &ToolError{Code: "invalid_input",
						Message: "invalid arguments", Cause: err}
				}
			}
			if in.Limit <= 0 {
				in.Limit = 10
			}
			projects := d.Registry.List()
			items := make([]plantracker.RankedProject, 0, len(projects))
			for _, p := range projects {
				if p.LocalPath == "" {
					continue
				}
				content, _, err := loadPlanMd(p.LocalPath)
				if err != nil {
					continue
				}
				plan, _ := plantracker.ParseCached(content, cache)
				ciStatus := string(p.CIStatus)
				if ciStatus == "" {
					ciStatus = "unknown"
				}
				openCrit := p.VulnCritical

				// v0.25.0: scan_code=true なら aiverify を呼んで AI risk を統合
				var aiResult aiverify.Result
				if in.ScanCode {
					aiResult = scanProjectAICode(p.LocalPath)
				}

				readiness := plantracker.ReleaseReadinessExt(plan, ciStatus, openCrit,
					false, aiResult.RiskScore, aiResult.HasCritical)
				reason := pickReason(plan, ciStatus, openCrit, aiResult.HasCritical, aiResult.RiskScore)
				items = append(items, plantracker.RankedProject{
					Slug:               p.Slug,
					Readiness:          readiness,
					PlanProgressPct:    plan.ProgressPct,
					CurrentPhase:       plan.CurrentPhase,
					CIStatus:           ciStatus,
					OpenIssuesCritical: openCrit,
					AIRiskScore:        aiResult.RiskScore,
					AIHasCritical:      aiResult.HasCritical,
					AIGenLineCount:     aiResult.AIGenLines,
					Reason:             reason,
				})
			}
			ranked := plantracker.Rank(items)
			if len(ranked) > in.Limit {
				ranked = ranked[:in.Limit]
			}
			return map[string]any{
				"ranked":          ranked,
				"total_projects":  len(projects),
				"projects_scored": len(items),
				"scan_code":       in.ScanCode,
			}, nil
		},
	}
}

// scanProjectAICode は LocalPath 配下の主要 source file を aiverify で scan する。
//
// 完全 walk は重いので、上位 N file (size 制限あり) のみ。
// 言語: Go / Python / TS / JS / Rust。
// 各 file 最大 256KB、合計 64 file まで(暴走防止)。
func scanProjectAICode(localPath string) aiverify.Result {
	const maxFiles = 64
	const maxFileSize = 256 * 1024

	files := map[string]string{}
	walked := 0
	_ = filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if walked >= maxFiles {
			return filepath.SkipDir
		}
		// 隠しディレクトリと vendor / node_modules を skip
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
			return nil
		}
		ext := filepath.Ext(path)
		switch ext {
		case ".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".rs":
		default:
			return nil
		}
		if info.Size() > maxFileSize {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(localPath, path)
		files[rel] = string(data)
		walked++
		return nil
	})
	if len(files) == 0 {
		return aiverify.Result{}
	}
	return aiverify.Scan(files)
}

// pickReason は readiness 阻害の最大要因を 1 文で返す。
func pickReason(plan plantracker.PlanState, ciStatus string, openCrit int,
	aiCritical bool, aiRisk int) string {
	if aiCritical {
		return "AI-generated critical risk (review required)"
	}
	if openCrit > 0 {
		return fmt.Sprintf("%d critical issues blocking", openCrit)
	}
	if strings.EqualFold(ciStatus, "failing") {
		return "CI failing"
	}
	if !plan.IsHealthy {
		return "Plan.md missing required sections"
	}
	if plan.ProgressPct < 100 {
		return fmt.Sprintf("plan %d%% remaining", 100-plan.ProgressPct)
	}
	return "ready to release"
}
