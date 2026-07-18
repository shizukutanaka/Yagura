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
	"github.com/shizukutanaka/yagura/internal/apidoc"
	"github.com/shizukutanaka/yagura/internal/assertcheck"
	"github.com/shizukutanaka/yagura/internal/astcheck"
	"github.com/shizukutanaka/yagura/internal/calibrate"
	"github.com/shizukutanaka/yagura/internal/codehealth"
	"github.com/shizukutanaka/yagura/internal/cognit"
	"github.com/shizukutanaka/yagura/internal/complexity"
	"github.com/shizukutanaka/yagura/internal/coupling"
	"github.com/shizukutanaka/yagura/internal/ctxcheck"
	"github.com/shizukutanaka/yagura/internal/deadcode"
	"github.com/shizukutanaka/yagura/internal/deprank"
	"github.com/shizukutanaka/yagura/internal/errdiscard"
	"github.com/shizukutanaka/yagura/internal/errpolicy"
	"github.com/shizukutanaka/yagura/internal/errwrap"
	"github.com/shizukutanaka/yagura/internal/flagarg"
	"github.com/shizukutanaka/yagura/internal/globalcheck"
	"github.com/shizukutanaka/yagura/internal/hotspot"
	"github.com/shizukutanaka/yagura/internal/ifacebloat"
	"github.com/shizukutanaka/yagura/internal/lensoverlap"
	"github.com/shizukutanaka/yagura/internal/nakedret"
	"github.com/shizukutanaka/yagura/internal/namecheck"
	"github.com/shizukutanaka/yagura/internal/nestdepth"
	"github.com/shizukutanaka/yagura/internal/paramcheck"
	"github.com/shizukutanaka/yagura/internal/prealloc"
	"github.com/shizukutanaka/yagura/internal/predeclared"
	"github.com/shizukutanaka/yagura/internal/qualitycheck"
	"github.com/shizukutanaka/yagura/internal/recvcheck"
	"github.com/shizukutanaka/yagura/internal/regress"
	"github.com/shizukutanaka/yagura/internal/returncheck"
	"github.com/shizukutanaka/yagura/internal/synccheck"
	"github.com/shizukutanaka/yagura/internal/testcoverage"
	"github.com/shizukutanaka/yagura/internal/thelper"
	"github.com/shizukutanaka/yagura/internal/typeassert"
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
		Title:       "Check Code Quality",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
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
				if in.SummaryOnly {
					return formatQualityResultSummary(res), nil
				}
				return formatQualityResult(res), nil
			}
			// v0.23.0: cache 統合(content-hash で同 content の再 scan を skip)
			res := qualitycheck.ScanFilesCached(in.Files, rules, cache)
			if in.SummaryOnly {
				return formatQualityResultSummary(res), nil
			}
			return formatQualityResult(res), nil
		},
	}
}

// formatQualityResult は MCP-friendly 出力フォーマッタ(全 findings 含む)。
// flag-arg 修正(v0.66.0): summaryOnly bool を除去し関数を目的別に分割。
func formatQualityResult(res qualitycheck.Result) any {
	out := qualityResultBase(res)
	out["findings"] = res.Findings
	if len(res.ByFile) > 0 {
		out["by_file"] = res.ByFile
	}
	return out
}

// formatQualityResultSummary は findings を省いた集計のみを返す。
func formatQualityResultSummary(res qualitycheck.Result) any {
	return qualityResultBase(res)
}

func qualityResultBase(res qualitycheck.Result) map[string]any {
	return map[string]any{
		"files_scanned":  res.FilesScanned,
		"total_lines":    res.TotalLines,
		"finding_count":  len(res.Findings),
		"by_severity":    res.BySeverity,
		"by_rule":        res.ByRule,
		"has_prohibited": res.HasProhibited(),
		"summary":        res.Summary(),
	}
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
		Title:       "AI Code Risk Audit",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] AI code risk audit. 6 categories: auth/billing/data/external/crypto/secret. 2x multiplier inside AI-marker zones.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files":         map[string]any{"type": "object"},
				"text":          map[string]any{"type": "string"},
				"path":          map[string]any{"type": "string"},
				"summary_only":  map[string]any{"type": "boolean"},
				"custom_rules":  map[string]any{"type": "array", "description": "project-specific AI risk rules: [{id, pattern(regex), category, risk(CRITICAL|HIGH|MEDIUM|LOW), message, languages?}]"},
				"disable_rules": map[string]any{"type": "array", "description": "built-in rule IDs to suppress (e.g. [\"billing-stripe-uncaught\"])"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files        map[string]string   `json:"files"`
				Text         string              `json:"text"`
				Path         string              `json:"path"`
				SummaryOnly  bool                `json:"summary_only"`
				CustomRules  []aiverify.UserRule `json:"custom_rules"`
				DisableRules []string            `json:"disable_rules"`
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
			rules, terr := aiVerifyRules(in.CustomRules, in.DisableRules)
			if terr != nil {
				return nil, terr
			}

			// Cache is only safe with default rules in file mode (cache key is
			// content-based and does not encode custom rules); else pass nil.
			scanFiles := aiVerifyInputFiles(in.Files, in.Text, in.Path)
			var scanCache aiverify.CacheLike
			if in.Text == "" && len(in.CustomRules) == 0 && len(in.DisableRules) == 0 {
				scanCache = cache
			}
			res := runAIVerifyScan(scanFiles, rules, scanCache)

			// v0.26.0: testcoverage と結合し untested AI 生成を flag (+5/file)
			// files が複数与えられた場合のみ意味があるので text mode は skip
			if len(in.Files) > 0 {
				res = annotateUntestedAI(res, in.Files)
			}

			if in.SummaryOnly {
				return formatAIVerifyResultSummary(res), nil
			}
			return formatAIVerifyResult(res), nil
		},
	}
}

// runAIVerifyScan は入力モード(text 単体 / cache 経由 / 明示 rules)を選んで
// aiverify を実行する。cache 非 nil なら content-addressed cache 経由
// (呼び出し側が「default rule かつ file mode」のときだけ cache を渡す)。
func runAIVerifyScan(files map[string]string, rules []aiverify.Rule, cache aiverify.CacheLike) aiverify.Result {
	if cache != nil {
		return aiverify.ScanCached(files, cache)
	}
	return aiverify.ScanWithRules(files, rules)
}

// aiVerifyInputFiles は text/path 単体入力を files map に正規化する(text 空なら
// 元の files をそのまま返す)。
func aiVerifyInputFiles(files map[string]string, text, path string) map[string]string {
	if text == "" {
		return files
	}
	if path == "" {
		path = "<input>"
	}
	return map[string]string{path: text}
}

// aiVerifyRules は default rules に user config(custom/disable)を適用して
// 有効ルール集合を返す。
func aiVerifyRules(custom []aiverify.UserRule, disable []string) ([]aiverify.Rule, *ToolError) {
	rules := aiverify.DefaultRules()
	if len(custom) == 0 && len(disable) == 0 {
		return rules, nil
	}
	userCfg := &aiverify.UserConfig{Rules: custom, Disable: disable}
	applied, err := userCfg.Apply(aiverify.DefaultRules())
	if err != nil {
		return nil, &ToolError{Code: "invalid_input", Message: err.Error()}
	}
	return applied, nil
}

// annotateUntestedAI は testcoverage と結合し、テスト不在の AI 生成 source を
// flag する(+5/file)。has_test 認定 = untested ではない non-test source。
func annotateUntestedAI(res aiverify.Result, files map[string]string) aiverify.Result {
	tcRes := testcoverage.Audit(files)
	untestedSet := make(map[string]bool, len(tcRes.UntestedFiles))
	for _, p := range tcRes.UntestedFiles {
		untestedSet[p] = true
	}
	hasTestMap := make(map[string]bool, len(files))
	for p := range files {
		if testcoverage.IsTestFile(p) {
			continue
		}
		if !untestedSet[p] {
			hasTestMap[p] = true
		}
	}
	return aiverify.AnnotateUntested(res, files, func(p string) bool {
		return hasTestMap[p]
	})
}

// formatAIVerifyResult は全 findings を含む MCP 出力を返す。
// flag-arg 修正(v0.66.0): summaryOnly bool を除去し関数を目的別に分割。
func formatAIVerifyResult(res aiverify.Result) any {
	out := aiVerifyResultBase(res)
	out["findings"] = res.Findings
	return out
}

// formatAIVerifyResultSummary は findings を省いた集計のみを返す。
func formatAIVerifyResultSummary(res aiverify.Result) any {
	return aiVerifyResultBase(res)
}

func aiVerifyResultBase(res aiverify.Result) map[string]any {
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
		Title:       "Audit Test Coverage",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
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
		Title:       "Test Assertion Density",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Test assertion density analysis. Detects hollow test files (zero assertions), reports avg density per test function.",
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
		Title:       "Error Handling Policy Check",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Error-context discipline (Go). Wrap ratio (fmt.Errorf %w vs naked return err) + blank-discard (`_ = call()`) detection.",
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

// ─── yagura_complexity (v0.36.0) ──────────────────────────────
//
// ソクラテス的動機: 「このコードはそもそも完全にテストできるか?」という testability
// の前提条件。循環的複雑度 = 全パス網羅に要するテストケースの下限。assertcheck/
// coverage が検証の結果なら、本 tool は検証可能性そのものを測る。

func buildComplexityTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_complexity",
		Title:       "Cyclomatic Complexity Check",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Cyclomatic complexity (Go, gocyclo-compatible). Per-function McCabe score; flags functions over threshold (default 10).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "object",
					"description": "map of filename → content for .go files to analyse",
				},
				"threshold": map[string]any{
					"type":        "integer",
					"description": "complexity threshold for findings (default 10)",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files     map[string]string `json:"files"`
				Threshold int               `json:"threshold"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := complexity.Scan(in.Files, in.Threshold)
			return map[string]any{
				"files_scanned":  rep.FilesScanned,
				"threshold":      rep.Threshold,
				"max_complexity": rep.MaxComplexity,
				"avg_complexity": rep.AvgComplexity,
				"over_threshold": rep.OverThreshold,
				"functions":      rep.Functions,
				"findings":       rep.Findings,
			}, nil
		},
	}
}

// ─── yagura_param_check (v0.65.0) ─────────────────────────────
//
// ソクラテス的動機: complexity が関数内部の縦の複雑さ(分岐パス数)なら、param_check は
// 入口の横幅(引数の数)。Fowler の "Long Parameter List" smell を検出する。複雑度
// だけを gate にすると、巨大関数をヘルパに割って複雑度を下げつつ引数を引き回す退行を
// 見逃す——本 tool はその盲点(complexity の水平方向の対)を塞ぐ。

func buildParamCheckTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_param_check",
		Title:       "Check Parameter Count",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Long-parameter-list smell (Go, Fowler). Per-function param count; flags functions over threshold (default 5).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "object",
					"description": "map of filename → content for .go files to analyse",
				},
				"threshold": map[string]any{
					"type":        "integer",
					"description": "parameter-count threshold for findings (default 5)",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files     map[string]string `json:"files"`
				Threshold int               `json:"threshold"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := paramcheck.Scan(in.Files, in.Threshold)
			return map[string]any{
				"files_scanned":  rep.FilesScanned,
				"threshold":      rep.Threshold,
				"max_params":     rep.MaxParams,
				"avg_params":     rep.AvgParams,
				"over_threshold": rep.OverThreshold,
				"functions":      rep.Functions,
				"findings":       rep.Findings,
			}, nil
		},
	}
}

// ─── yagura_flag_arg (v0.66.0) ────────────────────────────────
//
// ソクラテス的動機: complexity は分岐パス数(垂直)、paramcheck は引数の総数(水平)を測る。
// しかし bool 型の引数はカウント以上の臭いを持つ: `process(data, true)` の呼び出し元で
// "true" が何を意味するか即座にわからない(Martin Fowler "Flag Argument" smell)。
// 本 tool は complexity + paramcheck が見逃す「引数の意味的制御結合」を補完する。

func buildFlagArgTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_flag_arg",
		Title:       "Flag Argument Smell Check",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Boolean flag-argument smell (Go, Fowler). Detects functions with bool parameters that encode hidden control-flow branches. A bool arg that selects behavior ('if verbose', 'if dryRun') is opaque at call sites; consider splitting into two clearly named functions.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "object",
					"description": "map of filename → content for .go files to analyse",
				},
				"threshold": map[string]any{
					"type":        "integer",
					"description": "minimum number of bool params to flag (default 1; set 2 to skip single-bool cases)",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files     map[string]string `json:"files"`
				Threshold int               `json:"threshold"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := flagarg.Scan(in.Files, in.Threshold)
			return map[string]any{
				"files_scanned": rep.FilesScanned,
				"funcs_scanned": rep.FuncsScanned,
				"threshold":     rep.Threshold,
				"flags_found":   rep.FlagsFound,
				"findings":      rep.Findings,
			}, nil
		},
	}
}

// ─── yagura_return_check (v0.67.0) ───────────────────────────
//
// ソクラテス的動機: paramcheck は関数の入口の広さ(引数の数)を測り、flagarg は
// 引数の意味的制御結合(bool 旗引数)を測る。しかし「出口の幅」——戻り値の数——は
// 別の軸であり、どのレンズも測っていなかった。Go の `(T, error)` は慣用的だが、
// `(T1, T2, T3, error)` は「関数がやりすぎ」の臭いになりうる。
// 本 tool は paramcheck/flagarg の「出口の対」として関数のシグネチャ全体像を補完する。

func buildReturnCheckTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_return_check",
		Title:       "Check Return Count",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Many-return-values smell (Go). Counts return values per function; flags functions over threshold (default 3). Complements param_check (input width) with output width — together they form a complete function-signature profile.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "object",
					"description": "map of filename → content for .go files to analyse",
				},
				"threshold": map[string]any{
					"type":        "integer",
					"description": "return-value count threshold for findings (default 3; flags 4+ returns)",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files     map[string]string `json:"files"`
				Threshold int               `json:"threshold"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := returncheck.Scan(in.Files, in.Threshold)
			return map[string]any{
				"files_scanned":    rep.FilesScanned,
				"funcs_scanned":    rep.FuncsScanned,
				"threshold":        rep.Threshold,
				"too_many_returns": rep.TooManyReturns,
				"max_returns":      rep.MaxReturns,
				"avg_returns":      rep.AvgReturns,
				"findings":         rep.Findings,
			}, nil
		},
	}
}

// ─── yagura_coupling (v0.36.0) ────────────────────────────────
//
// ソクラテス的動機: complexity が関数内部の絡まりを測るのに対し、本 tool は package
// *同士* の結合(アーキテクチャ)を測る。fan-in/fan-out/instability + Stable
// Dependencies Principle 違反(安定 package が より不安定な package に依存)を検出。

func buildCouplingTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_coupling",
		Title:       "Package Coupling Analysis",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Package import coupling (Go). Fan-in/out + instability (Ce/(Ca+Ce)) + Stable Dependencies Principle violations.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "object",
					"description": "map of filename → content for .go files (paths relative to module root)",
				},
				"module_path": map[string]any{
					"type":        "string",
					"description": "go.mod module path for internal-import detection (defaults to the server's main module)",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files      map[string]string `json:"files"`
				ModulePath string            `json:"module_path"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			mp := in.ModulePath
			if mp == "" {
				mp = d.MainModulePath
			}
			if mp == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "module_path required (no server default configured)"}
			}
			rep := coupling.Scan(in.Files, mp)
			return map[string]any{
				"module_path":   rep.ModulePath,
				"package_count": rep.PackageCount,
				"packages":      rep.Packages,
				"findings":      rep.Findings,
			}, nil
		},
	}
}

// ─── yagura_api_doc (v0.36.0) ─────────────────────────────────
//
// ソクラテス的動機: coupling が package 間の依存を測るのに対し、本 tool は package が
// 依存側に *約束* する exported API とその文書化を測る。doc コメントの無い exported
// シンボル = 仕様の無い契約。godoc 規律(golint 互換)を機械化。

func buildAPIDocTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_api_doc",
		Title:       "API Documentation Check",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Exported-API doc discipline (Go). Documented ratio + undocumented exported funcs/types/consts/vars/methods.",
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
			rep := apidoc.Scan(in.Files)
			return map[string]any{
				"files_scanned":    rep.FilesScanned,
				"exported_total":   rep.ExportedTotal,
				"documented":       rep.Documented,
				"documented_ratio": rep.DocumentedRatio,
				"by_kind":          rep.ByKind,
				"findings":         rep.Findings,
			}, nil
		},
	}
}

// ─── yagura_dead_code (v0.36.0) ───────────────────────────────
//
// ソクラテス的動機: apidoc が未文書化の公開契約を見るのに対し、本 tool は非公開側の
// 双対――自 package 内で誰からも参照されない unexported 宣言(dead code)を検出する。
// unexported は閉じた世界なので保守的かつ安全に到達不能を断定できる。

func buildDeadCodeTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_dead_code",
		Title:       "Dead Code Detection",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Dead unexported declarations (Go). Package-level funcs/types/consts/vars never referenced within their package.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "object",
					"description": "map of filename → content for .go files (paths relative to package root)",
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
			rep := deadcode.Scan(in.Files)
			return map[string]any{
				"files_scanned":       rep.FilesScanned,
				"packages_scanned":    rep.PackagesScanned,
				"declared_unexported": rep.DeclaredUnexported,
				"dead":                rep.Dead,
				"findings":            rep.Findings,
			}, nil
		},
	}
}

// ─── yagura_recv_check (v0.36.0) ──────────────────────────────
//
// ソクラテス的動機: 他レンズは unit を絶対基準で測るが、本 tool は unit を自分自身の
// 他の部分と照らす自己一貫性の軸。型のメソッドレシーバ名の不揃い / 値・ポインタ混在 /
// 非慣習的レシーバ名(this/self)を検出。

func buildRecvCheckTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_recv_check",
		Title:       "Check Receiver Consistency",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Method receiver consistency (Go). Inconsistent receiver names, mixed value/pointer receivers, un-idiomatic names (this/self).",
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
			rep := recvcheck.Scan(in.Files)
			return map[string]any{
				"files_scanned":      rep.FilesScanned,
				"types_with_methods": rep.TypesWithMethods,
				"findings":           rep.Findings,
			}, nil
		},
	}
}

// ─── yagura_code_health (v0.36.0) ─────────────────────────────
//
// ソクラテス的動機: 保守者は「この package は総合的に健全か」を問う。本 tool は
// complexity/apidoc/deadcode/recvcheck/assertcheck/astcheck を package 別 grade(A-F)へ
// 合成する。reviewgate(security 合成)の maintainability 版。

func buildCodeHealthTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_code_health",
		Title:       "Composite Code Health Grade",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Composite maintainability grade (Go). Per-package A-F from complexity/apidoc/deadcode/recvcheck/assertcheck/astcheck.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":        "object",
					"description": "map of filename → content for .go files (paths relative to module root)",
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
			rep := codehealth.Analyze(in.Files)
			return map[string]any{
				"overall_score": rep.OverallScore,
				"overall_grade": rep.OverallGrade,
				"packages":      rep.Packages,
			}, nil
		},
	}
}

func buildASTCheckTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_ast_check",
		Title:       "AST Structural Audit",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
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

// ─── yagura_err_discard (v0.68.0) ────────────────────────────
//
// ソクラテス的動機: paramcheck(入口幅)+ flagarg(意味的制御結合)+ returncheck(出口幅)は
// 関数の *定義側* をプロファイルする三軸の完全なシグネチャ像を与えた。しかし、これらは
// *呼び出し側* の規律を見ていない。error を返す関数が ExprStmt として呼ばれると、
// その error は暗黙的に捨てられる——コンパイラも go vet も(多くの場合)素通りする。
// errdiscard は「コールサイト規律」(ブラインドスポット IV)を可視化し、シグネチャ三軸に
// 続く第四のコードクオリティ軸を提供する。

func buildErrDiscardTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_err_discard",
		Title:       "Error Discard Detection",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Error-discard smell: call sites where a returned error is silently ignored",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"strict": map[string]any{"type": "boolean"},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files  map[string]string `json:"files"`
				Strict bool              `json:"strict"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := errdiscard.Scan(in.Files)
			return map[string]any{
				"files_scanned":    rep.FilesScanned,
				"calls_scanned":    rep.CallsScanned,
				"errors_discarded": rep.ErrorsDiscarded,
				"findings":         rep.Findings,
			}, nil
		},
	}
}

// ─── yagura_dep_rank (v0.69.0) ────────────────────────────────
//
// ソクラテス的動機: errdiscard まで全レンズが関数レベル(complexity/paramcheck/
// flagarg/returncheck)かコールサイトレベル(errdiscard)で動作しており、
// *パッケージレベルの構造結合*——どのパッケージが最も多くの内部パッケージから
// 依存されているか(in-degree = blast radius)——を捉えるレンズが存在しなかった。
// deprank は「パッケージグラフ構造」(ブラインドスポット V)を可視化する。

func buildDepRankTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_dep_rank",
		Title:       "Package Dependency Rank",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Package dependency rank: internal packages by import in-degree (blast radius when changed)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"module_prefix": map[string]any{
					"type":        "string",
					"description": "Go module path prefix (e.g. github.com/shizukutanaka/yagura)",
				},
				"threshold": map[string]any{
					"type":        "integer",
					"description": "Minimum in-degree to flag (default 5)",
				},
			},
			"required": []string{"files", "module_prefix"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files        map[string]string `json:"files"`
				ModulePrefix string            `json:"module_prefix"`
				Threshold    int               `json:"threshold"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			if in.ModulePrefix == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "module_prefix required"}
			}
			rep := deprank.Scan(in.Files, in.ModulePrefix, in.Threshold)
			return rep, nil
		},
	}
}

func buildHotspotTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_hotspot",
		Title:       "Detect Quality Hotspots",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Convergent-signal hotspots: functions flagged by 2+ of 12 independent lenses (complexity, params, returns, cognit, nestdepth, and more)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"min_lenses": map[string]any{
					"type":        "integer",
					"description": "Minimum number of lenses that must converge to report a hotspot (default 2)",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files     map[string]string `json:"files"`
				MinLenses int               `json:"min_lenses"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := hotspot.Scan(in.Files, in.MinLenses)
			return rep, nil
		},
	}
}

func buildLensOverlapTool(_ Deps) *Tool {
	return &Tool{
		Name:        "yagura_lens_overlap",
		Title:       "Lens Overlap Analysis",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Meta: Jaccard overlap between hotspot's 12 lenses — high overlap flags consolidation candidates, near-zero confirms orthogonal axes",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
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
			rep := lensoverlap.Scan(in.Files)
			return rep, nil
		},
	}
}

func buildNameCheckTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_name_check",
		Title:       "Check Name Consistency",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Name↔signature consistency: predicates (is/has) must return bool, getters/constructors must return a value",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
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
			rep := namecheck.Scan(in.Files)
			return rep, nil
		},
	}
}

func buildCtxCheckTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_ctx_check",
		Title:       "Context Argument Discipline",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] context.Context discipline: must be first param (not in struct fields). Go convention (containedctx-style)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
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
			rep := ctxcheck.Scan(in.Files)
			return rep, nil
		},
	}
}

func buildErrWrapTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_err_wrap",
		Title:       "Error Wrapping Discipline",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Error-wrapping discipline (Go 1.13): %w not %v, errors.Is over ==, errors.As over type assert (errorlint-style)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
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
			rep := errwrap.Scan(in.Files)
			return rep, nil
		},
	}
}

func buildSyncCheckTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_sync_check",
		Title:       "Check Sync Lock Copies",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] sync-lock copy discipline: methods/params/returns must not copy types containing sync.Mutex/RWMutex/etc (copylocks-style)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
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
			rep := synccheck.Scan(in.Files)
			return rep, nil
		},
	}
}

func buildNakedRetTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_naked_ret",
		Title:       "Check Naked Returns",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Naked-return readability: naked `return` in long named-result functions (nakedret-style, default >30 lines)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"max_lines": map[string]any{
					"type":        "integer",
					"description": "function line-count threshold above which naked returns are flagged (default 30)",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files    map[string]string `json:"files"`
				MaxLines int               `json:"max_lines"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := nakedret.Scan(in.Files, in.MaxLines)
			return rep, nil
		},
	}
}

func buildPredeclaredTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_predeclared",
		Title:       "Check Predeclared Shadowing",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Predeclared-identifier shadowing: vars/params/types/funcs that shadow Go builtins (predeclared-style)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"ignore": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "predeclared identifiers to allow shadowing (e.g. [\"cap\",\"min\",\"max\"])",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files  map[string]string `json:"files"`
				Ignore []string          `json:"ignore"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := predeclared.Scan(in.Files, in.Ignore)
			return rep, nil
		},
	}
}

func buildCalibrateTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_calibrate",
		Title:       "Threshold Calibration",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Threshold calibration: percentile distributions of complexity/params/returns/func-lines to set data-driven --max gates",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
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
			rep := calibrate.Scan(in.Files)
			return rep, nil
		},
	}
}

func buildRegressTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_regress",
		Title:       "Detect Quality Regressions",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Quality ratchet: compare old vs new code and report functions whose complexity/params/returns/lines regressed",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"old": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
					"description":          "baseline file set (path→content)",
				},
				"new": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
					"description":          "current file set (path→content)",
				},
				"thresholds": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "integer"},
					"description":          "optional per-metric Crossed-gate overrides (complexity/params/returns/func_lines), e.g. from calibrate's suggested_threshold; omit to use conventional defaults",
				},
			},
			"required": []string{"old", "new"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Old        map[string]string `json:"old"`
				New        map[string]string `json:"new"`
				Thresholds map[string]int    `json:"thresholds"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Old) == 0 || len(in.New) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "both 'old' and 'new' file sets are required"}
			}
			rep := regress.CompareWithThresholds(in.Old, in.New, in.Thresholds)
			return rep, nil
		},
	}
}

func buildNestDepthTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_nest_depth",
		Title:       "Check Nesting Depth",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Max control-flow nesting depth per function (the pyramid-of-doom signal complexity misses; guard-clause discipline)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"max_depth": map[string]any{
					"type":        "integer",
					"description": "nesting-depth threshold above which a function is flagged (default 4)",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files    map[string]string `json:"files"`
				MaxDepth int               `json:"max_depth"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := nestdepth.Scan(in.Files, in.MaxDepth)
			return rep, nil
		},
	}
}

func buildGlobalCheckTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_global_check",
		Title:       "Mutable Global State Check",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Mutable global state: package-level vars actually mutated somewhere (testability + data-race hazard; gochecknoglobals-style)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
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
			rep := globalcheck.Scan(in.Files)
			return rep, nil
		},
	}
}

func buildTypeAssertTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_type_assert",
		Title:       "Check Type Assertions",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Panic-safety: single-value type assertions x.(T) that panic on mismatch (use comma-ok; forcetypeassert-style)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
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
			rep := typeassert.Scan(in.Files)
			return rep, nil
		},
	}
}

func buildCognitTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_cognit",
		Title:       "Cognitive Complexity Check",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Cognitive complexity per function (human reading cost; nesting-weighted, switch=1; gocognit-style — complements McCabe)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"max": map[string]any{
					"type":        "integer",
					"description": "cognitive-complexity threshold above which a function is flagged (default 15)",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files map[string]string `json:"files"`
				Max   int               `json:"max"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := cognit.Scan(in.Files, in.Max)
			return rep, nil
		},
	}
}

func buildPreallocTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_prealloc",
		Title:       "Check Slice Preallocation",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Performance: slices grown by append in a range loop without preallocation (make([]T,0,len); prealloc-style)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
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
			rep := prealloc.Scan(in.Files)
			return rep, nil
		},
	}
}

func buildIfaceBloatTool(_ Deps) *Tool {
	return &Tool{
		Name:        "yagura_ifacebloat",
		Title:       "Check Interface Bloat",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Interface design: named interfaces with too many methods (Rob Pike \"bigger interface = weaker abstraction\"; interfacebloat-style)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"threshold": map[string]any{
					"type":        "integer",
					"description": "method-count threshold above which an interface is flagged (default 10)",
				},
			},
			"required": []string{"files"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Files     map[string]string `json:"files"`
				Threshold int               `json:"threshold"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Files) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "files required"}
			}
			rep := ifacebloat.Scan(in.Files, in.Threshold)
			return rep, nil
		},
	}
}

func buildThelperTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_thelper",
		Title:       "Check Test Helpers",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Test quality: test helpers (take *testing.T/B/TB) that never call t.Helper() (failures point at the helper; thelper-style)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"files": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
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
			rep := thelper.Scan(in.Files)
			return rep, nil
		},
	}
}
