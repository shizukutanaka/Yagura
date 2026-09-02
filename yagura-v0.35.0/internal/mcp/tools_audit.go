// tools_audit.go: .claude/ artifact 監査を MCP からも叩けるようにする content-based tool 群。
//
// CLI(yagura workflow-audit 等)は disk を走査するが、MCP 駆動の agent は手元の
// content を渡して監査したい。既存の yagura_skill_audit / yagura_subagent_audit と同じく
// content を受けて pure な harness.AuditX / publicityscan.Scan を呼ぶだけの薄い wrapper。
// これで監査ファミリ(skill / subagent / workflow / settings / agent-config / plugin /
// publicity)が CLI と MCP の両サーフェスで揃う。

package mcp

import (
	"context"
	"encoding/json"

	"github.com/shizukutanaka/yagura/internal/harness"
	"github.com/shizukutanaka/yagura/internal/injectscan"
	"github.com/shizukutanaka/yagura/internal/publicityscan"
)

// contentAuditAnnotations は content-audit ファミリ共通の振る舞いヒント。
// 7 tool すべてが content 文字列を受けて pure な audit 関数を呼ぶだけ(disk/
// registry/network に触れない)——read-only・非破壊・冪等・closed-world。
// v0.113.0。
var contentAuditAnnotations = &ToolAnnotations{
	ReadOnlyHint:    true,
	DestructiveHint: false,
	IdempotentHint:  true,
	OpenWorldHint:   false,
}

// contentAuditTool は「content 文字列を受けて任意の audit 関数を呼ぶ」共通ビルダー。
func contentAuditTool(name, title, desc, contentDesc string, audit func(string) any) *Tool {
	return &Tool{
		Name:        name,
		Title:       title,
		Description: desc,
		Annotations: contentAuditAnnotations,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string", "description": contentDesc},
			},
			"required": []string{"content"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Content == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "content required"}
			}
			return audit(in.Content), nil
		},
	}
}

func buildWorkflowAuditTool(d Deps) *Tool {
	return contentAuditTool("yagura_workflow_audit",
		"Audit Workflow Script",
		"[G] Dynamic Workflow JS audit: token budget, /goal, quarantine, fan-out.",
		"full workflow JavaScript source",
		func(c string) any { return harness.AuditWorkflow(c) })
}

func buildSettingsAuditTool(d Deps) *Tool {
	return contentAuditTool("yagura_settings_audit",
		"Audit Settings JSON",
		"[G] .claude/settings.json audit: permissions deny, unrestricted allow, hooks.",
		"full settings.json text",
		func(c string) any { return harness.AuditSettings(c) })
}

func buildAgentConfigAuditTool(d Deps) *Tool {
	return contentAuditTool("yagura_agent_config_audit",
		"Audit Agent Config",
		"[G] OpenClaw-style openclaw.json audit: security + reliability + model refs.",
		"full openclaw.json text",
		func(c string) any { return harness.AuditAgentConfig(c) })
}

func buildPluginAuditTool(d Deps) *Tool {
	return contentAuditTool("yagura_plugin_audit",
		"Audit Plugin Manifest",
		"[G] Claude Code plugin.json / marketplace.json audit (auto-detected).",
		"full plugin.json or marketplace.json text",
		func(c string) any { return harness.AuditPluginManifest(c) })
}

func buildPublicityScanTool(d Deps) *Tool {
	return contentAuditTool("yagura_publicity_scan",
		"Scan Content For Leaks",
		"[S] Pre-publish leak scan: home paths, internal hosts, private IPs, emails.",
		"text content to scan before publishing (SKILL.md, docs, diff)",
		func(c string) any {
			f := publicityscan.Scan(c)
			return map[string]any{"findings": f, "summary": publicityscan.Summarize(f)}
		})
}

func buildMCPAuditTool(d Deps) *Tool {
	return contentAuditTool("yagura_mcp_audit",
		"Audit MCP Config",
		"[S] MCP .mcp.json / tools audit: tool-poisoning, fetch|sh, unpinned npx, secrets.",
		"full .mcp.json server config or a tools/list JSON",
		func(c string) any { return harness.AuditMCPConfig(c) })
}

func buildInjectScanTool(d Deps) *Tool {
	return contentAuditTool("yagura_inject_scan",
		"Scan For Prompt Injection",
		"[S] Indirect prompt-injection scan of untrusted content: override/exfil/hidden/encoding (multilingual).",
		"untrusted text the agent ingested (fetched web page, issue body, tool output, file)",
		func(c string) any { return injectscan.Scan(c) })
}
