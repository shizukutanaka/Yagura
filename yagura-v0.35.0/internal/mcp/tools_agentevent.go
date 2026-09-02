// tools_agentevent.go: yagura_agent_event — 任意のエージェントの lifecycle イベントを
// ベンダー中立な canonical イベント(OpenTelemetry GenAI semconv 整合)へ正規化する
// (internal/agentevent)。
//
// Yagura の governance / MCP サーフェスは元々エージェント非依存だが、observability の
// 取り込み口だけが Claude Code の hook 形式に結合していた。本 tool は Claude Code /
// Gemini CLI / Codex / 汎用 / OTel 形式のイベントを受け、`gen_ai.operation.name`
// (execute_tool/invoke_agent/chat) 等の標準語彙へ写す。これで hook_timeline / hook_stats /
// telemetry を「どのエージェントでも」効かせる土台になり、OTel 対応ツールとも相互運用できる。
// Yagura は LLM を呼ばず、決定論的に正規化する。

package mcp

import (
	"context"
	"encoding/json"

	"github.com/shizukutanaka/yagura/internal/agentevent"
)

func buildAgentEventTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_agent_event",
		Title:       "Normalize Agent Event",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Normalize any agent's lifecycle event (Claude Code/Gemini/Codex/OTel/generic) to OTel GenAI semconv.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"event": map[string]any{
					"type":        "object",
					"description": "the raw lifecycle event object from any agent's hook/telemetry.",
				},
			},
			"required": []string{"event"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Event map[string]any `json:"event"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Event) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "event object is required"}
			}
			e := agentevent.Normalize(in.Event)
			return map[string]any{
				"normalized":    e,
				"otel":          e.OTel(),
				"source_format": e.SourceFormat,
			}, nil
		},
	}
}
