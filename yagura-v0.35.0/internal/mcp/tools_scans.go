// tools_scans.go: security scan系 MCP tool(S1.3–S1.6)。
//
// yagura_secretscan / yagura_sbom / yagura_gha_audit / yagura_pin_drift と
// その専用 helper(projectFieldsAsScanItems / secretScanSeverityRank)。
// tools.go の topic 別分割(Roadmap #1)の一環で、対応するテストは従来から
// tools_scans_test.go にある。挙動・登録順は不変(RegisterDefaultTools は tools.go)。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shizukutanaka/yagura/internal/ghaaudit"
	"github.com/shizukutanaka/yagura/internal/pindrift"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/sbom"
	"github.com/shizukutanaka/yagura/internal/secretscan"
)

// ─── yagura_secretscan (S1.3 secret leak detection) ──────────

func buildSecretScanTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_secretscan",
		Description: "[G] Secret scan: 14 patterns + entropy. Redacts hits.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug": map[string]any{
					"type": "string",
				},
				"min_severity": map[string]any{
					"type": "string",
				},
				"custom_rules":  map[string]any{"type": "array", "description": "project-specific secret rules: [{id, pattern(regex), severity(CRITICAL|HIGH|MEDIUM|LOW), description?, entropy_min?, capture_idx?}]"},
				"disable_rules": map[string]any{"type": "array", "description": "built-in rule IDs to suppress (e.g. [\"aws-access-key-id\"])"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.SecretScanner == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "secretscanner not configured at startup"}
			}
			var in struct {
				Slug         string                `json:"slug"`
				MinSeverity  string                `json:"min_severity"`
				CustomRules  []secretscan.RuleSpec `json:"custom_rules"`
				DisableRules []string              `json:"disable_rules"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: "invalid args", Cause: err}
			}
			if len(in.CustomRules) > 200 {
				return nil, &ToolError{Code: "invalid_input", Message: "too many custom_rules (max 200)"}
			}

			// プロジェクト → ScanItem への変換
			projectsToScan, terr := secretScanTargets(d, in.Slug)
			if terr != nil {
				return nil, terr
			}

			items := make([]secretscan.ScanItem, 0, len(projectsToScan)*5)
			for _, p := range projectsToScan {
				items = append(items, projectFieldsAsScanItems(p)...)
			}

			// Default scanner unless the caller supplied custom/disabled rules.
			scanner, terr := secretScanScanner(d, in.CustomRules, in.DisableRules)
			if terr != nil {
				return nil, terr
			}

			result := scanner.ScanBatch(items)

			// min_severity フィルタ
			result, terr = filterSecretScanSeverity(result, in.MinSeverity)
			if terr != nil {
				return nil, terr
			}

			return map[string]any{
				"scanned_projects": len(projectsToScan),
				"sources_scanned":  len(items),
				"total_findings":   result.Total,
				"by_severity":      result.BySeverity,
				"by_source":        result.BySource,
				"source_order":     result.SourceOrder,
				"scanned_at":       d.Now().Format(time.RFC3339),
			}, nil
		},
	}
}

// secretScanTargets は scan 対象 project 群を返す(slug 指定なら 1 件、無指定なら
// archived を除く全件)。
func secretScanTargets(d Deps, slug string) ([]*project.Project, *ToolError) {
	if slug != "" {
		p, err := d.Registry.Get(slug)
		if err != nil {
			return nil, &ToolError{Code: "not_found", Message: "project not found: " + slug}
		}
		return []*project.Project{p}, nil
	}
	var out []*project.Project
	for _, p := range d.Registry.List() {
		if p.Stage != project.StageArchived {
			out = append(out, p)
		}
	}
	return out, nil
}

// secretScanScanner は custom/disable rules 指定があれば専用 scanner を、無ければ
// default scanner を返す。
func secretScanScanner(d Deps, custom []secretscan.RuleSpec, disable []string) (SecretScanner, *ToolError) {
	if len(custom) == 0 && len(disable) == 0 {
		return d.SecretScanner, nil
	}
	cfg := &secretscan.UserConfig{Rules: custom, Disable: disable}
	rules, err := cfg.Apply(secretscan.DefaultRules())
	if err != nil {
		return nil, &ToolError{Code: "invalid_input", Message: err.Error()}
	}
	return secretscan.NewWithRules(rules), nil
}

// filterSecretScanSeverity は min_severity 以上のみ残す(空なら無変更)。
func filterSecretScanSeverity(r secretscan.BatchResult, minSeverity string) (secretscan.BatchResult, *ToolError) {
	if minSeverity == "" {
		return r, nil
	}
	min := strings.ToUpper(minSeverity)
	if min != "LOW" && min != "MEDIUM" && min != "HIGH" && min != "CRITICAL" {
		return r, &ToolError{Code: "invalid_input",
			Message: "min_severity must be LOW/MEDIUM/HIGH/CRITICAL"}
	}
	return filterFindingsBatch(r, secretscan.Severity(min)), nil
}

// projectFieldsAsScanItems は Project のテキストフィールドを ScanItem 配列にする。
// 各フィールド = 1 source として扱う(検出時にどのフィールドかを特定するため)。
//
// 空文字フィールドはスキップ(スキャン対象を減らして高速化)。
func projectFieldsAsScanItems(p *project.Project) []secretscan.ScanItem {
	items := []secretscan.ScanItem{}
	add := func(field, text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		items = append(items, secretscan.ScanItem{
			Source: p.Slug + ":" + field,
			Text:   text,
		})
	}
	add("display_name", p.DisplayName)
	add("notes", p.Notes)
	add("tags", strings.Join(p.Tags, " "))
	if p.Sprint != nil {
		add("sprint.goal", p.Sprint.Goal)
		for i, m := range p.Sprint.Milestones {
			add(fmt.Sprintf("sprint.milestone[%d]", i), m.Title)
		}
	}
	return items
}

// filterFindingsBatch は min 以上の severity だけを残した新しい BatchResult を返す。
func filterFindingsBatch(r secretscan.BatchResult, min secretscan.Severity) secretscan.BatchResult {
	minRank := secretScanSeverityRank(min)
	out := secretscan.BatchResult{
		BySource:    map[string][]secretscan.Finding{},
		SourceOrder: []string{},
		BySeverity:  map[string]int{},
	}
	for _, src := range r.SourceOrder {
		var keep []secretscan.Finding
		for _, f := range r.BySource[src] {
			if secretScanSeverityRank(f.Severity) >= minRank {
				keep = append(keep, f)
				out.BySeverity[string(f.Severity)]++
				out.Total++
			}
		}
		if len(keep) > 0 {
			out.BySource[src] = keep
			out.SourceOrder = append(out.SourceOrder, src)
		}
	}
	return out
}

func secretScanSeverityRank(s secretscan.Severity) int {
	switch s {
	case secretscan.SeverityCritical:
		return 4
	case secretscan.SeverityHigh:
		return 3
	case secretscan.SeverityMedium:
		return 2
	case secretscan.SeverityLow:
		return 1
	}
	return 0
}

// ─── yagura_sbom (S1.4 SBOM continuous generation) ───────────

// SbomGenerator は SBOM 生成インターフェース。
// 実装は internal/sbom.Generator。
type SbomGenerator interface {
	Generate(mainPath, mainVersion string) (*sbom.Bom, error)
}

func buildSbomTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_sbom",
		Description: "[G] yagura SBOM CycloneDX 1.5. Set summary_only for compact.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary_only": map[string]any{
					"type": "boolean",
				},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.Sbom == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "sbom generator not configured"}
			}
			var in struct {
				SummaryOnly bool `json:"summary_only"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, &ToolError{Code: "invalid_input",
						Message: "invalid arguments", Cause: err}
				}
			}
			bom, err := d.Sbom.Generate(d.MainModulePath, d.MainVersion)
			if err != nil {
				return nil, &ToolError{Code: "internal",
					Message: "sbom generation failed", Cause: err}
			}
			if in.SummaryOnly {
				return bom.Summarize(), nil
			}
			return bom, nil
		},
	}
}

// ─── yagura_gha_audit (S1.5 GitHub Actions hardening audit) ──

// GhaAuditor は GitHub Actions workflow YAML の静的解析インターフェース。
// 実装は internal/ghaaudit.Auditor。
type GhaAuditor interface {
	AuditFile(filePath, content string) []ghaaudit.Finding
	AuditDir(dir string, files map[string]string) map[string][]ghaaudit.Finding
}

func buildGhaAuditTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_gha_audit",
		Description: "[G] GHA workflow audit: 12 supply-chain risk patterns.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type": "object",
				},
				"summary_only": map[string]any{
					"type": "boolean",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.Ghaaudit == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "gha auditor not configured"}
			}
			var in struct {
				Files       map[string]string `json:"files"`
				SummaryOnly bool              `json:"summary_only"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input",
					Message: "files map[string]string required", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input",
					Message: "files must contain at least one file"}
			}
			results := d.Ghaaudit.AuditDir(".", in.Files)
			if in.SummaryOnly {
				return ghaaudit.Summarize(results), nil
			}
			return map[string]any{
				"results": results,
				"summary": ghaaudit.Summarize(results),
			}, nil
		},
	}
}

// ─── yagura_pin_drift (S1.6 SHA pin drift detection) ─────────

// PinDriftChecker は SHA pin の drift 検出インターフェース。
// 実装は internal/pindrift.Checker。
type PinDriftChecker interface {
	CheckPins(ctx context.Context, pins []pindrift.Pin) []pindrift.Result
	CheckPinsParallel(ctx context.Context, pins []pindrift.Pin, concurrency int) []pindrift.Result
}

func buildPinDriftTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_pin_drift",
		Description: "[G] GHA SHA pin verify via API. Detects drift/stale.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type": "object",
				},
				"summary_only": map[string]any{
					"type": "boolean",
				},
				"concurrency": map[string]any{
					"type": "integer",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.PinDrift == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "pin drift checker not configured"}
			}
			var in struct {
				Files       map[string]string `json:"files"`
				SummaryOnly bool              `json:"summary_only"`
				Concurrency int               `json:"concurrency"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input",
					Message: "files map[string]string required", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input",
					Message: "files must contain at least one file"}
			}
			// 全 file から pin を抽出
			var allPins []pindrift.Pin
			for path, content := range in.Files {
				allPins = append(allPins, pindrift.ExtractPins(path, content)...)
			}
			if len(allPins) == 0 {
				return map[string]any{
					"results": []pindrift.Result{},
					"summary": pindrift.Summary{ByStatus: map[string]int{}},
					"note":    "no SHA-pinned uses: found (try yagura_gha_audit first)",
				}, nil
			}
			// concurrency が指定されていれば並列実行(デフォルト 4)
			var results []pindrift.Result
			if in.Concurrency > 0 {
				results = d.PinDrift.CheckPinsParallel(ctx, allPins, in.Concurrency)
			} else if in.Concurrency == 0 {
				// 明示的に 0 指定または未指定の場合はデフォルト並列度 4 で実行
				results = d.PinDrift.CheckPinsParallel(ctx, allPins, 4)
			} else {
				// 負値は serial
				results = d.PinDrift.CheckPins(ctx, allPins)
			}
			if in.SummaryOnly {
				return pindrift.Summarize(results), nil
			}
			return map[string]any{
				"results": results,
				"summary": pindrift.Summarize(results),
			}, nil
		},
	}
}

