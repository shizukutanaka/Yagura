// tools_inventory.go: extracted from tools.go in v0.29 (gstack-style topic split).
//
// All tools in this file are registered via RegisterDefaultTools in tools.go.
// See CLAUDE.md Workflows for the registration pattern.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/registry"
	"github.com/shizukutanaka/yagura/internal/today"
)

// listOut は yagura_list / yagura_search の 1 行分の出力(compact 表現)。
type listOut struct {
	Slug            string `json:"slug"`
	DisplayName     string `json:"display_name"`
	Stage           string `json:"stage"`
	Priority        int    `json:"priority"`
	Language        string `json:"language,omitempty"`
	OpenPRs         int    `json:"open_prs,omitempty"`
	OpenIssues      int    `json:"open_issues,omitempty"`
	CIStatus        string `json:"ci_status,omitempty"`
	LatestVersion   string `json:"latest_version,omitempty"`
	DaysSinceCommit int    `json:"days_since_commit"`
}

// todayItem は yagura_today のランキング 1 件分。スコアリングは internal/today に
// 抽出済み(CLI と共有)。type alias なので既存の出力 shape / テストはそのまま。
type todayItem = today.Item

func buildListTool(d Deps) *Tool {
	return &Tool{
		Name: "yagura_list",
		Description: "[G] List projects (compact). Optional limit caps rows.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer", "minimum": 1,
					"description": "最大返却件数(省略時は全件)。トークン節約用。"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			limit, err := parseLimitArg(args)
			if err != nil {
				return nil, err
			}
			return listResult(d.Registry.List(), d.Now(), limit), nil
		},
	}
}

// listResult は projects を listOut に変換した {count, projects[, total, truncated]}
// を返す共通ロジック。limit>0 のとき先頭 limit 件に切り詰め、元の総数を total /
// truncated で伝える(Headroom 流「必要なら追加取得」: agent は total を見て
// 件数指定で再取得できる)。list と search で共有。
func listResult(projects []*project.Project, now time.Time, limit int) map[string]any {
	total := len(projects)
	truncated := false
	if limit > 0 && total > limit {
		projects = projects[:limit]
		truncated = true
	}
	out := make([]listOut, 0, len(projects))
	for _, p := range projects {
		out = append(out, listOut{
			Slug:            p.Slug,
			DisplayName:     p.DisplayName,
			Stage:           string(p.Stage),
			Priority:        p.Priority,
			Language:        p.Language,
			OpenPRs:         p.OpenPRs,
			OpenIssues:      p.OpenIssues,
			CIStatus:        string(p.CIStatus),
			LatestVersion:   p.LatestVersion,
			DaysSinceCommit: project.DaysSince(p.LatestActivity, now),
		})
	}
	res := map[string]any{"count": len(out), "projects": out}
	if truncated {
		res["total"] = total
		res["truncated"] = true
	}
	return res
}

// parseLimitArg は args から任意の "limit" を読む。未指定/0/負値は 0(無制限)。
func parseLimitArg(args json.RawMessage) (int, error) {
	if len(args) == 0 {
		return 0, nil
	}
	var in struct {
		Limit int `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return 0, &ToolError{Code: "invalid_input", Message: "invalid args", Cause: err}
	}
	if in.Limit < 0 {
		return 0, nil
	}
	return in.Limit, nil
}

func buildGetTool(d Deps) *Tool {
	return &Tool{
		Name: "yagura_get",
		Description: "[G] Get project by slug.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug": map[string]any{"type": "string"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug string `json:"slug"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: "invalid args", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug is required"}
			}
			p, err := d.Registry.Get(in.Slug)
			if err != nil {
				if errors.Is(err, registry.ErrNotFound) {
					return nil, &ToolError{Code: "not_found", Message: "project not found: " + in.Slug}
				}
				return nil, &ToolError{Code: "internal", Message: "lookup failed", Cause: err}
			}
			return p, nil
		},
	}
}

func buildSearchTool(d Deps) *Tool {
	return &Tool{
		Name: "yagura_search",
		Description: "[G] Search projects: tag/lang/stage/text (AND).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tag":      map[string]any{"type": "string", "description": "tag 完全一致 (大文字小文字無視)"},
				"language": map[string]any{"type": "string", "description": "言語完全一致 (大文字小文字無視)"},
				"stage":    map[string]any{"type": "string", "description": "active / maintenance / paused / archived"},
				"query":    map[string]any{"type": "string", "description": "slug/name/notes/tags を横断する部分一致"},
				"limit":    map[string]any{"type": "integer", "minimum": 1, "description": "最大返却件数(省略時は全件)。"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Tag      string `json:"tag"`
				Language string `json:"language"`
				Stage    string `json:"stage"`
				Query    string `json:"query"`
				Limit    int    `json:"limit"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, &ToolError{Code: "invalid_input", Message: "invalid args", Cause: err}
				}
			}
			stage := project.Stage(strings.ToLower(strings.TrimSpace(in.Stage)))
			if in.Stage != "" {
				switch stage {
				case project.StageActive, project.StageMaintenance, project.StagePaused, project.StageArchived:
				default:
					return nil, &ToolError{Code: "invalid_input",
						Message: "stage must be one of active/maintenance/paused/archived"}
				}
			}

			filter := func(p *project.Project) bool {
				if in.Tag != "" && !p.HasTag(in.Tag) {
					return false
				}
				if in.Language != "" && !strings.EqualFold(p.Language, in.Language) {
					return false
				}
				if in.Stage != "" && p.Stage != stage {
					return false
				}
				if in.Query != "" && !p.MatchesQuery(in.Query) {
					return false
				}
				return true
			}
			limit := in.Limit
			if limit < 0 {
				limit = 0
			}
			return listResult(d.Registry.Filter(filter), d.Now(), limit), nil
		},
	}
}

func buildTodayTool(d Deps) *Tool {
	return &Tool{
		Name: "yagura_today",
		Description: "[G] Top projects today by score.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{
					"type": "integer", "minimum": 1, "maximum": 50,
				},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			limit := 5
			if len(args) > 0 {
				var in struct {
					Limit int `json:"limit"`
				}
				if err := json.Unmarshal(args, &in); err != nil {
					return nil, &ToolError{Code: "invalid_input", Message: "invalid args", Cause: err}
				}
				if in.Limit > 0 {
					if in.Limit > 50 {
						in.Limit = 50
					}
					limit = in.Limit
				}
			}

			now := d.Now()
			items := today.Rank(d.Registry.List(), now, limit)
			return map[string]any{
				"date":  now.Format("2006-01-02"),
				"count": len(items),
				"items": items,
			}, nil
		},
	}
}

func buildRegisterTool(d Deps) *Tool {
	return &Tool{
		Name: "yagura_register",
		Description: "[G] Register project.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":         map[string]any{"type": "string"},
				"display_name": map[string]any{"type": "string"},
				"repository":   map[string]any{"type": "string"},
				"language":     map[string]any{"type": "string"},
				"local_path":   map[string]any{"type": "string"},
				"tags":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"depends_on":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"stage":        map[string]any{"type": "string"},
				"priority":     map[string]any{"type": "integer", "minimum": 0, "maximum": 5},
				"notes":        map[string]any{"type": "string"},
			},
			"required": []string{"slug", "repository"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug        string   `json:"slug"`
				DisplayName string   `json:"display_name"`
				Repository  string   `json:"repository"`
				Language    string   `json:"language"`
				LocalPath   string   `json:"local_path"`
				Tags        []string `json:"tags"`
				DependsOn   []string `json:"depends_on"`
				Stage       string   `json:"stage"`
				Priority    int      `json:"priority"`
				Notes       string   `json:"notes"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: "invalid args", Cause: err}
			}
			displayName := in.DisplayName
			if displayName == "" {
				displayName = in.Slug
			}
			stage := project.Stage(strings.ToLower(strings.TrimSpace(in.Stage)))
			if stage == "" {
				stage = project.StageActive
			}
			p := &project.Project{
				Slug:        in.Slug,
				DisplayName: displayName,
				Repository:  in.Repository,
				Language:    in.Language,
				LocalPath:   in.LocalPath,
				Tags:        in.Tags,
				DependsOn:   in.DependsOn,
				Stage:       stage,
				Priority:    in.Priority,
				Notes:       in.Notes,
			}
			if err := d.Registry.Add(p); err != nil {
				if errors.Is(err, registry.ErrAlreadyExists) {
					return nil, &ToolError{Code: "invalid_input",
						Message: "project already exists: " + in.Slug}
				}
				return nil, &ToolError{Code: "invalid_input",
					Message: "register failed: " + err.Error(), Cause: err}
			}
			return map[string]any{
				"slug":    p.Slug,
				"created": true,
			}, nil
		},
	}
}

func buildUnregisterTool(d Deps) *Tool {
	return &Tool{
		Name: "yagura_unregister",
		Description: "[G] Unregister project (hard delete).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug": map[string]any{"type": "string"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug string `json:"slug"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: "invalid args", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug is required"}
			}
			if err := d.Registry.Delete(in.Slug); err != nil {
				if errors.Is(err, registry.ErrNotFound) {
					return nil, &ToolError{Code: "not_found",
						Message: "project not found: " + in.Slug}
				}
				return nil, &ToolError{Code: "internal",
					Message: "delete failed", Cause: err}
			}
			return map[string]any{
				"slug":    in.Slug,
				"deleted": true,
			}, nil
		},
	}
}

func buildUpdateTool(d Deps) *Tool {
	return &Tool{
		Name: "yagura_update",
		Description: "[G] Update project manual fields. Omit field = unchanged.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":         map[string]any{"type": "string"},
				"display_name": map[string]any{"type": "string"},
				"language":     map[string]any{"type": "string"},
				"local_path":   map[string]any{"type": "string"},
				"tags":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"depends_on":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"stage":        map[string]any{"type": "string"},
				"priority":     map[string]any{"type": "integer", "minimum": 0, "maximum": 5},
				"notes":        map[string]any{"type": "string"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			// Decode into pointer fields so we can distinguish "not provided"
			// from "provided as empty/zero". Each field's zero value means
			// "keep current".
			var in struct {
				Slug        string    `json:"slug"`
				DisplayName *string   `json:"display_name"`
				Language    *string   `json:"language"`
				LocalPath   *string   `json:"local_path"`
				Tags        *[]string `json:"tags"`
				DependsOn   *[]string `json:"depends_on"`
				Stage       *string   `json:"stage"`
				Priority    *int      `json:"priority"`
				Notes       *string   `json:"notes"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: "invalid args", Cause: err}
			}
			if in.Slug == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "slug is required"}
			}

			cur, err := d.Registry.Get(in.Slug)
			if err != nil {
				if errors.Is(err, registry.ErrNotFound) {
					return nil, &ToolError{Code: "not_found",
						Message: "project not found: " + in.Slug}
				}
				return nil, &ToolError{Code: "internal",
					Message: "lookup failed", Cause: err}
			}

			// Apply only provided fields
			if in.DisplayName != nil {
				cur.DisplayName = *in.DisplayName
			}
			if in.Language != nil {
				cur.Language = *in.Language
			}
			if in.LocalPath != nil {
				cur.LocalPath = *in.LocalPath
			}
			if in.Tags != nil {
				cur.Tags = *in.Tags
			}
			if in.DependsOn != nil {
				cur.DependsOn = *in.DependsOn
			}
			if in.Stage != nil {
				stage := project.Stage(strings.ToLower(strings.TrimSpace(*in.Stage)))
				switch stage {
				case project.StageActive, project.StageMaintenance,
					project.StagePaused, project.StageArchived:
					cur.Stage = stage
				default:
					return nil, &ToolError{Code: "invalid_input",
						Message: "stage must be one of active/maintenance/paused/archived"}
				}
			}
			if in.Priority != nil {
				if *in.Priority < 0 || *in.Priority > 5 {
					return nil, &ToolError{Code: "invalid_input",
						Message: "priority must be 0-5"}
				}
				cur.Priority = *in.Priority
			}
			if in.Notes != nil {
				cur.Notes = *in.Notes
			}

			if err := d.Registry.Update(cur); err != nil {
				return nil, &ToolError{Code: "internal",
					Message: "update failed", Cause: err}
			}
			return map[string]any{
				"slug":    in.Slug,
				"updated": true,
			}, nil
		},
	}
}

func buildStatsTool(d Deps) *Tool {
	return &Tool{
		Name: "yagura_stats",
		Description: "[G] Portfolio counts/totals/averages.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			projects := d.Registry.List()
			now := d.Now()

			byStage := map[string]int{}
			byCI := map[string]int{}
			byLanguage := map[string]int{}
			var totalPRs, totalIssues, staleCount, withSprint int
			var prioritySum, priorityN int

			for _, p := range projects {
				byStage[string(p.Stage)]++
				ciKey := string(p.CIStatus)
				if ciKey == "" {
					ciKey = "unknown"
				}
				byCI[ciKey]++
				if p.Language != "" {
					byLanguage[p.Language]++
				}
				totalPRs += p.OpenPRs
				totalIssues += p.OpenIssues
				if p.Sprint != nil {
					withSprint++
				}
				if p.Priority > 0 {
					prioritySum += p.Priority
					priorityN++
				}
				if p.Stage == project.StageActive && !p.LatestActivity.IsZero() &&
					int(now.Sub(p.LatestActivity).Hours()/24) >= 14 {
					staleCount++
				}
			}

			var avgPriority float64
			if priorityN > 0 {
				avgPriority = float64(prioritySum) / float64(priorityN)
			}

			return map[string]any{
				"total":              len(projects),
				"by_stage":           byStage,
				"by_ci_status":       byCI,
				"by_language":        byLanguage,
				"total_open_prs":     totalPRs,
				"total_open_issues":  totalIssues,
				"stale_active_count": staleCount,
				"with_active_sprint": withSprint,
				"avg_priority":       avgPriority,
				"as_of":              now.Format(time.RFC3339),
			}, nil
		},
	}
}
