// tools_opsrisk.go: yagura_ops_risk — 操作ごとの「段階的自律性」を決定論的に分類する
// control-plane guardrail(internal/opsrisk)。
//
// Zenn の調査(2026)に基づく: ガバナンスは「使わせない」でなく「安全に使わせる」設計で、
// 低リスク=自動+ログ、高リスク=人間承認+監査+アラート(zenn.dev/miyan の5判断軸)。
// 判断ロジックは決定論的コードに置く(zenn.dev/imudak「判断はコード、提案はLLM」)。
// OWASP LLM08:2025 Excessive Agency の最小権限・consent も反映。
//
// path-policy が「どのパスを触ってよいか」を統治するのに対し、ops_risk は「その操作に
// どれだけ自律実行を許すか」を capability・可逆性・影響範囲から auto/log/review/human に
// 分類する。Yagura は LLM を呼ばず、tier と必要 control を根拠つきで返す。

package mcp

import (
	"context"
	"encoding/json"

	"github.com/shizukutanaka/yagura/internal/opsrisk"
)

func buildOpsRiskTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_ops_risk",
		Title:       "Classify Operation Risk",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Classify operations into an autonomy tier (auto/log/review/human) by capability/reversibility/blast.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operations": map[string]any{
					"type":        "array",
					"description": "operations to classify.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":         map[string]any{"type": "string"},
							"capability":   map[string]any{"type": "string", "description": "read|write|delete|exec|network|external|auth|billing|data."},
							"reversible":   map[string]any{"type": "boolean", "description": "can the operation be undone? (omit if unknown)"},
							"blast_radius": map[string]any{"type": "string", "description": "single|project|portfolio|external."},
							"has_gate":     map[string]any{"type": "boolean", "description": "is there a consent/confirmation gate before it runs?"},
						},
						"required": []string{"capability"},
					},
				},
			},
			"required": []string{"operations"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Operations []opsrisk.Op `json:"operations"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Operations) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "operations must list at least one operation"}
			}
			return opsrisk.ClassifyAll(in.Operations), nil
		},
	}
}
