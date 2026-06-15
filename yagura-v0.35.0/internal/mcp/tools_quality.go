// tools_quality.go: code 品質ゲート系 MCP tool(cortex flywheel ② Review)。
//
// yagura_quality_check(v0.19 逸脱検出ゲート)/ yagura_ai_verify(v0.25
// AI-generated code の risk pattern 検出)/ yagura_test_audit(v0.26
// source-test 対応検出)と format helper。tools.go の topic 別分割
// (Roadmap #1)の一環。登録順は不変。
package mcp

import (
	"context"
	"encoding/json"

	"github.com/shizukutanaka/yagura/internal/aiverify"
	"github.com/shizukutanaka/yagura/internal/assertcheck"
	"github.com/shizukutanaka/yagura/internal/astcheck"
	"github.com/shizukutanaka/yagura/internal/errpolicy"
	"github.com/shizukutanaka/yagura/internal/qualitycheck"
	"github.com/shizukutanaka/yagura/internal/testcoverage"
)


// ─── yagura_quality_check (v0.19.0) ───────────────────────────
//
// cortex (aircloset 2026/05) 翻訳: 「逸脱を物理的に潰す」品質ゲート。
// `as any` / @ts-ignore / eslint-disable / TODO / FIXME 等を line-based
// regex で検出し、severity 別に集計する。CI で `prohibited > 0` なら fail
// するのが想定運用。

func buildQualityCheckTool(d Deps, cache qualitycheck.CacheLike) *Tool {
	return &Tool{
		Name:        "yagura_quality_check",
		Description: "[G] Code lint: as any, ts-ignore, TODO. 3 severity tiers.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files":        map[string]any{"type": "object"},
				"text":         map[string]any{"type": "string"},
				"language":     map[string]any{"type": "string"},
				"summary_only": map[string]any{"type": "boolean"},
				"custom_rules": map[string]any{"type": "array", "description": "project-specific lint rules: [{id, pattern(regex), severity(prohibited|warning|info), languages?, description?, suggestion?}]"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files       map[string]string       `json:"files"`
				Text        string                  `json:"text"`
				Language    string                  `json:"language"`
				SummaryOnly bool                    `json:"summary_only"`
				CustomRules []qualitycheck.RuleSpec `json:"custom_rules"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 && in.Text == "" {
				return nil, &ToolError{Code: "invalid_input",
					Message: "either 'files' or 'text' is required"}
			}
			if len(in.CustomRules) > 200 {
				return nil, &ToolError{Code: "invalid_input", Message: "too many custom_rules (max 200)"}
			}
			rules := qualitycheck.DefaultRules()
			if len(in.CustomRules) > 0 {
				custom, err := qualitycheck.CompileRules(in.CustomRules)
				if err != nil {
					return nil, &ToolError{Code: "invalid_input", Message: err.Error()}
				}
				rules = append(rules, custom...)
			}

			if in.Text != "" {
				findings := qualitycheck.ScanText("<input>", in.Text, in.Language, rules)
				res := qualitycheck.Result{
					FilesScanned: 1,
					Findings:     findings,
					BySeverity:   map[qualitycheck.Severity]int{},
					ByRule:       map[string]int{},
				}
				for _, f := range findings {
					res.BySeverity[f.Severity]++
					res.ByRule[f.RuleID]++
				}
				return formatQualityResult(res, in.SummaryOnly), nil
			}
			// v0.23.0: cache 統合(content-hash で同 content の再 scan を skip)
			res := qualitycheck.ScanFilesCached(in.Files, rules, cache)
			return formatQualityResult(res, in.SummaryOnly), nil
		},
	}
}

// formatQualityResult is MCP-friendly output formatter.
func formatQualityResult(res qualitycheck.Result, summaryOnly bool) any {
	out := map[string]any{
		"files_scanned":  res.FilesScanned,
		"total_lines":    res.TotalLines,
		"finding_count":  len(res.Findings),
		"by_severity":    res.BySeverity,
		"by_rule":        res.ByRule,
		"has_prohibited": res.HasProhibited(),
		"summary":        res.Summary(),
	}
	if !summaryOnly {
		out["findings"] = res.Findings
		if len(res.ByFile) > 0 {
			out["by_file"] = res.ByFile
		}
	}
	return out
}


// ─── yagura_ai_verify (v0.25.0) ──────────────────────────────
//
// AI-generated コードの risk pattern を検出する。m's harness G0.7 INVARIANT
// 「AI出力検証義務」への直接対応。
//
// Veracode 2025: 45% of AI code ships OWASP Top-10
// Apiiro Fortune 50: 322% privilege-escalation paths, 40% secrets exposure
// CodeRabbit: 1.7× issue multiplier — AI zone 内 finding は score 2x。

func buildAIVerifyTool(d Deps, cache aiverify.CacheLike) *Tool {
	return &Tool{
		Name:        "yagura_ai_verify",
		Description: "[G] AI code risk audit. 6 categories: auth/billing/data/external/crypto/secret. 2x multiplier inside AI-marker zones.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files":        map[string]any{"type": "object"},
				"text":         map[string]any{"type": "string"},
				"path":         map[string]any{"type": "string"},
				"summary_only": map[string]any{"type": "boolean"},
				"custom_rules": map[string]any{"type": "array", "description": "project-specific AI risk rules: [{id, pattern(regex), category, risk(CRITICAL|HIGH|MEDIUM|LOW), message, languages?}]"},
				"disable_rules": map[string]any{"type": "array", "description": "built-in rule IDs to suppress (e.g. [\"billing-stripe-uncaught\"])"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files        map[string]string  `json:"files"`
				Text         string             `json:"text"`
				Path         string             `json:"path"`
				SummaryOnly  bool               `json:"summary_only"`
				CustomRules  []aiverify.UserRule `json:"custom_rules"`
				DisableRules []string           `json:"disable_rules"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 && in.Text == "" {
				return nil, &ToolError{Code: "invalid_input",
					Message: "either 'files' or 'text' is required"}
			}
			if len(in.CustomRules) > 200 {
				return nil, &ToolError{Code: "invalid_input", Message: "too many custom_rules (max 200)"}
			}

			// Build effective rule set (defaults ± user config).
			rules := aiverify.DefaultRules()
			if len(in.CustomRules) > 0 || len(in.DisableRules) > 0 {
				userCfg := &aiverify.UserConfig{Rules: in.CustomRules, Disable: in.DisableRules}
				var err error
				rules, err = userCfg.Apply(aiverify.DefaultRules())
				if err != nil {
					return nil, &ToolError{Code: "invalid_input", Message: err.Error()}
				}
			}

			var res aiverify.Result
			if in.Text != "" {
				path := in.Path
				if path == "" {
					path = "<input>"
				}
				res = aiverify.ScanWithRules(map[string]string{path: in.Text}, rules)
			} else if cache != nil && len(in.CustomRules) == 0 && len(in.DisableRules) == 0 {
				// Cache is only safe when the rule set is the default (cache key is
				// content-based and does not encode custom rules).
				res = aiverify.ScanCached(in.Files, cache)
			} else {
				res = aiverify.ScanWithRules(in.Files, rules)
			}

			// v0.26.0: testcoverage と結合し untested AI 生成を flag (+5/file)
			// files が複数与えられた場合のみ意味があるので text mode は skip
			if len(in.Files) > 0 {
				tcRes := testcoverage.Audit(in.Files)
				hasTestMap := make(map[string]bool, len(in.Files))
				// untested ではない source を has_test 認定
				untestedSet := make(map[string]bool, len(tcRes.UntestedFiles))
				for _, p := range tcRes.UntestedFiles {
					untestedSet[p] = true
				}
				for p := range in.Files {
					if testcoverage.IsTestFile(p) {
						continue
					}
					if !untestedSet[p] {
						hasTestMap[p] = true
					}
				}
				res = aiverify.AnnotateUntested(res, in.Files, func(p string) bool {
					return hasTestMap[p]
				})
			}

			return formatAIVerifyResult(res, in.SummaryOnly), nil
		},
	}
}

func formatAIVerifyResult(res aiverify.Result, summaryOnly bool) any {
	out := map[string]any{
		"files_scanned": res.FilesScanned,
		"total_lines":   res.TotalLines,
		"ai_gen_lines":  res.AIGenLines,
		"finding_count": len(res.Findings),
		"risk_score":    res.RiskScore,
		"has_critical":  res.HasCritical,
		"by_severity":   res.BySeverity,
		"by_category":   res.ByCategory,
		"summary":       res.Summary(),
	}
	if len(res.AIGenWithoutTests) > 0 {
		out["ai_gen_without_tests"] = res.AIGenWithoutTests
	}
	if !summaryOnly {
		out["findings"] = res.Findings
	}
	if res.CacheHits > 0 || res.CacheMisses > 0 {
		out["cache_hits"] = res.CacheHits
		out["cache_misses"] = res.CacheMisses
	}
	return out
}

// ─── yagura_test_audit + ai_verify との結合 (v0.26.0) ───────
//
// m's harness G0.7 「テスト通過 + 人間確認が必須」への直接対応。
// language-aware な test file 検出 + AI 生成 untested risk 強化。

func buildTestAuditTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_test_audit",
		Description: "[G] Source-test coverage detection. Go/TS/JS/Python/Rust/Java filename + Rust inline #[cfg(test)] + Python doctest.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files":         map[string]any{"type": "object"},
				"untested_only": map[string]any{"type": "boolean"},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files        map[string]string `json:"files"`
				UntestedOnly bool              `json:"untested_only"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			res := testcoverage.Audit(in.Files)
			out := map[string]any{
				"files_scanned":     res.FilesScanned,
				"source_files":      res.SourceFiles,
				"test_files":        res.TestFiles,
				"sources_with_test": res.SourcesWithTest,
				"sources_no_test":   res.SourcesNoTest,
				"coverage_ratio":    res.CoverageRatio,
				"untested_files":    res.UntestedFiles,
				"by_language":       res.ByLanguage,
			}
			return out, nil
		},
	}
}

// ─── yagura_ast_check (v0.36.0, Roadmap #6) ───────────────────
//
// go/ast による構造解析。行 regex では原理的に書けない検査
// (os.Exit in library / 空 != nil 分岐 / defer-in-loop / err 文字列比較 /
// parse-error)を決定論的に返す。CLI `ast-check` と同じ astcheck.ScanFiles。
// ─── yagura_assert_check (v0.36.0) ────────────────────────────
//
// ソクラテス的動機: testcoverage は test の "存在" を検査するが、assertion density
// (test 内で何を主張しているか)は別問題。hollow test(assertion 無し)は常に緑
// になるが証明にはならない。本 tool はアサーション密度を数値化する。

func buildAssertCheckTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_assert_check",
		Description: "[Q] Test assertion density analysis. Detects hollow test files (zero assertions), reports avg density per test function.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "object",
					"description": "map of filename → content for *_test.go files to analyse",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files map[string]string `json:"files"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := assertcheck.Scan(in.Files)
			return map[string]any{
				"files_scanned":    rep.FilesScanned,
				"test_files":       rep.TestFiles,
				"total_test_funcs": rep.TotalTestFuncs,
				"total_assertions": rep.TotalAssertions,
				"hollow_files":     rep.HollowFiles,
				"avg_density":      rep.AvgDensity,
				"files":            rep.Files,
			}, nil
		},
	}
}

// ─── yagura_err_policy (v0.36.0) ──────────────────────────────
//
// ソクラテス的動機: 「失敗が起きたとき、どこで・なぜ かが分かるか?」という診断
// 可能性の軸。naked `return err`(context 喪失)vs wrapped `fmt.Errorf(...%w...)`
// の wrap 率を計測し、`_ = call()` の blank-discard を surface する。

func buildErrPolicyTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_err_policy",
		Description: "[Q] Error-context discipline (Go). Wrap ratio (fmt.Errorf %w vs naked return err) + blank-discard (`_ = call()`) detection.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "object",
					"description": "map of filename → content for .go files to analyse",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files map[string]string `json:"files"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := errpolicy.Scan(in.Files)
			return map[string]any{
				"files_scanned":   rep.FilesScanned,
				"wrapped_returns": rep.WrappedReturns,
				"naked_returns":   rep.NakedReturns,
				"blank_discards":  rep.BlankDiscards,
				"wrap_ratio":      rep.WrapRatio,
				"findings":        rep.Findings,
			}, nil
		},
	}
}

func buildASTCheckTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_ast_check",
		Description: "[G] Go AST structural audit. os.Exit in library, empty != nil branch, defer in loop, err-string compare, parse errors.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{"type": "object"},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files map[string]string `json:"files"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			res := astcheck.ScanFiles(in.Files)
			return map[string]any{
				"files_scanned": res.FilesScanned,
				"findings":      res.Findings,
				"by_severity":   res.BySeverity,
				"by_rule":       res.ByRule,
			}, nil
		},
	}
}

