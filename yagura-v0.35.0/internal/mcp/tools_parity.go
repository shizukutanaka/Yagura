// tools_parity.go: MCP tools closing the CLI→MCP parity gap surfaced by a
// Socratic self-audit (v0.102.0). Eight CLI verbs (coverage/diff-scan/
// flow-risk/cc-security/claudemd-audit/review-gate/alert-snapshot/
// self-improve-history) had domain logic but no MCP tool, even though MCP is
// the primary Claude Code integration surface. All are pure functions or
// reuse an existing Deps-provided dependency — no new architecture needed.
package mcp

import (
	"context"
	"encoding/json"

	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/audit"
	"github.com/shizukutanaka/yagura/internal/ccsecurity"
	"github.com/shizukutanaka/yagura/internal/config"
	"github.com/shizukutanaka/yagura/internal/coverage"
	"github.com/shizukutanaka/yagura/internal/diffscan"
	"github.com/shizukutanaka/yagura/internal/flowrisk"
	"github.com/shizukutanaka/yagura/internal/harness"
	"github.com/shizukutanaka/yagura/internal/reviewgate"
)

// ─── yagura_coverage (v0.102.0) ────────────────────────────────

func buildCoverageTool(_ Deps) *Tool {
	return &Tool{
		Name:        "yagura_coverage",
		Title:       "Classify Path Coverage",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Blind-spot meta lens: classifies paths analyzable/uncovered/non-source. Reports both sensor-tier and Go-only AST-lens-tier ratios.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"paths"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Paths []string `json:"paths"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Paths) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "paths required"}
			}
			return coverage.Classify(in.Paths), nil
		},
	}
}

// ─── yagura_diff_scan (v0.102.0) ───────────────────────────────

func buildDiffScanTool(_ Deps) *Tool {
	return &Tool{
		Name:        "yagura_diff_scan",
		Title:       "Scan Unified Diff",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Unified-diff delta lens: added lines, removed lines, and removed safety guards (error-check/recover/cleanup).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"diff": map[string]any{"type": "string"},
			},
			"required": []string{"diff"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Diff string `json:"diff"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Diff == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "diff required"}
			}
			return map[string]any{
				"added_lines":    diffscan.AddedLines(in.Diff),
				"removed_lines":  diffscan.RemovedLines(in.Diff),
				"removed_guards": diffscan.RemovedGuards(in.Diff),
			}, nil
		},
	}
}

// ─── yagura_flow_risk (v0.102.0) ───────────────────────────────

func buildFlowRiskTool(_ Deps) *Tool {
	return &Tool{
		Name:        "yagura_flow_risk",
		Title:       "Analyze Flow Risk",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Temporal/flow lens: detects dangerous operation-sequence orderings (secret-read->network, fetch-untrusted->exec/write).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"steps": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":       map[string]any{"type": "string"},
							"capability": map[string]any{"type": "string"},
						},
						"required": []string{"name", "capability"},
					},
				},
			},
			"required": []string{"steps"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Steps []flowrisk.Step `json:"steps"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Steps) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "steps required"}
			}
			return map[string]any{"risks": flowrisk.Analyze(in.Steps)}, nil
		},
	}
}

// ─── yagura_cc_security (v0.102.0) ─────────────────────────────

func buildCCSecurityTool(_ Deps) *Tool {
	return &Tool{
		Name:        "yagura_cc_security",
		Title:       "Audit Claude Code Security",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] Claude Code project security posture audit. Client supplies gathered facts (gitignore/CLAUDE.md/settings.json contents); server scores deterministically.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"env_files":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"has_gitignore":    map[string]any{"type": "boolean"},
				"gitignore":        map[string]any{"type": "string"},
				"has_git_dir":      map[string]any{"type": "boolean"},
				"has_claude_md":    map[string]any{"type": "boolean"},
				"claude_md":        map[string]any{"type": "string"},
				"has_settings":     map[string]any{"type": "boolean"},
				"settings_json":    map[string]any{"type": "string"},
				"has_worklog":      map[string]any{"type": "boolean"},
				"mcp_server_count": map[string]any{"type": "integer"},
				"extra_text": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string"},
							"text": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in ccsecurity.Input
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			return ccsecurity.Audit(in), nil
		},
	}
}

// ─── yagura_claudemd_audit (v0.102.0) ──────────────────────────

func buildClaudeMdAuditTool(_ Deps) *Tool {
	return &Tool{
		Name:        "yagura_claudemd_audit",
		Title:       "Audit CLAUDE.md Structure",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[Q] CLAUDE.md structural audit: canonical 4-section coverage, instruction count (Lost in the Middle), issues/suggestions.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string"},
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
			return harness.AuditClaudeMd(in.Content), nil
		},
	}
}

// ─── yagura_review_gate (v0.102.0) ─────────────────────────────

func buildReviewGateTool(_ Deps) *Tool {
	return &Tool{
		Name:        "yagura_review_gate",
		Title:       "Evaluate Review Gate",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] cortex flywheel Review synthesis: hard signals (secrets/critical AI risk/lint/AST-high) block; else AI-risk threshold gates review vs allow.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"secret_findings": map[string]any{"type": "integer"},
				"ai_risk_score":   map[string]any{"type": "integer"},
				"ai_critical":     map[string]any{"type": "integer"},
				"lint_prohibited": map[string]any{"type": "integer"},
				"ast_high":        map[string]any{"type": "integer"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in reviewgate.Signals
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			return reviewgate.Evaluate(in), nil
		},
	}
}

// ─── yagura_alert_snapshot (v0.102.0) ──────────────────────────

func buildAlertSnapshotTool(store *alertfix.Store) *Tool {
	return &Tool{
		Name:        "yagura_alert_snapshot",
		Title:       "Snapshot Alert States",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[S] Current alert lifecycle states (active/resolved/snoozed) + stats. Optional status filter.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{"type": "string", "enum": []string{"active", "resolved", "snoozed"}},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Status != "" {
				switch alertfix.LifecycleStatus(in.Status) {
				case alertfix.StatusActive, alertfix.StatusResolved, alertfix.StatusSnoozed:
				default:
					return nil, &ToolError{Code: "invalid_input", Message: "status must be active|resolved|snoozed"}
				}
			}
			snap := store.Snapshot()
			if in.Status != "" {
				want := alertfix.LifecycleStatus(in.Status)
				kept := snap[:0]
				for _, s := range snap {
					if s.Status == want {
						kept = append(kept, s)
					}
				}
				snap = kept
			}
			return map[string]any{
				"states":          snap,
				"lifecycle_stats": store.Stats(),
			}, nil
		},
	}
}

// ─── yagura_self_improve_history (v0.102.0) ────────────────────

func buildSelfImproveHistoryTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_self_improve_history",
		Title:       "Replay Self-Improve History",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[S] Replay the self_improve audit trail (past RSI assessments). Optional limit to last N.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Limit int `json:"limit"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, &ToolError{Code: "invalid_input", Cause: err}
				}
			}
			recs, err := audit.Read(config.AuditDirFor(d.StateDir), "self_improve")
			if err != nil {
				return nil, &ToolError{Code: "internal_error", Cause: err}
			}
			if in.Limit > 0 && len(recs) > in.Limit {
				recs = recs[len(recs)-in.Limit:]
			}
			return map[string]any{"count": len(recs), "assessments": recs}, nil
		},
	}
}
