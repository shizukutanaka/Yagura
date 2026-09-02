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
	"strings"

	"github.com/shizukutanaka/yagura/internal/aiverify"
	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/churn"
	"github.com/shizukutanaka/yagura/internal/cochange"
	"github.com/shizukutanaka/yagura/internal/codehealth"
	"github.com/shizukutanaka/yagura/internal/complexity"
	"github.com/shizukutanaka/yagura/internal/defectdataset"
	"github.com/shizukutanaka/yagura/internal/fixhistory"
	"github.com/shizukutanaka/yagura/internal/ownership"
	"github.com/shizukutanaka/yagura/internal/portfolioquality"
	"github.com/shizukutanaka/yagura/internal/processrisk"
	"github.com/shizukutanaka/yagura/internal/qualitycheck"
	"github.com/shizukutanaka/yagura/internal/regress"
	"github.com/shizukutanaka/yagura/internal/srcfiles"
	"github.com/shizukutanaka/yagura/internal/testcoverage"
	"github.com/shizukutanaka/yagura/internal/walkforward"
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

// ─── yagura_err_policy (v0.36.0) ──────────────────────────────
//
// ソクラテス的動機: 「失敗が起きたとき、どこで・なぜ かが分かるか?」という診断
// 可能性の軸。naked `return err`(context 喪失)vs wrapped `fmt.Errorf(...%w...)`
// の wrap 率を計測し、`_ = call()` の blank-discard を surface する。

// ─── yagura_complexity (v0.36.0) ──────────────────────────────
//
// ソクラテス的動機: 「このコードはそもそも完全にテストできるか?」という testability
// の前提条件。循環的複雑度 = 全パス網羅に要するテストケースの下限。assertcheck/
// coverage が検証の結果なら、本 tool は検証可能性そのものを測る。

// ─── yagura_param_check (v0.65.0) ─────────────────────────────
//
// ソクラテス的動機: complexity が関数内部の縦の複雑さ(分岐パス数)なら、param_check は
// 入口の横幅(引数の数)。Fowler の "Long Parameter List" smell を検出する。複雑度
// だけを gate にすると、巨大関数をヘルパに割って複雑度を下げつつ引数を引き回す退行を
// 見逃す——本 tool はその盲点(complexity の水平方向の対)を塞ぐ。

// ─── yagura_flag_arg (v0.66.0) ────────────────────────────────
//
// ソクラテス的動機: complexity は分岐パス数(垂直)、paramcheck は引数の総数(水平)を測る。
// しかし bool 型の引数はカウント以上の臭いを持つ: `process(data, true)` の呼び出し元で
// "true" が何を意味するか即座にわからない(Martin Fowler "Flag Argument" smell)。
// 本 tool は complexity + paramcheck が見逃す「引数の意味的制御結合」を補完する。

// ─── yagura_return_check (v0.67.0) ───────────────────────────
//
// ソクラテス的動機: paramcheck は関数の入口の広さ(引数の数)を測り、flagarg は
// 引数の意味的制御結合(bool 旗引数)を測る。しかし「出口の幅」——戻り値の数——は
// 別の軸であり、どのレンズも測っていなかった。Go の `(T, error)` は慣用的だが、
// `(T1, T2, T3, error)` は「関数がやりすぎ」の臭いになりうる。
// 本 tool は paramcheck/flagarg の「出口の対」として関数のシグネチャ全体像を補完する。

// ─── yagura_coupling (v0.36.0) ────────────────────────────────
//
// ソクラテス的動機: complexity が関数内部の絡まりを測るのに対し、本 tool は package
// *同士* の結合(アーキテクチャ)を測る。fan-in/fan-out/instability + Stable
// Dependencies Principle 違反(安定 package が より不安定な package に依存)を検出。

// ─── yagura_api_doc (v0.36.0) ─────────────────────────────────
//
// ソクラテス的動機: coupling が package 間の依存を測るのに対し、本 tool は package が
// 依存側に *約束* する exported API とその文書化を測る。doc コメントの無い exported
// シンボル = 仕様の無い契約。godoc 規律(golint 互換)を機械化。

// ─── yagura_dead_code (v0.36.0) ───────────────────────────────
//
// ソクラテス的動機: apidoc が未文書化の公開契約を見るのに対し、本 tool は非公開側の
// 双対――自 package 内で誰からも参照されない unexported 宣言(dead code)を検出する。
// unexported は閉じた世界なので保守的かつ安全に到達不能を断定できる。

// ─── yagura_recv_check (v0.36.0) ──────────────────────────────
//
// ソクラテス的動機: 他レンズは unit を絶対基準で測るが、本 tool は unit を自分自身の
// 他の部分と照らす自己一貫性の軸。型のメソッドレシーバ名の不揃い / 値・ポインタ混在 /
// 非慣習的レシーバ名(this/self)を検出。

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

// ─── yagura_err_discard (v0.68.0) ────────────────────────────
//
// ソクラテス的動機: paramcheck(入口幅)+ flagarg(意味的制御結合)+ returncheck(出口幅)は
// 関数の *定義側* をプロファイルする三軸の完全なシグネチャ像を与えた。しかし、これらは
// *呼び出し側* の規律を見ていない。error を返す関数が ExprStmt として呼ばれると、
// その error は暗黙的に捨てられる——コンパイラも go vet も(多くの場合)素通りする。
// errdiscard は「コールサイト規律」(ブラインドスポット IV)を可視化し、シグネチャ三軸に
// 続く第四のコードクオリティ軸を提供する。

// ─── yagura_dep_rank (v0.69.0) ────────────────────────────────
//
// ソクラテス的動機: errdiscard まで全レンズが関数レベル(complexity/paramcheck/
// flagarg/returncheck)かコールサイトレベル(errdiscard)で動作しており、
// *パッケージレベルの構造結合*——どのパッケージが最も多くの内部パッケージから
// 依存されているか(in-degree = blast radius)——を捉えるレンズが存在しなかった。
// deprank は「パッケージグラフ構造」(ブラインドスポット V)を可視化する。

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

// buildPortfolioQualityTool は登録済み全プロジェクトのコード健全性を横断ランキングする
// (v0.118.0)。First Principles 由来の C5 ギャップ解消: 注意配分レイヤ
// (alert_fix/today/release_radar)は外部センサーしか見ておらず、~24 個の quality lens は
// ポートフォリオから不可視だった。
//
// **files を受け取らない**のが要点: registry の local_path から daemon 自身がディスクを
// 読むので、ソース内容は 1 バイトも LLM context を通らない(既存 lens tool 群の
// content-based 契約が抱えていた token 経済の矛盾を解消する)。
func buildPortfolioQualityTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_portfolio_quality",
		Title:       "Portfolio Code Health",
		Description: "[S] Rank ALL registered projects by code health (worst first). Reads local_path itself — no files needed.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer", "description": "max ranked projects to return (default all)"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.Registry == nil {
				return nil, &ToolError{Code: "unavailable", Message: "registry not configured"}
			}
			var in struct {
				Limit int `json:"limit"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, &ToolError{Code: "invalid_input", Cause: err}
				}
			}
			projects := make([]portfolioquality.Project, 0)
			for _, p := range d.Registry.List() {
				projects = append(projects, portfolioquality.Project{Slug: p.Slug, LocalPath: p.LocalPath})
			}
			rep := portfolioquality.Rank(projects, nil)
			if in.Limit > 0 && len(rep.Projects) > in.Limit {
				rep.Projects = rep.Projects[:in.Limit]
			}
			return rep, nil
		},
	}
}

// buildChurnRiskTool は登録プロジェクトの *時間軸* を解析する(v0.119.0)。
//
// 研究的根拠:
//   - Nagappan & Ball, ICSE 2005: 絶対 churn は defect density の予測子として貧弱だが、
//     サイズ・時間幅で正規化した相対 churn(M1-M8)は fault-prone を 89.0% で判別する。
//     よって順位付けは **相対** churn で行う(絶対 churn では並べない)。
//   - Tornhill の behavioral code analysis: 本当に危険なのは「頻繁に変わる複雑なコード」。
//     RiskScore = 相対 churn × 複雑度 がこの規則。
//
// v0.118.0 の portfolio_quality と同じく **files を受け取らない**——slug から local_path を
// 解決し daemon 側で git log とソースを読むので、内容は LLM context を通らない。
func buildChurnRiskTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_churn_risk",
		Title:       "Churn Risk (Temporal Hotspots)",
		Description: "[S] Rank files by relative churn x complexity from git history (Nagappan-Ball M1-M8 + Tornhill hotspots). Needs only a slug.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: true},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":        map[string]any{"type": "string", "description": "registered project slug"},
				"max_commits": map[string]any{"type": "integer", "description": "commits to walk back (default 500)"},
				"limit":       map[string]any{"type": "integer", "description": "max ranked files to return"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.Registry == nil {
				return nil, &ToolError{Code: "unavailable", Message: "registry not configured"}
			}
			var in struct {
				Slug       string `json:"slug"`
				MaxCommits int    `json:"max_commits"`
				Limit      int    `json:"limit"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug required"}
			}
			p, err := d.Registry.Get(in.Slug)
			if err != nil || p == nil {
				return nil, &ToolError{Code: "not_found", Message: "project not registered"}
			}
			if p.LocalPath == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "project has no local_path; set it with yagura_update"}
			}
			logOut, err := churn.ReadGitLog(ctx, p.LocalPath, in.MaxCommits)
			if err != nil {
				// 履歴が取れないことを明示的な失敗にする(churn 0 を「安全」と誤読させない)
				return nil, &ToolError{Code: "no_history", Message: err.Error()}
			}
			commits, err := churn.Parse(logOut)
			if err != nil {
				return nil, &ToolError{Code: "parse_failed", Cause: err}
			}
			sr, err := srcfiles.ReadGo(p.LocalPath)
			if err != nil {
				return nil, &ToolError{Code: "read_failed", Message: err.Error()}
			}
			sizes := make(map[string]int, len(sr.Files))
			for path, content := range sr.Files {
				sizes[path] = strings.Count(content, "\n") + 1
			}
			cx := map[string]int{}
			for _, f := range complexity.Scan(sr.Files, 10).Functions {
				if f.Complexity > cx[f.File] {
					cx[f.File] = f.Complexity
				}
			}
			rep := churn.Analyze(commits, sizes, cx)
			if in.Limit > 0 && len(rep.Files) > in.Limit {
				rep.Files = rep.Files[:in.Limit]
			}
			return map[string]any{
				"slug":         in.Slug,
				"report":       rep,
				"files_read":   len(sr.Files),
				"files_total":  sr.Matched,
				"incomplete":   sr.Incomplete(),
				"truncated_by": sr.TruncatedBy,
			}, nil
		},
	}
}

// buildOwnershipTool は「誰がそのコードを書いたか」を計測する(v0.120.0)。
//
// 研究的根拠: Bird et al., "Don't Touch My Code!", ESEC/FSE 2011 — minor 寄与者数と
// 最大所有者の所有割合が pre-release fault / post-release failure と関係する。
// Minor/Major/Total/Ownership の 4 指標は論文どおり(閾値 5%)、ランキングは
// Ownership 昇順(所有権が低いほど危険)。
//
// ai_proportion / human_ownership は **論文外の拡張**(ヒューリスティック判定)であり、
// 研究の裏付けがある 4 指標とは分けて解釈すること。
func buildOwnershipTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_ownership",
		Title:       "Code Ownership (Bird et al. FSE 2011)",
		Description: "[S] Per-file ownership: Minor/Major/Total/Ownership (Bird FSE2011, 5% threshold) + AI-authorship extension. Needs only a slug.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: true},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":        map[string]any{"type": "string", "description": "registered project slug"},
				"max_commits": map[string]any{"type": "integer", "description": "commits to walk back (default 500)"},
				"limit":       map[string]any{"type": "integer", "description": "max files to return (lowest ownership first)"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.Registry == nil {
				return nil, &ToolError{Code: "unavailable", Message: "registry not configured"}
			}
			var in struct {
				Slug       string `json:"slug"`
				MaxCommits int    `json:"max_commits"`
				Limit      int    `json:"limit"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug required"}
			}
			p, err := d.Registry.Get(in.Slug)
			if err != nil || p == nil {
				return nil, &ToolError{Code: "not_found", Message: "project not registered"}
			}
			if p.LocalPath == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "project has no local_path; set it with yagura_update"}
			}
			logOut, err := churn.ReadGitLog(ctx, p.LocalPath, in.MaxCommits)
			if err != nil {
				return nil, &ToolError{Code: "no_history", Message: err.Error()}
			}
			commits, err := churn.Parse(logOut)
			if err != nil {
				return nil, &ToolError{Code: "parse_failed", Cause: err}
			}
			// 現存する Go ファイルだけを対象にする(削除済みパスを混ぜない)
			sr, err := srcfiles.ReadGo(p.LocalPath)
			if err != nil {
				return nil, &ToolError{Code: "read_failed", Message: err.Error()}
			}
			only := make(map[string]bool, len(sr.Files))
			for path := range sr.Files {
				only[path] = true
			}
			rep := ownership.Analyze(commits, only)
			if in.Limit > 0 && len(rep.Files) > in.Limit {
				rep.Files = rep.Files[:in.Limit]
			}
			return map[string]any{
				"slug":              in.Slug,
				"report":            rep,
				"minor_threshold":   ownership.MinorThreshold,
				"research_metrics":  []string{"total_contributors", "minor_contributors", "major_contributors", "ownership"},
				"extension_metrics": []string{"ai_proportion", "human_ownership", "top_human_owner", "fully_ai_authored"},
			}, nil
		},
	}
}

// buildProcessRiskTool は churn(v0.119.0)と ownership(v0.120.0)の
// **プロセス指標のみ**を合成して順位付けする(v0.121.0)。
//
// 研究的根拠: Rahman & Devanbu (ICSE 2013) と Majumder/Mody/Menzies (EMSE 2022、
// 700 プロジェクト / 722,471 コミット)——プロセス指標 AUC ~95% に対し製品指標は
// ~54%(ほぼ偶然)。よって複雑度は **表示のみで採点に使わない**。
// v0.119.0 の churn RiskScore(相対churn × 複雑度)への自己反証でもある。
func buildProcessRiskTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_process_risk",
		Title:       "Process-Metric Risk (churn + ownership)",
		Description: "[S] Rank files by PROCESS metrics only (churn + ownership); complexity shown but not scored (product metrics ~AUC 54%). Needs only a slug.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: true},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":          map[string]any{"type": "string", "description": "registered project slug"},
				"max_commits":   map[string]any{"type": "integer", "description": "commits to walk back (default 500)"},
				"limit":         map[string]any{"type": "integer", "description": "max files to return"},
				"gap_days":      map[string]any{"type": "integer", "description": "days to leave between feature and label windows in validation (verification latency; default 0)"},
				"window_days":   map[string]any{"type": "integer", "description": "sliding feature-window size in days for validation (0 = expanding, the default)"},
				"file_cost_loc": map[string]any{"type": "integer", "description": "LOC-equivalent cost of OPENING one file in the effort-aware metric (default 0 = free). Which scorer wins depends on this; at 0 smallest-file-first wins 7/8 repositories, at 400 only 1/8."},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.Registry == nil {
				return nil, &ToolError{Code: "unavailable", Message: "registry not configured"}
			}
			var in struct {
				Slug        string `json:"slug"`
				MaxCommits  int    `json:"max_commits"`
				Limit       int    `json:"limit"`
				GapDays     int    `json:"gap_days"`
				WindowDays  int    `json:"window_days"`
				FileCostLOC int    `json:"file_cost_loc"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug required"}
			}
			p, err := d.Registry.Get(in.Slug)
			if err != nil || p == nil {
				return nil, &ToolError{Code: "not_found", Message: "project not registered"}
			}
			if p.LocalPath == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "project has no local_path; set it with yagura_update"}
			}
			logOut, err := churn.ReadGitLog(ctx, p.LocalPath, in.MaxCommits)
			if err != nil {
				return nil, &ToolError{Code: "no_history", Message: err.Error()}
			}
			commits, err := churn.Parse(logOut)
			if err != nil {
				return nil, &ToolError{Code: "parse_failed", Cause: err}
			}
			sr, err := srcfiles.ReadGo(p.LocalPath)
			if err != nil {
				return nil, &ToolError{Code: "read_failed", Message: err.Error()}
			}
			sizes := make(map[string]int, len(sr.Files))
			only := make(map[string]bool, len(sr.Files))
			for path, content := range sr.Files {
				sizes[path] = strings.Count(content, "\n") + 1
				only[path] = true
			}
			cx := map[string]int{}
			for _, f := range complexity.Scan(sr.Files, 10).Functions {
				if f.Complexity > cx[f.File] {
					cx[f.File] = f.Complexity
				}
			}
			chRep := churn.Analyze(commits, sizes, cx)
			ownRep := ownership.Analyze(commits, only)
			rep := processrisk.Score(chRep.Files, ownRep.Files)
			// v0.123.0: SZZ 第 1 段(fix コミット特定)でランキングを自己較正する。
			// commits は既に手元にあるので git 読み出しは 1 回のまま(単一 seam)。
			fixRep := fixhistory.Analyze(commits)
			ranking := make([]string, 0, len(rep.Files))
			for _, f := range rep.Files {
				ranking = append(ranking, f.Path)
			}
			inWindow := fixhistory.Validate(ranking, fixRep.FixesByFile, 10)
			// v0.125.0: 時系列順を保つ walk-forward を headline にする。
			// Falessi et al. (EMSE 2020) — 順序を無視した検証は誤った数字を出す。
			wf := walkforward.Run(commits, sizes, cx, walkforward.Options{GapDays: in.GapDays, WindowDays: in.WindowDays, FileCostLOC: in.FileCostLOC})
			// v0.122.0: 上位リスクを alert 化して注意配分レイヤへ供給する。
			// 件数は alertfix 側で Sadowski et al. の知見に基づき厳しく絞られる
			// (全件出すと「起票された bug の 84% が未修正」の失敗を再現する)。
			riskFiles := make([]alertfix.ProcessRiskFile, 0, len(rep.Files))
			for _, f := range rep.Files {
				riskFiles = append(riskFiles, alertfix.ProcessRiskFile{
					Path: f.Path, Score: f.Score, RelativeChurn: f.RelativeChurn,
					Ownership: f.Ownership, HasOwnership: f.HasOwnership, Reasons: f.Reasons,
				})
			}
			alerts := alertfix.EvaluateProcessRisk(in.Slug, riskFiles, alertfix.DefaultThresholds())
			if in.Limit > 0 && len(rep.Files) > in.Limit {
				rep.Files = rep.Files[:in.Limit]
			}
			return map[string]any{
				"slug":         in.Slug,
				"report":       rep,
				"alerts":       alerts,
				"files_read":   len(sr.Files),
				"files_total":  sr.Matched,
				"incomplete":   sr.Incomplete(),
				"truncated_by": sr.TruncatedBy,
				// v0.123.0: 自己較正(SZZ 第 1 段)。fix 履歴が無ければ valid=false。
				"fix_history": map[string]any{
					"fix_commits":   fixRep.FixCommits,
					"total_commits": fixRep.TotalCommits,
					"most_fixed":    fixRep.MostFixed,
				},
				// v0.125.0: headline は時系列順を保つ walk-forward。
				"validation": wf,
				// 同一窓の一致度も残すが、**予測の証拠ではない**ことを明示する
				// (v0.123.0 はこれを headline にしていた=誤り)。
				"in_window_agreement": map[string]any{
					"result":     inWindow,
					"predictive": false,
					"note": "Features and labels come from the SAME commit window, so this measures " +
						"agreement inside one window, not forecasting. Kept for comparison with the " +
						"walk-forward result above; see Falessi et al., EMSE 2020.",
				},
			}, nil
		},
	}
}

// buildDefectDatasetTool は各リポジトリの git 履歴から **ファイル単位の欠陥データセット**
// を生成する(v0.124.0、tool #106)。
//
// 研究的根拠: Zimmermann, Premraj & Zeller "Predicting Defects for Eclipse"(PROMISE 2007)
// の形式(行 = ファイル、列 = メトリクス + 欠陥ラベル)と **時間分割**に倣う。
// 既定で古い側 70% から特徴、新しい側 30% からラベルを作るので、特徴が未来を見ない。
// split_ratio=0 で分割を切れるが、その場合は応答の meta.leakage=true が立つ。
func buildDefectDatasetTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_defect_dataset",
		Title:       "Defect Dataset (PROMISE-style)",
		Description: "[S] Build a per-file defect dataset from git history (metrics + fix labels, temporally split). JSON or CSV. Needs only a slug.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: true},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":        map[string]any{"type": "string", "description": "registered project slug"},
				"max_commits": map[string]any{"type": "integer", "description": "commits to walk back (default 500)"},
				"split_ratio": map[string]any{"type": "number", "description": "feature-window share (default 0.7; 0 disables the split and flags leakage)"},
				"format":      map[string]any{"type": "string", "enum": []string{"json", "csv"}, "description": "output format (default json)"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.Registry == nil {
				return nil, &ToolError{Code: "unavailable", Message: "registry not configured"}
			}
			var in struct {
				Slug       string   `json:"slug"`
				MaxCommits int      `json:"max_commits"`
				SplitRatio *float64 `json:"split_ratio"`
				Format     string   `json:"format"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug required"}
			}
			p, err := d.Registry.Get(in.Slug)
			if err != nil || p == nil {
				return nil, &ToolError{Code: "not_found", Message: "project not registered"}
			}
			if p.LocalPath == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "project has no local_path; set it with yagura_update"}
			}
			logOut, err := churn.ReadGitLog(ctx, p.LocalPath, in.MaxCommits)
			if err != nil {
				return nil, &ToolError{Code: "no_history", Message: err.Error()}
			}
			commits, err := churn.Parse(logOut)
			if err != nil {
				return nil, &ToolError{Code: "parse_failed", Cause: err}
			}
			sr, err := srcfiles.ReadGo(p.LocalPath)
			if err != nil {
				return nil, &ToolError{Code: "read_failed", Message: err.Error()}
			}
			sizes := make(map[string]int, len(sr.Files))
			for path, content := range sr.Files {
				sizes[path] = strings.Count(content, "\n") + 1
			}
			cx := map[string]int{}
			for _, f := range complexity.Scan(sr.Files, 10).Functions {
				if f.Complexity > cx[f.File] {
					cx[f.File] = f.Complexity
				}
			}
			ratio := defectdataset.DefaultSplitRatio
			if in.SplitRatio != nil {
				ratio = *in.SplitRatio
			}
			ds := defectdataset.Build(commits, sizes, cx, defectdataset.Options{SplitRatio: ratio})
			out := map[string]any{
				"slug":         in.Slug,
				"meta":         ds.Meta,
				"files_read":   len(sr.Files),
				"files_total":  sr.Matched,
				"incomplete":   sr.Incomplete(),
				"truncated_by": sr.TruncatedBy,
			}
			if strings.EqualFold(in.Format, "csv") {
				out["csv"] = ds.CSV()
			} else {
				out["rows"] = ds.Rows
			}
			return out, nil
		},
	}
}

// buildChangeCouplingTool は git 履歴から **進化的結合**(一緒に変わるファイル対)を
// 算出し、その結合が将来の共変更を当てられるかを時系列分割で検証する(v0.128.0、tool #107)。
//
// 研究的根拠: Gall, Hajek & Jazayeri (ICSM 1998) の logical coupling と、
// Zimmermann らの ROSE (ICSE 2004 / TSE 2005) ——版履歴の association rule で
// 「次に変えるべき場所」を提案する。既定しきい値は同じ量を実装する code-maat の
// CLI 既定(min-revs 5 / min-shared-revs 5 / min-coupling 30 / max-changeset-size 30)
// に揃えてある。
//
// **検証は必ずベースラインと併記する。** 「よく変わるファイルを挙げるだけ」の頻度
// ベースラインは驚くほど強く、実測ではこれを安定して上回れない。応答は confidence 順と
// lift 順の **両方** の検証結果を返す——どちらか都合のいい方だけを見せないため。
func buildChangeCouplingTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_change_coupling",
		Title:       "Change Coupling (files that change together)",
		Description: "[S] Find files that change together in git history, and test whether that predicts future co-changes. Needs only a slug.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: true},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":                map[string]any{"type": "string", "description": "registered project slug"},
				"max_commits":         map[string]any{"type": "integer", "description": "commits to walk back (default 500)"},
				"limit":               map[string]any{"type": "integer", "description": "max coupled pairs to return"},
				"min_revs":            map[string]any{"type": "integer", "description": "minimum revisions per file (default 5, the code-maat default)"},
				"min_shared_revs":     map[string]any{"type": "integer", "description": "minimum shared revisions per pair (default 5)"},
				"min_degree":          map[string]any{"type": "number", "description": "minimum symmetric coupling degree 0-1 (default 0.30)"},
				"max_changeset_files": map[string]any{"type": "integer", "description": "commits touching more files than this are excluded (default 30); sweeping commits fabricate coupling"},
				"split_ratio":         map[string]any{"type": "number", "description": "share of history used to mine rules before validating on the rest (default 0.7)"},
				"k":                   map[string]any{"type": "integer", "description": "suggestions per seed file when validating (default 3)"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.Registry == nil {
				return nil, &ToolError{Code: "unavailable", Message: "registry not configured"}
			}
			var in struct {
				Slug              string   `json:"slug"`
				MaxCommits        int      `json:"max_commits"`
				Limit             int      `json:"limit"`
				MinRevs           int      `json:"min_revs"`
				MinSharedRevs     int      `json:"min_shared_revs"`
				MinDegree         *float64 `json:"min_degree"`
				MaxChangesetFiles int      `json:"max_changeset_files"`
				SplitRatio        *float64 `json:"split_ratio"`
				K                 int      `json:"k"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug required"}
			}
			p, err := d.Registry.Get(in.Slug)
			if err != nil || p == nil {
				return nil, &ToolError{Code: "not_found", Message: "project not registered"}
			}
			if p.LocalPath == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "project has no local_path; set it with yagura_update"}
			}
			logOut, err := churn.ReadGitLog(ctx, p.LocalPath, in.MaxCommits)
			if err != nil {
				return nil, &ToolError{Code: "no_history", Message: err.Error()}
			}
			commits, err := churn.Parse(logOut)
			if err != nil {
				return nil, &ToolError{Code: "parse_failed", Cause: err}
			}

			opts := cochange.DefaultOptions()
			if in.MinRevs > 0 {
				opts.MinRevs = in.MinRevs
			}
			if in.MinSharedRevs > 0 {
				opts.MinSharedRevs = in.MinSharedRevs
			}
			if in.MinDegree != nil {
				opts.MinDegree = *in.MinDegree
			}
			if in.MaxChangesetFiles > 0 {
				opts.MaxChangesetFiles = in.MaxChangesetFiles
			}
			opts.Limit = in.Limit

			rep := cochange.Analyze(commits, opts)

			ratio := 0.7
			if in.SplitRatio != nil {
				ratio = *in.SplitRatio
			}
			k := in.K
			if k <= 0 {
				k = 3
			}
			train, test := cochange.Split(commits, ratio)
			// **limit は表示用であって検証用ではない**。同じ opts をそのまま渡すと
			// 「上位 N 件だけ返して」という要求が、測定に使う規則の数まで黙って
			// 削ってしまい、precision も baseline も変わってしまう(実測で発見)。
			evalOpts := opts
			evalOpts.Limit = 0
			// confidence 順と lift 順の **両方** を返す。lift は基準率交絡
			// (毎回変わるファイルがどの相手からも高 confidence に見える)の補正。
			byConfidence := cochange.Evaluate(train, test, evalOpts, k)
			liftOpts := evalOpts
			liftOpts.RankByLift = true
			byLift := cochange.Evaluate(train, test, liftOpts, k)

			return map[string]any{
				"slug":   in.Slug,
				"report": rep,
				"validation": map[string]any{
					"by_confidence": byConfidence,
					"by_lift":       byLift,
					"note": "Both rankings are returned so neither can be cherry-picked. Read each " +
						"precision against ITS OWN baseline: a frequency baseline that always names the " +
						"most-changed files of the train window is a strong opponent in repositories with " +
						"a release ritual, and beating it is the only thing that makes mined coupling " +
						"worth anything. lift < 1 means the mined rules did WORSE than naming busy files.",
				},
			}, nil
		},
	}
}
