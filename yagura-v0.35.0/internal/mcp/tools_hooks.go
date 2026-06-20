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
			if p.LocalPath != "" {
				addProgressPlanData(&snap, in.Slug, p.LocalPath)
			}
			addProgressHookData(&snap, srv, in.Slug)
			addProgressAlertData(&snap, srv)
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

// addProgressPlanData は Plan.md から progress / feature 情報を snapshot に加える。
func addProgressPlanData(snap *progressfile.Snapshot, slug, localPath string) {
	content, _, err := loadPlanMd(localPath)
	if err != nil {
		return
	}
	state := plantracker.Parse(content)
	snap.PlanProgressPct = state.ProgressPct
	snap.CurrentPhase = state.CurrentPhase
	pin := planStateToFeatureInput(slug, content, state)
	fl := featurelist.Build(pin, nil)
	snap.TotalFeatures = fl.Stats.Total
	snap.DoneFeatures = fl.Stats.Done
	for _, f := range fl.Features {
		if f.Status == "pending" {
			snap.PendingFeatures = append(snap.PendingFeatures, f.Title)
		}
	}
}

// addProgressHookData は hook receiver の集計(sessions / errors / top tools)を加える。
func addProgressHookData(snap *progressfile.Snapshot, srv *Server, slug string) {
	hr := srv.HookReceiver()
	if hr == nil {
		return
	}
	st := hr.ProjectStats(slug)
	snap.HookSessions = st.ByEvent["Stop"] + st.ByEvent["SubagentStop"]
	snap.ToolErrorCount = st.ErrorCount
	for _, tu := range hr.TopTools(slug, 5) {
		snap.TopTools = append(snap.TopTools, progressfile.ToolUse{Tool: tu.Tool, Count: tu.Count})
	}
}

// addProgressAlertData は active な lifecycle alert を snapshot に加える。
func addProgressAlertData(snap *progressfile.Snapshot, srv *Server) {
	store := srv.AlertStore()
	if store == nil {
		return
	}
	for _, st := range store.Snapshot() {
		if string(st.Status) == "active" {
			snap.ActiveAlerts = append(snap.ActiveAlerts, progressfile.Alert{
				ID: st.AlertID, Severity: "high", Source: "yagura", Summary: st.AlertID,
			})
		}
	}
}

// initScriptToolsFiles は language から必須 tool / file リストを導出する。
func initScriptToolsFiles(language string) (tools, files []string) {
	tools = []string{"git"}
	switch strings.ToLower(language) {
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
	return tools, files
}

// generateInitScript は target("posix" / "powershell")に応じた init script を
// 生成し、body / filename / file-mode を返す。未知 target は *ToolError。
// initScriptParams は init script 生成の project 由来パラメータ群。
// (引数列の肥大を避けるため struct に束ねる — paramcheck dogfood)
type initScriptParams struct {
	slug, workDir, language string
	tools, files            []string
}

// initScriptResult bundles the three output fields of generateInitScript.
// (returncheck dogfood: was 4 returns; struct collapses to 2)
type initScriptResult struct {
	Body     string
	Filename string
	Mode     os.FileMode
}

func generateInitScript(target string, p initScriptParams) (initScriptResult, *ToolError) {
	handoff := []string{"claude-progress.txt", "AGENTS.md"}
	switch target {
	case "powershell", "ps1", "windows", "win":
		body := initps1.Generate(initps1.BootSpec{
			Project: p.slug, GeneratedBy: "yagura " + version(), WorkDir: p.workDir,
			Language: p.language, RequiredTools: p.tools, RequiredFiles: p.files, HandoffFiles: handoff,
		})
		// PS scripts don't need +x; ExecutionPolicy gates execution.
		return initScriptResult{Body: body, Filename: "init.ps1", Mode: 0o644}, nil
	case "", "posix", "sh", "bash", "unix", "linux", "macos", "darwin":
		body := initsh.Generate(initsh.BootSpec{
			Project: p.slug, GeneratedBy: "yagura " + version(), WorkDir: p.workDir,
			Language: p.language, RequiredTools: p.tools, RequiredFiles: p.files, HandoffFiles: handoff,
		})
		return initScriptResult{Body: body, Filename: "init.sh", Mode: 0o755}, nil
	default:
		return initScriptResult{}, &ToolError{Code: "invalid_input",
			Message: "unknown target: " + target + " (use 'posix' or 'powershell')"}
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
			tools, files := initScriptToolsFiles(p.Language)

			target := strings.ToLower(strings.TrimSpace(in.Target))
			scr, terr := generateInitScript(target, initScriptParams{
				slug: in.Slug, workDir: p.LocalPath, language: p.Language, tools: tools, files: files,
			})
			if terr != nil {
				return nil, terr
			}

			result := map[string]any{
				"slug":     in.Slug,
				"target":   target,
				"body":     scr.Body,
				"length":   len(scr.Body),
				"filename": scr.Filename,
			}
			if in.Write && p.LocalPath != "" {
				path := filepath.Join(p.LocalPath, scr.Filename)
				if err := atomicWriteFile(path, []byte(scr.Body), scr.Mode); err != nil {
					return nil, &ToolError{Code: "write_failed", Message: err.Error()}
				}
				result["written_to"] = path
			}
			return result, nil
		},
	}
}
