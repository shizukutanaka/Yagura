// tools_sessionsummary.go: yagura_session_summary — エージェントのイベント列を
// セッション活動の構造化サマリへ集約する(internal/sessionsummary)。
//
// 着想(Hermes Desktop 参照): Hermes Desktop の目玉「ライブ tool activity と構造化 tool-call
// サマリ」の kernel 側 deterministic 版。`events` を直接渡すか、`slug`(+ optional `session`)で
// daemon が記録済みの hook timeline を要約する。どちらも agentevent.Normalize を通すので、
// Claude Code / Gemini CLI / Codex / OTel いずれのセッションでも同じサマリが得られる。
// UI(Hermes 風でも Yagura dashboard でも)はこれを render するだけ。LLM 不使用。

package mcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/shizukutanaka/yagura/internal/agentevent"
	"github.com/shizukutanaka/yagura/internal/hookreceiver"
	"github.com/shizukutanaka/yagura/internal/sessionsummary"
)

func buildSessionSummaryTool(d Deps, srv *Server) *Tool {
	return &Tool{
		Name:        "yagura_session_summary",
		Title:       "Session Summary",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[S] Structured tool-call summary of an agent session (any agent): pass events, or a slug to summarize the recorded timeline.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"events": map[string]any{
					"type":        "array",
					"description": "chronological raw lifecycle events from any agent (each normalized via agent_event).",
					"items":       map[string]any{"type": "object"},
				},
				"slug": map[string]any{
					"type":        "string",
					"description": "summarize the daemon's recorded hook timeline for this project instead of passing events.",
				},
				"session": map[string]any{
					"type":        "string",
					"description": "with slug: restrict to one session id.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "with slug: max recorded events to consider (default 500).",
				},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Events  []map[string]any `json:"events"`
				Slug    string           `json:"slug"`
				Session string           `json:"session"`
				Limit   int              `json:"limit"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, &ToolError{Code: "invalid_input", Cause: err}
				}
			}

			var norm []agentevent.Event
			switch {
			case len(in.Events) > 0:
				norm = make([]agentevent.Event, 0, len(in.Events))
				for _, raw := range in.Events {
					norm = append(norm, agentevent.Normalize(raw))
				}
			case in.Slug != "":
				hr := srv.HookReceiver()
				if hr == nil {
					return nil, &ToolError{Code: "unavailable", Message: "hook receiver not configured"}
				}
				sum, _ := srv.RecordedSummary(in.Slug, in.Session, in.Limit)
				return sum, nil
			default:
				return nil, &ToolError{Code: "invalid_input", Message: "provide either 'events' or 'slug'"}
			}

			return sessionsummary.Summarize(norm), nil
		},
	}
}

// RecordedSummary summarizes the daemon's recorded hook timeline for a project.
// 同じ pipeline(Timeline → recordedToEvents → Summarize)を yagura_session_summary
// と dashboard の活動ドリルダウンの双方が使う(single source of truth)。ok は記録が
// 1 件でもあれば true。hook receiver 未設定なら空サマリ + false。
func (s *Server) RecordedSummary(slug, session string, limit int) (sessionsummary.Summary, bool) {
	hr := s.HookReceiver()
	if hr == nil {
		return sessionsummary.Summary{}, false
	}
	if limit <= 0 {
		limit = 500
	}
	recorded := hr.Timeline(slug, time.Time{}, "", limit)
	norm := recordedToEvents(recorded, session)
	return sessionsummary.Summarize(norm), len(norm) > 0
}

// recordedToEvents は記録済み hook event を agentevent.Event へ写す。
// 各 event を Normalize に通すので live 取り込みと同じ正規化規則になる(DRY)。
// session != "" のときはその session id だけに絞る。Timeline は新しい順なので逆順にして
// 時系列(古い順)へ戻す(連続エラー検出などが正しく効くように)。
func recordedToEvents(recorded []hookreceiver.Event, session string) []agentevent.Event {
	out := make([]agentevent.Event, 0, len(recorded))
	for i := len(recorded) - 1; i >= 0; i-- {
		e := recorded[i]
		if session != "" && e.SessionID != session {
			continue
		}
		raw := map[string]any{
			"hook_event_name": e.HookEventName,
			"tool_name":       e.ToolName,
			"session_id":      e.SessionID,
		}
		if e.DurationMS > 0 {
			raw["duration_ms"] = e.DurationMS
		}
		if e.AgentType != "" {
			raw["agent"] = e.AgentType
		}
		if e.IsError {
			raw["status"] = "error"
		}
		out = append(out, agentevent.Normalize(raw))
	}
	return out
}
