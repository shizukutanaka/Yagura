// Package progressfile generates claude-progress.txt — the cross-session handoff
// artifact from Anthropic's 2-agent long-running harness pattern.
//
// Motivation (v0.33.0):
//
//	Anthropic "Effective harnesses for long-running agents" (2026) describes
//	the coding agent writing a claude-progress.txt at session end so that the
//	next session (with no shared memory) can resume from the right place.
//
//	Quote: "The key insight here was finding a way for agents to quickly
//	understand the state of work when starting with a fresh context window,
//	which is accomplished with the claude-progress.txt file alongside the git
//	history."
//
//	yagura already knows: registry facts, Plan.md state, feature_list status,
//	recent hook events, recent alerts. The missing piece was synthesizing
//	these into one human + agent readable handoff file.
//
// Design (ADR-0001 zero-dep):
//   - Pure function: Snapshot → string
//   - Deterministic (sort + stable wording)
//   - Reads like a shift-change note, not a dashboard dump
//   - Top 5 most useful facts only — the agent will read this first, so keep
//     it lean (Fowler: "give the agent a map, not a manual")
//
// Reference: https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents
package progressfile

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Snapshot is the structured input — caller assembles this from registry +
// plantracker + featurelist + hookreceiver + alertfix in mcp/tools.go.
type Snapshot struct {
	Project     string
	GeneratedAt time.Time
	GeneratedBy string // "yagura vX.Y.Z"

	// Feature list state (from yagura_feature_list)
	TotalFeatures   int
	DoneFeatures    int
	PendingFeatures []string // titles of next 5 pending features

	// Plan.md progress (from yagura_plan_status)
	PlanProgressPct int
	CurrentPhase    string

	// Recent hook activity (from yagura_hook_stats, last N sessions)
	HookSessions   int
	ToolErrorCount int
	TopTools       []ToolUse

	// Active alerts (from yagura_alert_fix)
	ActiveAlerts []Alert

	// Optional free-form note from caller (e.g. session intent)
	Note string
}

// ToolUse is one row in the "tools used most" mini-table.
type ToolUse struct {
	Tool  string
	Count int
}

// Alert is one row in the "active alerts" mini-table.
type Alert struct {
	ID       string
	Severity string
	Source   string
	Summary  string
}

// Generate renders the full claude-progress.txt body as plain text.
//
// Output sections (in order):
//  1. Header — project, when, by whom
//  2. Where you are — % done, current phase
//  3. What's next — top 5 pending features
//  4. Recent activity — sessions, errors, top tools
//  5. Active alerts — if any
//  6. Note — caller-supplied intent
//  7. Footer — how to use this file
//
// Empty sections are omitted (no filler) to keep the file scannable.
func Generate(s Snapshot) string {
	var sb strings.Builder
	writeHeader(&sb, s)
	writeProgress(&sb, s)
	writeNext(&sb, s)
	writeActivity(&sb, s)
	writeAlerts(&sb, s)
	writeNote(&sb, s)
	writeFooter(&sb)
	return sb.String()
}

func writeHeader(sb *strings.Builder, s Snapshot) {
	when := s.GeneratedAt
	if when.IsZero() {
		when = time.Now().UTC()
	}
	by := s.GeneratedBy
	if by == "" {
		by = "yagura"
	}
	proj := s.Project
	if proj == "" {
		proj = "(unknown project)"
	}
	fmt.Fprintf(sb, "claude-progress.txt — %s\n", proj)
	fmt.Fprintf(sb, "Generated at %s by %s.\n", when.UTC().Format(time.RFC3339), by)
	sb.WriteString("\n")
	sb.WriteString("This is a shift-change note. If you are an agent picking up where the previous\n")
	sb.WriteString("session left off, read this top-to-bottom before doing anything else.\n\n")
}

func writeProgress(sb *strings.Builder, s Snapshot) {
	if s.TotalFeatures == 0 && s.PlanProgressPct == 0 && s.CurrentPhase == "" {
		return
	}
	sb.WriteString("## Where you are\n\n")
	if s.TotalFeatures > 0 {
		fmt.Fprintf(sb, "- Features: %d of %d done (%d%%)\n",
			s.DoneFeatures, s.TotalFeatures, percentSafe(s.DoneFeatures, s.TotalFeatures))
	}
	if s.PlanProgressPct > 0 {
		fmt.Fprintf(sb, "- Plan.md progress: %d%%\n", s.PlanProgressPct)
	}
	if s.CurrentPhase != "" {
		fmt.Fprintf(sb, "- Current phase: %s\n", s.CurrentPhase)
	}
	sb.WriteString("\n")
}

func writeNext(sb *strings.Builder, s Snapshot) {
	if len(s.PendingFeatures) == 0 {
		return
	}
	sb.WriteString("## What's next\n\n")
	sb.WriteString("Pick the topmost feature unless something else is clearly more urgent.\n\n")
	limit := 5
	if len(s.PendingFeatures) < limit {
		limit = len(s.PendingFeatures)
	}
	for i := 0; i < limit; i++ {
		fmt.Fprintf(sb, "%d. %s\n", i+1, s.PendingFeatures[i])
	}
	if len(s.PendingFeatures) > limit {
		fmt.Fprintf(sb, "\n(%d more pending — see yagura_feature_list for the full list.)\n",
			len(s.PendingFeatures)-limit)
	}
	sb.WriteString("\n")
}

func writeActivity(sb *strings.Builder, s Snapshot) {
	if s.HookSessions == 0 && s.ToolErrorCount == 0 && len(s.TopTools) == 0 {
		return
	}
	sb.WriteString("## Recent activity (this session and prior)\n\n")
	if s.HookSessions > 0 {
		fmt.Fprintf(sb, "- Hook sessions observed: %d\n", s.HookSessions)
	}
	if s.ToolErrorCount > 0 {
		fmt.Fprintf(sb, "- Tool errors: %d  (investigate before adding new work)\n", s.ToolErrorCount)
	}
	if len(s.TopTools) > 0 {
		// sort by count desc, tie-break alphabetical (deterministic)
		tools := append([]ToolUse{}, s.TopTools...)
		sort.Slice(tools, func(i, j int) bool {
			if tools[i].Count != tools[j].Count {
				return tools[i].Count > tools[j].Count
			}
			return tools[i].Tool < tools[j].Tool
		})
		sb.WriteString("- Tools used most:\n")
		limit := 5
		if len(tools) < limit {
			limit = len(tools)
		}
		for i := 0; i < limit; i++ {
			fmt.Fprintf(sb, "  - %s (%d)\n", tools[i].Tool, tools[i].Count)
		}
	}
	sb.WriteString("\n")
}

func writeAlerts(sb *strings.Builder, s Snapshot) {
	if len(s.ActiveAlerts) == 0 {
		return
	}
	sb.WriteString("## Active alerts (resolve before declaring complete)\n\n")
	// Sort by severity descending (CRITICAL > HIGH > MEDIUM > LOW > INFO), then by ID
	alerts := append([]Alert{}, s.ActiveAlerts...)
	sort.Slice(alerts, func(i, j int) bool {
		ri := severityRank(alerts[i].Severity)
		rj := severityRank(alerts[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return alerts[i].ID < alerts[j].ID
	})
	for _, a := range alerts {
		fmt.Fprintf(sb, "- [%s] %s — %s  (id=%s, source=%s)\n",
			strings.ToUpper(a.Severity), a.Summary, "fix via yagura_alert_fix",
			a.ID, a.Source)
	}
	sb.WriteString("\n")
}

func writeNote(sb *strings.Builder, s Snapshot) {
	if strings.TrimSpace(s.Note) == "" {
		return
	}
	sb.WriteString("## Note from previous session\n\n")
	sb.WriteString(strings.TrimSpace(s.Note))
	sb.WriteString("\n\n")
}

func writeFooter(sb *strings.Builder) {
	sb.WriteString("---\n")
	sb.WriteString("How to use this file:\n")
	sb.WriteString("- This file is regenerated by `yagura_progress_file` on demand.\n")
	sb.WriteString("- Treat it as the most authoritative summary of cross-session state.\n")
	sb.WriteString("- If git history disagrees with this file, trust git history and ask.\n")
}

// severityRank gives a sort key: lower number = higher priority.
//
// Unknown severities sort after INFO.
func severityRank(sev string) int {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	case "info":
		return 4
	default:
		return 5
	}
}

// percentSafe avoids divide-by-zero for the rare case where total is 0.
func percentSafe(done, total int) int {
	if total <= 0 {
		return 0
	}
	return done * 100 / total
}
