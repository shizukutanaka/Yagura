// tools_pathpolicy.go: yagura_path_policy — 変更パス集合を glob ルールで
// deny / review / allow に決定論的に判定する control-plane guardrail
// (internal/pathpolicy)。
//
// エージェントは編集前に「この変更は許されるか」を問い合わせ、CI は PR の変更
// ファイル一覧を gate にかけられる。Yagura は LLM を呼ばず、最も厳しいマッチを
// 根拠つきで返す(deny ルールは shadow されない安全側)。parallel_plan / recovery_decide /
// self_improve と並ぶ deterministic control plane の一部。

package mcp

import (
	"context"
	"encoding/json"

	"github.com/shizukutanaka/yagura/internal/pathpolicy"
)

func buildPathPolicyTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_path_policy",
		Title:       "Evaluate Path Policy",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Gate changed paths against glob rules → deny/review/allow (strictest match wins).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"policy": map[string]any{
					"type":        "object",
					"description": "{rules:[{path(glob),action(deny|review|allow),reason?}], default?(default allow)}.",
				},
				"changed": map[string]any{
					"type":        "array",
					"description": "changed file paths to evaluate (e.g. git diff --name-only).",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required": []string{"policy", "changed"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Policy  pathpolicy.Policy `json:"policy"`
				Changed []string          `json:"changed"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Changed) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "changed must list at least one path"}
			}
			if err := in.Policy.Validate(); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			return pathpolicy.Evaluate(in.Policy, in.Changed), nil
		},
	}
}
