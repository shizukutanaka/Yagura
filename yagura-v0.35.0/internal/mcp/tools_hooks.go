// tools_hooks.go: v0.33「closing the loop」系 MCP tool。
//
// yagura_hook_timeline / yagura_hook_stats(hook 観測の MCP 化)と
// yagura_progress_file / yagura_init_sh(disk への artifacts 書出し)。
// tools.go の topic 別分割(Roadmap #1)の一環。対応テストは従来から
// tools_hooks_test.go / tools_plantools_test.go にある。登録順は不変。
// 共有 infra(version() / atomicWriteFile)は tools.go に残す。
package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shizukutanaka/yagura/internal/featurelist"
	"github.com/shizukutanaka/yagura/internal/initps1"
	"github.com/shizukutanaka/yagura/internal/initsh"
	"github.com/shizukutanaka/yagura/internal/plantracker"
	"github.com/shizukutanaka/yagura/internal/progressfile"
)

// ─── v0.33.0: closing the loop — disk write + hook query ─────────

// buildHookTimelineTool exposes Claude Code hook events via MCP query.
//
// Without this tool, hook data sat in JSONL on disk but couldn't be inspected
// from within an agent session. With it, an agent can ask "what tools have I
// been using in the last hour?" before deciding what to do next.
func buildHookTimelineTool(srv *Server) *Tool {
	return &Tool{
		Name:        "yagura_hook_timeline",
		Description: "[S] Recent Claude Code hook events for a project. Use to see what tools agents have invoked recently.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":       map[string]any{"type": "string", "description": "Project slug. Empty = all projects."},
				"hours":      map[string]any{"type": "integer", "description": "Look-back window. Default 24."},
				"event_type": map[string]any{"type": "string", "description": "Filter by hook_event_name (PreToolUse, PostToolUse, Stop, …)."},
				"limit":      map[string]any{"type": "integer", "description": "Max events returned. Default 100, capped at 500."},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug      string `json:"slug"`
				Hours     int    `json:"hours"`
				EventType string `json:"event_type"`
				Limit     int    `json:"limit"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, &ToolError{Code: "invalid_input", Cause: err}
				}
			}
			hr := srv.HookReceiver()
			if hr == nil {
				return nil, &ToolError{Code: "unavailable", Message: "hook receiver not configured"}
			}
			if in.Hours <= 0 {
				in.Hours = 24
			}
			if in.Limit <= 0 {
				in.Limit = 100
			}
			if in.Limit > 500 {
				in.Limit = 500
			}
			since := time.Now().Add(-time.Duration(in.Hours) * time.Hour)
			events := hr.Timeline(in.Slug, since, in.EventType, in.Limit)
			return map[string]any{
				"slug":       in.Slug,
				"hours":      in.Hours,
				"event_type": in.EventType,
				"count":      len(events),
				"events":     events,
			}, nil
		},
	}
}

// buildHookStatsTool surfaces aggregate hook counters from MCP.
//
// Complements yagura_hook_timeline by giving the agent the macro view
// (per-event counts, error totals, top tools) without enumerating every
// event.
func buildHookStatsTool(srv *Server) *Tool {
	return &Tool{
		Name:        "yagura_hook_stats",
		Description: "[S] Aggregate Claude Code hook stats per project (event counts, errors, top tools).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":  map[string]any{"type": "string", "description": "Project slug. Empty = all projects."},
				"top_n": map[string]any{"type": "integer", "description": "Top-N tools. Default 10."},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug string `json:"slug"`
				TopN int    `json:"top_n"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, &ToolError{Code: "invalid_input", Cause: err}
				}
			}
			hr := srv.HookReceiver()
			if hr == nil {
				return nil, &ToolError{Code: "unavailable", Message: "hook receiver not configured"}
			}
			if in.TopN <= 0 {
				in.TopN = 10
			}
			if in.Slug != "" {
				st := hr.ProjectStats(in.Slug)
				return map[string]any{
					"slug":      in.Slug,
					"stats":     st,
					"top_tools": hr.TopTools(in.Slug, in.TopN),
				}, nil
			}
			return map[string]any{
				"all_projects": hr.AllStats(),
				"top_tools":    hr.TopTools("", in.TopN),
			}, nil
		},
	}
}

// buildProgressFileTool generates claude-progress.txt for a project.
//
// Pulls feature_list + plantracker + hookreceiver + alertfix state through
// the existing snapshot helpers, then renders via progressfile.Generate.
func buildProgressFileTool(d Deps, srv *Server) *Tool {
	return &Tool{
		Name:        "yagura_progress_file",
		Description: "[G] Generate claude-progress.txt for cross-session handoff (Anthropic 2-agent harness pattern).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":  map[string]any{"type": "string"},
				"note":  map[string]any{"type": "string", "description": "Optional free-form intent / state."},
				"write": map[string]any{"type": "boolean", "description": "Also write to {local_path}/claude-progress.txt"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug  string `json:"slug"`
				Note  string `json:"note"`
				Write bool   `json:"write"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug required"}
			}
			p, err := d.Registry.Get(in.Slug)
			if err != nil {
				return nil, &ToolError{Code: "not_found", Message: "project not registered"}
			}
			snap := progressfile.Snapshot{
				Project:     in.Slug,
				GeneratedBy: "yagura " + version(),
				Note:        in.Note,
			}
			// Plan.md → feature list & progress
			if p.LocalPath != "" {
				if content, _, err := loadPlanMd(p.LocalPath); err == nil {
					state := plantracker.Parse(content)
					snap.PlanProgressPct = state.ProgressPct
					snap.CurrentPhase = state.CurrentPhase
					pin := planStateToFeatureInput(in.Slug, content, state)
					fl := featurelist.Build(pin, nil)
					snap.TotalFeatures = fl.Stats.Total
					snap.DoneFeatures = fl.Stats.Done
					for _, f := range fl.Features {
						if f.Status == "pending" {
							snap.PendingFeatures = append(snap.PendingFeatures, f.Title)
						}
					}
				}
			}
			// Hook activity
			if hr := srv.HookReceiver(); hr != nil {
				st := hr.ProjectStats(in.Slug)
				snap.HookSessions = st.ByEvent["Stop"] + st.ByEvent["SubagentStop"]
				snap.ToolErrorCount = st.ErrorCount
				for _, tu := range hr.TopTools(in.Slug, 5) {
					snap.TopTools = append(snap.TopTools, progressfile.ToolUse{
						Tool: tu.Tool, Count: tu.Count,
					})
				}
			}
			// Active alerts
			if store := srv.AlertStore(); store != nil {
				for _, st := range store.Snapshot() {
					if string(st.Status) == "active" {
						snap.ActiveAlerts = append(snap.ActiveAlerts, progressfile.Alert{
							ID: st.AlertID, Severity: "high", Source: "yagura",
							Summary: st.AlertID,
						})
					}
				}
			}
			body := progressfile.Generate(snap)
			result := map[string]any{
				"slug":     in.Slug,
				"body":     body,
				"length":   len(body),
				"filename": "claude-progress.txt",
			}
			if in.Write && p.LocalPath != "" {
				path := filepath.Join(p.LocalPath, "claude-progress.txt")
				if err := atomicWriteFile(path, []byte(body), 0o644); err != nil {
					return nil, &ToolError{Code: "write_failed", Message: err.Error()}
				}
				result["written_to"] = path
			}
			return result, nil
		},
	}
}

// buildInitShTool generates an init script for a project.
//
// v0.34.0: target parameter for POSIX sh ("posix", default) or PowerShell
// ("powershell" / "windows"). Both share the same BootSpec via cross-package
// duplication so output stays format-appropriate per OS.
func buildInitShTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_init_sh",
		Description: "[G] Generate init script (sh or PowerShell) for long-running agent sessions (Anthropic 2-agent harness).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":   map[string]any{"type": "string"},
				"target": map[string]any{"type": "string", "description": "'posix' (default, init.sh) or 'powershell'/'windows' (init.ps1)."},
				"write":  map[string]any{"type": "boolean", "description": "Also write to {local_path}/init.{sh,ps1}."},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug   string `json:"slug"`
				Target string `json:"target"`
				Write  bool   `json:"write"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug required"}
			}
			p, err := d.Registry.Get(in.Slug)
			if err != nil {
				return nil, &ToolError{Code: "not_found", Message: "project not registered"}
			}
			// Build a shared spec — language-specific lists are derived once,
			// then handed to the format-specific generator.
			tools := []string{"git"}
			var files []string
			switch strings.ToLower(p.Language) {
			case "go", "golang":
				tools = append(tools, "go", "make")
				files = []string{"go.mod"}
			case "node", "nodejs", "javascript", "typescript":
				tools = append(tools, "node", "npm")
				files = []string{"package.json"}
			case "python":
				tools = append(tools, "python3")
			case "rust":
				tools = append(tools, "cargo")
				files = []string{"Cargo.toml"}
			}

			target := strings.ToLower(strings.TrimSpace(in.Target))
			var body, filename string
			var mode os.FileMode
			switch target {
			case "powershell", "ps1", "windows", "win":
				spec := initps1.BootSpec{
					Project:       in.Slug,
					GeneratedBy:   "yagura " + version(),
					WorkDir:       p.LocalPath,
					Language:      p.Language,
					RequiredTools: tools,
					RequiredFiles: files,
					HandoffFiles:  []string{"claude-progress.txt", "AGENTS.md"},
				}
				body = initps1.Generate(spec)
				filename = "init.ps1"
				mode = 0o644 // PS scripts don't need +x; ExecutionPolicy gates execution
			case "", "posix", "sh", "bash", "unix", "linux", "macos", "darwin":
				spec := initsh.BootSpec{
					Project:       in.Slug,
					GeneratedBy:   "yagura " + version(),
					WorkDir:       p.LocalPath,
					Language:      p.Language,
					RequiredTools: tools,
					RequiredFiles: files,
					HandoffFiles:  []string{"claude-progress.txt", "AGENTS.md"},
				}
				body = initsh.Generate(spec)
				filename = "init.sh"
				mode = 0o755
			default:
				return nil, &ToolError{
					Code:    "invalid_input",
					Message: "unknown target: " + in.Target + " (use 'posix' or 'powershell')",
				}
			}

			result := map[string]any{
				"slug":     in.Slug,
				"target":   target,
				"body":     body,
				"length":   len(body),
				"filename": filename,
			}
			if in.Write && p.LocalPath != "" {
				path := filepath.Join(p.LocalPath, filename)
				if err := atomicWriteFile(path, []byte(body), mode); err != nil {
					return nil, &ToolError{Code: "write_failed", Message: err.Error()}
				}
				result["written_to"] = path
			}
			return result, nil
		},
	}
}
