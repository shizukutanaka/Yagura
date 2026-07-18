// tools_handoff.go: extracted from tools.go in v0.29 (gstack-style topic split).
//
// All tools in this file are registered via RegisterDefaultTools in tools.go.
// See CLAUDE.md Workflows for the registration pattern.
//
// ════════════════════════════════════════════════════════════════════════
// v0.13.0: Agent Handoff Layer
// ────────────────────────────────────────────────────────────────────────
//
// 5 つの MCP tool で Claude Code ↔ Windsurf の自動切替を実現する。
//
// Tool 一覧:
//   yagura_quota_report   — agent 側が残量を能動報告
//   yagura_agent_status   — 現在の状態と推奨 agent を取得
//   yagura_session_save   — handoff 用 session context を保存
//   yagura_session_load   — 保存済み context を読み込み
//   yagura_handoff        — 切替実行(save → MarkSwitched → launch)
//
// 使用フロー:
//   1. Claude Code が `/usage` で残量低下を検出
//   2. yagura_quota_report(agent="claude_code", remaining=15, source="auto")
//   3. yagura_agent_status を呼び ShouldHandoff=true を確認
//   4. yagura_session_save({workspace, branch, plan_md_step, ...})
//   5. yagura_handoff(target="windsurf") → Windsurf 起動 + state 更新
//   6. Cascade が起動後、yagura_session_load で context 取得して継続
// ════════════════════════════════════════════════════════════════════════

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shizukutanaka/yagura/internal/handoff"
	"github.com/shizukutanaka/yagura/internal/quotamonitor"
	"time"
)

func buildQuotaReportTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_quota_report",
		Title:       "Report Agent Quota",
		Annotations: &ToolAnnotations{ReadOnlyHint: false, DestructiveHint: false, IdempotentHint: false, OpenWorldHint: false},
		Description: "[S] Report agent quota. Triggers auto-handoff.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{
					"type": "string",
				},
				"remaining_percent": map[string]any{
					"type": "integer",
				},
				"source": map[string]any{
					"type": "string",
				},
				"window_resets_at": map[string]any{
					"type": "string",
				},
				"weekly_resets_at": map[string]any{
					"type": "string",
				},
			},
			"required": []string{"agent", "remaining_percent"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.QuotaMonitor == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "quota monitor not configured"}
			}
			var in struct {
				Agent            string `json:"agent"`
				RemainingPercent int    `json:"remaining_percent"`
				Source           string `json:"source"`
				WindowResetsAt   string `json:"window_resets_at"`
				WeeklyResetsAt   string `json:"weekly_resets_at"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			agent, err := quotamonitor.AgentFromString(in.Agent)
			if err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: err.Error()}
			}
			source := in.Source
			if source == "" {
				source = "manual"
			}
			windowReset, _ := time.Parse(time.RFC3339, in.WindowResetsAt)
			weeklyReset, _ := time.Parse(time.RFC3339, in.WeeklyResetsAt)
			if err := d.QuotaMonitor.Report(agent, in.RemainingPercent, source, windowReset, weeklyReset); err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: err.Error()}
			}
			// 報告後の判定も返す(自動切替トリガー)
			should, target, reason := d.QuotaMonitor.ShouldHandoff(agent)
			st, _ := d.QuotaMonitor.Status(agent)
			return map[string]any{
				"recorded":       st,
				"should_handoff": should,
				"handoff_target": string(target),
				"handoff_reason": reason,
			}, nil
		},
	}
}

func buildAgentStatusTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_agent_status",
		Title:       "Get Agent Status",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[S] Get both agents' quota state + recommended next agent.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.QuotaMonitor == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "quota monitor not configured"}
			}
			all := d.QuotaMonitor.AllStatuses()
			recommended, reason := d.QuotaMonitor.Recommend()
			// map[Agent] is not stable JSON; convert to keyed object
			statuses := map[string]any{}
			for k, v := range all {
				statuses[string(k)] = v
			}
			return map[string]any{
				"statuses":              statuses,
				"recommended_agent":     string(recommended),
				"recommendation_reason": reason,
			}, nil
		},
	}
}

func buildSessionSaveTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_session_save",
		Title:       "Save Handoff Session",
		Annotations: &ToolAnnotations{ReadOnlyHint: false, DestructiveHint: true, IdempotentHint: true, OpenWorldHint: false},
		Description: "[S] Save handoff context.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type": "string",
				},
				"saved_by": map[string]any{
					"type": "string",
				},
				"branch": map[string]any{
					"type": "string",
				},
				"last_commit": map[string]any{
					"type": "string",
				},
				"active_files": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"plan_md_step": map[string]any{
					"type": "string",
				},
				"open_todos": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"file": map[string]any{"type": "string"},
							"line": map[string]any{"type": "integer"},
							"kind": map[string]any{"type": "string"},
							"text": map[string]any{"type": "string"},
						},
					},
				},
				"free_notes": map[string]any{
					"type": "string",
				},
			},
			"required": []string{"workspace"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.HandoffStore == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "handoff store not configured"}
			}
			var in handoff.Context
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Workspace == "" && d.WorkspaceRoot != "" {
				in.Workspace = d.WorkspaceRoot
			}
			if err := d.HandoffStore.Save(&in); err != nil {
				return nil, &ToolError{Code: "save_failed", Message: err.Error()}
			}
			return map[string]any{
				"saved":     true,
				"path":      d.HandoffStore.Path(),
				"saved_at":  in.SavedAt,
				"workspace": in.Workspace,
			}, nil
		},
	}
}

func buildSessionLoadTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_session_load",
		Title:       "Load Handoff Session",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[S] Load handoff context. Null if none.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.HandoffStore == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "handoff store not configured"}
			}
			c, err := d.HandoffStore.Load()
			if err != nil {
				if errors.Is(err, handoff.ErrNotSaved) {
					return map[string]any{
						"context": nil,
						"note":    "no handoff context saved",
					}, nil
				}
				return nil, &ToolError{Code: "load_failed", Message: err.Error()}
			}
			return map[string]any{
				"context": c,
				"note":    "load this context to resume work from where the previous agent left off",
			}, nil
		},
	}
}

func buildHandoffTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_handoff",
		Title:       "Execute Agent Handoff",
		Annotations: &ToolAnnotations{ReadOnlyHint: false, DestructiveHint: true, IdempotentHint: false, OpenWorldHint: true},
		Description: "[S] Handoff: save + mark + launch target. dry_run optional.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{
					"type": "string",
				},
				"workspace": map[string]any{
					"type": "string",
				},
				"free_notes": map[string]any{
					"type": "string",
				},
				"dry_run": map[string]any{
					"type": "boolean",
				},
			},
			"required": []string{"target"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.QuotaMonitor == nil || d.HandoffStore == nil || d.AgentLauncher == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "agent handoff requires quota monitor, handoff store, and launcher"}
			}
			var in struct {
				Target    string `json:"target"`
				Workspace string `json:"workspace"`
				FreeNotes string `json:"free_notes"`
				DryRun    bool   `json:"dry_run"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			target, err := quotamonitor.AgentFromString(in.Target)
			if err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: err.Error()}
			}
			workspace, terr := resolveHandoffWorkspace(d, in.Workspace)
			if terr != nil {
				return nil, terr
			}

			// (1) handoff context を保存
			source := handoffSource(target)
			ctxObj := &handoff.Context{
				Version:   1,
				SavedAt:   d.Now().UTC(),
				SavedBy:   string(source),
				Workspace: workspace,
				FreeNotes: in.FreeNotes,
			}
			if err := d.HandoffStore.Save(ctxObj); err != nil {
				return nil, &ToolError{Code: "save_failed", Message: err.Error()}
			}

			// (2) source agent を SWITCHED 状態にマーク
			if err := d.QuotaMonitor.MarkSwitched(source); err != nil {
				return nil, &ToolError{Code: "monitor_failed", Message: err.Error()}
			}

			// (3) target agent を launch(dry_run なら skip)
			if terr := launchTargetAgent(ctx, d, target, workspace, in.DryRun); terr != nil {
				return nil, terr
			}

			cmd, cmdArgs := d.AgentLauncher.LastCommand()
			return map[string]any{
				"handoff_complete": !in.DryRun,
				"source_agent":     string(source),
				"target_agent":     string(target),
				"workspace":        workspace,
				"context_saved_to": d.HandoffStore.Path(),
				"launch_command":   append([]string{cmd}, cmdArgs...),
				"dry_run":          in.DryRun,
			}, nil
		},
	}
}

// resolveHandoffWorkspace は明示 workspace、無ければ daemon の WorkspaceRoot を返す。
// どちらも空なら error。
func resolveHandoffWorkspace(d Deps, workspace string) (string, *ToolError) {
	if workspace == "" {
		workspace = d.WorkspaceRoot
	}
	if workspace == "" {
		return "", &ToolError{Code: "invalid_input",
			Message: "workspace required (and yagura WorkspaceRoot not configured)"}
	}
	return workspace, nil
}

// handoffSource は target の相手側 agent(= 現在動いている source)を返す。
func handoffSource(target quotamonitor.Agent) quotamonitor.Agent {
	if target == quotamonitor.AgentClaudeCode {
		return quotamonitor.AgentWindsurf
	}
	return quotamonitor.AgentClaudeCode
}

// launchTargetAgent は target に応じた agent を起動する(dryRun なら no-op)。
func launchTargetAgent(ctx context.Context, d Deps, target quotamonitor.Agent, workspace string, dryRun bool) *ToolError {
	if dryRun {
		return nil
	}
	var launchErr error
	switch target {
	case quotamonitor.AgentWindsurf:
		launchErr = d.AgentLauncher.LaunchWindsurf(ctx, workspace)
	case quotamonitor.AgentClaudeCode:
		launchErr = d.AgentLauncher.LaunchClaudeCode(ctx, workspace)
	}
	if launchErr != nil {
		return &ToolError{Code: "launch_failed", Message: launchErr.Error()}
	}
	return nil
}

func buildHeartbeatTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_heartbeat",
		Title:       "Record Agent Heartbeat",
		Annotations: &ToolAnnotations{ReadOnlyHint: false, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[S] Agent heartbeat (~5min). Detects stale agents.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{
					"type": "string",
				},
			},
			"required": []string{"agent"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.QuotaMonitor == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "quota monitor not configured"}
			}
			var in struct {
				Agent string `json:"agent"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			agent, err := quotamonitor.AgentFromString(in.Agent)
			if err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: err.Error()}
			}
			if err := d.QuotaMonitor.RecordHeartbeat(agent); err != nil {
				return nil, &ToolError{Code: "record_failed", Message: err.Error()}
			}
			return map[string]any{
				"recorded": true,
				"agent":    string(agent),
				"at":       d.Now().UTC().Format(time.RFC3339Nano),
			}, nil
		},
	}
}

func buildQuotaForecastTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_quota_forecast",
		Title:       "Forecast Quota Depletion",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[S] Empty-time linreg forecast. Needs ≥3 samples.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{
					"type": "string",
				},
			},
			"required": []string{"agent"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.QuotaMonitor == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "quota monitor not configured"}
			}
			var in struct {
				Agent string `json:"agent"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			agent, err := quotamonitor.AgentFromString(in.Agent)
			if err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: err.Error()}
			}
			result := d.QuotaMonitor.Forecast(agent)
			// 残り時間も人間向けに出す
			var minutesUntilEmpty string
			if !result.PredictedEmptyAt.IsZero() {
				remaining := time.Until(result.PredictedEmptyAt)
				if remaining > 0 {
					minutesUntilEmpty = remaining.Round(time.Minute).String()
				} else {
					minutesUntilEmpty = "0s (already passed prediction)"
				}
			}
			return map[string]any{
				"agent":               string(agent),
				"forecast":            result,
				"minutes_until_empty": minutesUntilEmpty,
			}, nil
		},
	}
}

func buildUsageSummaryTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_usage_summary",
		Title:       "Get Usage Summary",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[S] Agent usage summary + sparkline. Both agents by default.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{
					"type": "string",
				},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.QuotaMonitor == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "quota monitor not configured"}
			}
			var in struct {
				Agent string `json:"agent"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, &ToolError{Code: "invalid_input", Cause: err}
				}
			}
			if in.Agent == "" || in.Agent == "both" {
				return map[string]any{
					"summaries": d.QuotaMonitor.AllUsageSummaries(),
				}, nil
			}
			agent, err := quotamonitor.AgentFromString(in.Agent)
			if err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: err.Error()}
			}
			return map[string]any{
				"summary": d.QuotaMonitor.UsageSummary(agent),
			}, nil
		},
	}
}
