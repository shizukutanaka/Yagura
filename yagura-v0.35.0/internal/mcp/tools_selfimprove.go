// tools_selfimprove.go: yagura_self_improve — harness レベルの再帰的自己改善(RSI)を
// 安全な形で回すための決定論的カーネル(internal/selfimprove)。
//
// STOP(arXiv 2310.02304)が示すとおり、モデルを固定したまま最適化できるのは
// 「足場(harness)」であり、Yagura はその harness。本 tool は harness 自身の観測値
// (yagura_token_stats / yagura_skill_audit / yagura_harness_coverage 由来)を受け、
// 優先度つきの改善提案を rule-based に返す。前回窓(prev_tools)を渡すと、
// Darwin Gödel Machine(arXiv 2505.22954)流に「後退」を検出して採用/巻き戻しを助言する
// (盲目採用を避け、misevolution = arXiv 2509.26354 を防ぐ)。
//
// Yagura は自分のコードを書き換えない(ADR-0001 / 安全性)。判断は決定論的で audit 可能、
// 実行は人間/エージェント側。parallel_plan(1→N)・recovery_decide(失敗時)と並ぶ
// control-plane の一部。

package mcp

import (
	"context"
	"encoding/json"

	"github.com/shizukutanaka/yagura/internal/audit"
	"github.com/shizukutanaka/yagura/internal/selfimprove"
)

func buildSelfImproveTool(d Deps, getStats func() []ToolStats, emit func(audit.Record)) *Tool {
	return &Tool{
		Name:        "yagura_self_improve",
		Title:       "Self Improvement Proposals",
		Annotations: &ToolAnnotations{ReadOnlyHint: false, DestructiveHint: false, IdempotentHint: false, OpenWorldHint: false},
		Description: "[S] Turn harness self-metrics into ranked, gated improvement proposals (RSI, deterministic). Omit 'tools' to self-collect live stats; set record=true to append the assessment to the audit trail.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tools": map[string]any{
					"type":        "array",
					"description": "observed per-tool stats; OMIT to auto-collect this daemon's live token stats. [{name, calls, errors, avg_resp_bytes}].",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":           map[string]any{"type": "string"},
							"calls":          map[string]any{"type": "integer"},
							"errors":         map[string]any{"type": "integer"},
							"avg_resp_bytes": map[string]any{"type": "integer"},
						},
						"required": []string{"name"},
					},
				},
				"prev_tools": map[string]any{
					"type":        "array",
					"description": "previous window's tool stats; enables fitness/regression detection (revert advice).",
					"items":       map[string]any{"type": "object"},
				},
				"skills": map[string]any{
					"type":        "array",
					"description": "skill audit results (from yagura_skill_audit): [{path, score, retire}].",
					"items":       map[string]any{"type": "object"},
				},
				"coverage_gaps": map[string]any{
					"type":        "array",
					"description": "Fowler matrix quadrants with no control (from yagura_harness_coverage).",
					"items":       map[string]any{"type": "string"},
				},
				"session_calls": map[string]any{
					"type":        "integer",
					"description": "total tool calls in the window (used for token-economy call-share thresholds).",
				},
				"record": map[string]any{
					"type":        "boolean",
					"description": "append this assessment (counts + proposal ids) to the append-only audit log — the auditable RSI trajectory.",
				},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var snap selfimprove.Snapshot
			var opt struct {
				Record bool `json:"record"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &snap); err != nil {
					return nil, &ToolError{Code: "invalid_input", Cause: err}
				}
				json.Unmarshal(args, &opt) // record フラグ(snapshot とは別軸)
			}
			// 'tools' を省略したら、この daemon の live token stats を自己収集する
			// (RSI ループを実際に閉じる: harness が自分自身を観測して提案する)。
			selfCollected := false
			if len(snap.Tools) == 0 && getStats != nil {
				snap.Tools, snap.SessionCalls = collectLiveStats(getStats(), snap.SessionCalls)
				selfCollected = true
			}
			rep := selfimprove.Analyze(snap)

			// record=true なら自己評価を append-only audit log に残す。misevolution 研究
			// (arXiv 2509.26354)が求める「memories auditable」: 自己改善の軌跡を改ざん検出
			// 可能な hash chain に刻み、後から verify/diff で収束(非 misevolution)を確認できる。
			recorded := false
			if opt.Record && emit != nil {
				ids := make([]string, 0, len(rep.Proposals))
				for _, p := range rep.Proposals {
					ids = append(ids, p.ID)
				}
				emit(audit.Record{
					Kind:   "self_improve",
					Actor:  "mcp",
					Target: "harness",
					Fields: map[string]any{
						"summary":        rep.Summary,
						"by_severity":    rep.BySeverity,
						"by_kind":        rep.ByKind,
						"proposals":      ids,
						"self_collected": selfCollected,
					},
				})
				recorded = true
			}

			return map[string]any{
				"proposals":      rep.Proposals,
				"by_kind":        rep.ByKind,
				"by_severity":    rep.BySeverity,
				"summary":        rep.Summary,
				"self_collected": selfCollected,
				"recorded":       recorded,
			}, nil
		},
	}
}

// collectLiveStats は server の ToolStats を selfimprove.ToolStat に写像する。
// session_calls が未指定(0)なら全 tool の call 合計で埋める。
func collectLiveStats(stats []ToolStats, sessionCalls uint64) ([]selfimprove.ToolStat, uint64) {
	out := make([]selfimprove.ToolStat, 0, len(stats))
	var total uint64
	for _, s := range stats {
		total += s.Calls
		avg := 0
		if s.Calls > 0 {
			avg = int(s.ResponseBytes / s.Calls)
		}
		out = append(out, selfimprove.ToolStat{
			Name:         s.Name,
			Calls:        s.Calls,
			Errors:       s.ErrorCount,
			AvgRespBytes: avg,
		})
	}
	if sessionCalls == 0 {
		sessionCalls = total
	}
	return out, sessionCalls
}
