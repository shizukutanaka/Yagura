// tools_alerts.go: alert lifecycle 系 MCP tool(v0.27 alert_fix + v0.30 alert_resolve)。
//
// yagura_alert_fix(6 source × 4 severity の rule-based recommendation hub)と
// yagura_alert_resolve(resolve / snooze / reopen)、および snapshot 変換
// helper(ProjectToSnapshot / filterBySeverity)。tools.go の topic 別分割
// (Roadmap #1)の一環。登録順は tools.go の RegisterDefaultTools のまま不変。
package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/plantracker"
	"github.com/shizukutanaka/yagura/internal/project"
)

// ─── yagura_alert_fix (v0.27.0) ──────────────────────────────
//
// cortex flywheel ④ Alert-Fix の yagura 実装。
// portfolio 全体または特定 project の sensor data を集約し、
// actionable な next-action を rule-based に提案する hub。

func buildAlertFixTool(d Deps, cache plantracker.CacheLike, store *alertfix.Store) *Tool {
	return &Tool{
		Name:        "yagura_alert_fix",
		Description: "[S] Aggregate health signals across portfolio. Returns actionable alerts with suggested_tool + args.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":             map[string]any{"type": "string"},
				"severity_min":     map[string]any{"type": "string"},
				"stale_days":       map[string]any{"type": "integer"},
				"scorecard_min":    map[string]any{"type": "number"},
				"open_issues_high": map[string]any{"type": "integer"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug            string  `json:"slug"`
				SeverityMin     string  `json:"severity_min"`
				StaleDays       int     `json:"stale_days"`
				ScorecardMin    float64 `json:"scorecard_min"`
				OpenIssuesHigh  int     `json:"open_issues_high"`
				IncludeInactive bool    `json:"include_inactive"` // v0.30: resolved/snoozed も含める
			}
			json.Unmarshal(args, &in)

			th := alertFixThresholds(in.StaleDays, in.ScorecardMin, in.OpenIssuesHigh)

			// snapshot 抽出
			var projects []*project.Project
			if in.Slug != "" {
				p, err := d.Registry.Get(in.Slug)
				if err != nil {
					return nil, &ToolError{Code: "not_found", Message: "project not registered"}
				}
				projects = []*project.Project{p}
			} else {
				projects = d.Registry.List()
			}

			snaps := buildAlertSnapshots(projects, cache)
			report := alertfix.EvaluateAll(snaps, th)

			// severity_min filter
			if in.SeverityMin != "" {
				report = filterBySeverity(report, in.SeverityMin)
			}

			// v0.30.0: lifecycle filter — resolved / snoozed を除外
			// `include_inactive=true` の場合は filter を skip(audit / debug 用途)
			filteredOut := 0
			if store != nil && !in.IncludeInactive {
				before := len(report.Alerts)
				report = store.FilterReport(report)
				filteredOut = before - len(report.Alerts)
			}

			out := map[string]any{
				"alerts":           report.Alerts,
				"total":            report.Total,
				"by_severity":      report.BySeverity,
				"by_source":        report.BySource,
				"by_project":       report.ByProject,
				"projects_scanned": report.ProjectsScanned,
				"has_critical":     report.HasCritical,
				"summary":          report.Summary(),
				"generated_at":     report.GeneratedAt.Format(time.RFC3339),
			}
			if filteredOut > 0 {
				out["filtered_inactive"] = filteredOut
			}
			if store != nil {
				out["lifecycle_stats"] = store.Stats()
			}
			return out, nil
		},
	}
}

// alertFixThresholds は default 閾値に正の override 値のみを適用して返す。
func alertFixThresholds(staleDays int, scorecardMin float64, openIssuesHigh int) alertfix.Thresholds {
	th := alertfix.DefaultThresholds()
	if staleDays > 0 {
		th.StaleDays = staleDays
	}
	if scorecardMin > 0 {
		th.ScorecardMin = scorecardMin
	}
	if openIssuesHigh > 0 {
		th.OpenIssuesHigh = openIssuesHigh
	}
	return th
}

// buildAlertSnapshots は project 群を alertfix snapshot へ変換し、Plan.md があれば
// その health 情報(healthy/progress/issues)を各 snapshot に加える。
func buildAlertSnapshots(projects []*project.Project, cache plantracker.CacheLike) []alertfix.ProjectSnapshot {
	snaps := make([]alertfix.ProjectSnapshot, 0, len(projects))
	for _, p := range projects {
		snap := projectToSnapshot(*p)
		if p.LocalPath != "" {
			if content, _, err := loadPlanMd(p.LocalPath); err == nil {
				snap.HasPlanMd = true
				state, _ := plantracker.ParseCached(content, cache)
				snap.PlanIsHealthy = state.IsHealthy
				snap.PlanProgressPct = state.ProgressPct
				snap.PlanIssues = state.Issues
			}
		}
		snaps = append(snaps, snap)
	}
	return snaps
}

// projectToSnapshot は registry.Project から alertfix snapshot に必要な field を抽出する。
// ProjectToSnapshot maps a registry Project to an alertfix sensor snapshot.
// Exported so the daemon's periodic health sweep (scanner AfterScan hook) reuses
// the exact same field extraction as yagura_alert_fix (single source of truth).
func ProjectToSnapshot(p project.Project) alertfix.ProjectSnapshot {
	return projectToSnapshot(p)
}

func projectToSnapshot(p project.Project) alertfix.ProjectSnapshot {
	return alertfix.ProjectSnapshot{
		Slug:           p.Slug,
		Repository:     p.Repository,
		VulnCritical:   p.VulnCritical,
		VulnHigh:       p.VulnHigh,
		CIStatus:       string(p.CIStatus),
		ScorecardScore: p.ScorecardScore,
		OpenIssues:     p.OpenIssues,
		OpenPRs:        p.OpenPRs,
		LatestActivity: p.LatestActivity,
		RepoPublic:     p.RepoPublic,
		Tags:           p.Tags,
	}
}

// filterBySeverity は severity_min 以上のみ残す。
//
// "critical" → critical only / "high" → critical+high / "medium" → critical+high+medium 等。
func filterBySeverity(r alertfix.Report, min string) alertfix.Report {
	rank := map[string]int{
		"critical": 0, "high": 1, "medium": 2, "low": 3,
	}
	maxRank, ok := rank[strings.ToLower(min)]
	if !ok {
		return r
	}
	var kept []alertfix.Alert
	for _, a := range r.Alerts {
		if rank[string(a.Severity)] <= maxRank {
			kept = append(kept, a)
		}
	}
	r.Alerts = kept
	r.Total = len(kept)
	return r
}

// ─── yagura_alert_resolve (v0.30.0) ─────────────────────────────
//
// Alert lifecycle 管理。resolve / snooze / reopen の 3 action。
// JSONL に persist されるので daemon restart でも state は保持される。

func buildAlertResolveTool(store *alertfix.Store) *Tool {
	return &Tool{
		Name:        "yagura_alert_resolve",
		Description: "[G] Manage alert lifecycle: resolve/snooze/reopen. Persists to {state_dir}/alert_state.jsonl.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"alert_id":    map[string]any{"type": "string"},
				"action":      map[string]any{"type": "string", "enum": []string{"resolve", "snooze", "reopen"}},
				"note":        map[string]any{"type": "string"},
				"snooze_days": map[string]any{"type": "integer"}, // snooze 期間(日)
			},
			"required": []string{"alert_id", "action"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				AlertID    string `json:"alert_id"`
				Action     string `json:"action"`
				Note       string `json:"note"`
				SnoozeDays int    `json:"snooze_days"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if in.AlertID == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "alert_id required"}
			}
			if store == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "alert store not configured (start daemon with YAGURA_STATE_DIR)"}
			}
			var err error
			switch in.Action {
			case "resolve":
				err = store.Resolve(in.AlertID, in.Note)
			case "snooze":
				if in.SnoozeDays <= 0 {
					in.SnoozeDays = 7 // default 1 week
				}
				until := time.Now().Add(time.Duration(in.SnoozeDays) * 24 * time.Hour)
				err = store.Snooze(in.AlertID, until, in.Note)
			case "reopen":
				err = store.Reopen(in.AlertID, in.Note)
			default:
				return nil, &ToolError{Code: "invalid_input",
					Message: "action must be resolve/snooze/reopen"}
			}
			if err != nil {
				return nil, &ToolError{Code: "internal", Cause: err}
			}
			st, _ := store.Get(in.AlertID)
			return map[string]any{
				"alert_id":        in.AlertID,
				"action":          in.Action,
				"current_state":   st,
				"lifecycle_stats": store.Stats(),
			}, nil
		},
	}
}

