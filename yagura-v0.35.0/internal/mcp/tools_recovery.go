// tools_recovery.go: yagura_recovery_decide — self-healing orchestration の決定論的
// recovery 判断(internal/recovery)。
//
// MCP client が task の失敗を報告すると、Yagura が failure class + 試行回数 + budget から
// 次の recovery action(retry / backoff_retry / repair_args / substitute_tool /
// substitute_agent / refresh_context / replan / degrade / escalate)を根拠付きで返す。
// 実際の実行は client 側。parallel_plan(1→N dispatch)と組で reliability control plane
// を成す。Yagura は LLM を呼ばず、判断は rule-based で再現可能(audit/HITL 前提)。

package mcp

import (
	"context"
	"encoding/json"

	"github.com/shizukutanaka/yagura/internal/recovery"
)

func buildRecoveryDecideTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_recovery_decide",
		Title:       "Decide Recovery Action",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Pick the next recovery action for a failed agent task (retry/replan/escalate...).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"class": map[string]any{
					"type":        "string",
					"description": "failure class: timeout/rate_limit/tool_init/bad_args/tool_error/auth/quota/context_overflow/wrong_result/unknown (aliases like 429/403 accepted).",
				},
				"attempt":      map[string]any{"type": "integer", "description": "1-based attempt count for this task."},
				"max_attempts": map[string]any{"type": "integer", "description": "recovery budget (default 3)."},
				"agent":        map[string]any{"type": "string", "description": "current agent (used for substitute decisions)."},
				"severity":     map[string]any{"type": "string", "description": "'low' lets an exhausted budget degrade gracefully instead of escalating."},
			},
			"required": []string{"class"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Class       string `json:"class"`
				Attempt     int    `json:"attempt"`
				MaxAttempts int    `json:"max_attempts"`
				Agent       string `json:"agent"`
				Severity    string `json:"severity"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Class == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "failure 'class' is required"}
			}
			return recovery.Decide(recovery.Event{
				Class:       recovery.FailureClass(in.Class),
				Attempt:     in.Attempt,
				MaxAttempts: in.MaxAttempts,
				Agent:       in.Agent,
				Severity:    in.Severity,
			}), nil
		},
	}
}
