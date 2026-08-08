// Package alertfix は portfolio 全体の health signal を集約し、
// rule-based な next-action recommendation を生成する。
//
// 動機 (v0.27.0):
//   cortex (aircloset 2026/05) の 4 flywheel:
//     ① Code (生成)       — Claude Code/Windsurf が担当
//     ② Review (検証)     — yagura ai_verify + test_audit (v0.25-26)
//     ③ Release (公開)    — yagura release_radar (v0.24)
//     ④ Alert-Fix (再投入) — yagura alertfix (v0.27) ★
//
//   yagura は既に sensor data (VulnCritical, CIStatus, ScorecardScore, Plan.md)
//   を持つが、これらを actionable な next-action として agent に提示する hub が
//   欠けていた。本 package がそのハブ。
//
//   重要原則 (m's harness G0.7 と整合): LLM を呼ばず rule-based。これにより
//   determinism + reproducibility + zero-dep ADR-0001 を維持しつつ、agent が
//   即実行可能な suggested_tool + args を出す。
//
// 設計判断 (ADR-0001 ゼロ依存):
//   - struct + 純関数
//   - sensor data は外部から injection (Project, PlanState 等)
//   - recommendation は固定 template + 動的 args 生成
//   - severity threshold は const で調整可能
//
// 性能:
//   - O(N) projects scan
//   - 23 projects で <1ms 実測予測
package alertfix

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Severity は alert の重大度。
type Severity string

const (
	// SevCritical は即時対応が必要な最高重大度の alert。
	SevCritical Severity = "critical"
	// SevHigh は優先対応が推奨される高重大度の alert。
	SevHigh Severity = "high"
	// SevMedium は計画的対応が推奨される中重大度の alert。
	SevMedium Severity = "medium"
	// SevLow は任意対応の低重大度の alert。
	SevLow Severity = "low"
)

// Source は alert がどの sensor / guide から発火したか。
type Source string

const (
	// SourceVuln は既知脆弱性(OSV/CVE)から発火した alert の source。
	SourceVuln Source = "vulns"
	// SourcePlan は Plan.md の進捗停滞から発火した alert の source。
	SourcePlan Source = "plan"
	// SourceCI は CI ステータス失敗から発火した alert の source。
	SourceCI Source = "ci"
	// SourceStale は長期未活動(staleness)から発火した alert の source。
	SourceStale Source = "stale"
	// SourceScorecard は OpenSSF Scorecard スコア低下から発火した alert の source。
	SourceScorecard Source = "scorecard"
	// SourceOpenIssues は未解決 Issue 数超過から発火した alert の source。
	SourceOpenIssues Source = "open_issues"
	// SourceVisibility はリポジトリ公開設定の問題から発火した alert の source。
	SourceVisibility Source = "visibility"
)

// Alert は 1 つの actionable issue。
type Alert struct {
	ID             string         `json:"id"` // 安定 ID (project + source + signature)
	Project        string         `json:"project"`
	Source         Source         `json:"source"`
	Severity       Severity       `json:"severity"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	Recommendation string         `json:"recommendation"`
	SuggestedTool  string         `json:"suggested_tool,omitempty"`
	SuggestedArgs  map[string]any `json:"suggested_args,omitempty"`
	DetectedAt     time.Time      `json:"detected_at,omitempty"`
	// 数値 metric を flat に置く(LLM が判断材料にする)
	MetricInt    int    `json:"metric_int,omitempty"`
	MetricFloat  float64 `json:"metric_float,omitempty"`
}

// Report は alert 集計。
type Report struct {
	Alerts       []Alert            `json:"alerts"`
	Total        int                `json:"total"`
	BySeverity   map[Severity]int   `json:"by_severity"`
	BySource     map[Source]int     `json:"by_source"`
	ByProject    map[string]int     `json:"by_project,omitempty"`
	ProjectsScanned int             `json:"projects_scanned"`
	GeneratedAt  time.Time          `json:"generated_at"`
	HasCritical  bool               `json:"has_critical"`
}

// ProjectSnapshot は alertfix への入力 (registry.Project から抽出した sensor 値)。
//
// registry を直接 import しないことで test しやすく + 循環 import 回避。
type ProjectSnapshot struct {
	Slug              string
	Repository        string
	VulnCritical      int
	VulnHigh          int
	CIStatus          string  // "passing"/"failing"/"unknown"
	ScorecardScore    float64 // 0.0-10.0
	OpenIssues        int
	OpenPRs           int
	LatestActivity    time.Time
	// optional: plantracker 等から渡せる
	PlanIsHealthy     bool
	PlanProgressPct   int
	PlanIssues        []string
	HasPlanMd         bool
	// visibility literacy: RepoPublic は scanner が観測した実際の公開状態
	// (sensor、MCP からは詐称不可)。Tags は人間が宣言した分類(manual metadata)。
	// 両者の不一致(sensitivity tag つきなのに Public)を Evaluate が alert する。
	RepoPublic        bool
	Tags              []string
}

// sensitivityTags は「本来 private であるべき」ことを示す宣言タグ群。
// これらの tag が付いた project の repo が Public だと visibility mismatch。
var sensitivityTags = map[string]bool{
	"internal":     true,
	"confidential": true,
	"private":      true,
	"secret":       true,
}

// findSensitivityTag は tags から最初の sensitivity tag を探し、(値, 見つかったか)を
// 返す(case-insensitive)。`v, ok` lookup イディオムに従い値を先に返すため、純粋な
// bool 述語ではない——名前も `has`(bool を約束)ではなく `find`(値を返す)とする。
func findSensitivityTag(tags []string) (string, bool) {
	for _, t := range tags {
		if sensitivityTags[strings.ToLower(strings.TrimSpace(t))] {
			return t, true
		}
	}
	return "", false
}

// Thresholds は alert 発火条件(調整可能)。
type Thresholds struct {
	StaleDays          int     // 最終 activity から N 日経過で stale
	ScorecardMin       float64 // この値未満で alert
	OpenIssuesHigh     int     // この値以上で alert
	NowFn              func() time.Time
}

// DefaultThresholds は m's portfolio 想定で tuning した推奨値。
func DefaultThresholds() Thresholds {
	return Thresholds{
		StaleDays:      30,
		ScorecardMin:   5.0,
		OpenIssuesHigh: 20,
	}
}

// Evaluate は 1 つの project snapshot から alerts を生成する。
//
// 戻り値は alert list (severity 降順 + source alphabetical + project)。
func Evaluate(snap ProjectSnapshot, th Thresholds) []Alert {
	now := th.NowFn
	if now == nil {
		now = time.Now
	}
	var alerts []Alert

	// ─── Vuln ──────────────────────────────────────
	if snap.VulnCritical > 0 {
		alerts = append(alerts, Alert{
			ID:       buildID(snap.Slug, SourceVuln, "critical"),
			Project:  snap.Slug,
			Source:   SourceVuln,
			Severity: SevCritical,
			Title:    fmt.Sprintf("%d CRITICAL vulnerabilities", snap.VulnCritical),
			Description: fmt.Sprintf("%s has %d critical CVE matches in dependencies",
				snap.Slug, snap.VulnCritical),
			Recommendation: "Run yagura_vulns to inspect affected packages, then upgrade or pin in package manifests. Verify upgrade with yagura_quality_check before merging.",
			SuggestedTool:  "yagura_vulns",
			SuggestedArgs:  map[string]any{"slug": snap.Slug},
			MetricInt:      snap.VulnCritical,
			DetectedAt:     now(),
		})
	} else if snap.VulnHigh > 0 {
		alerts = append(alerts, Alert{
			ID:       buildID(snap.Slug, SourceVuln, "high"),
			Project:  snap.Slug,
			Source:   SourceVuln,
			Severity: SevHigh,
			Title:    fmt.Sprintf("%d HIGH vulnerabilities", snap.VulnHigh),
			Description: fmt.Sprintf("%s has %d high-severity CVE matches",
				snap.Slug, snap.VulnHigh),
			Recommendation: "Review HIGH vulns within 2 weeks (m's harness G7.8). Run yagura_vulns for details.",
			SuggestedTool:  "yagura_vulns",
			SuggestedArgs:  map[string]any{"slug": snap.Slug},
			MetricInt:      snap.VulnHigh,
			DetectedAt:     now(),
		})
	}

	// ─── CI ────────────────────────────────────────
	if strings.EqualFold(snap.CIStatus, "failing") {
		alerts = append(alerts, Alert{
			ID:             buildID(snap.Slug, SourceCI, ""),
			Project:        snap.Slug,
			Source:         SourceCI,
			Severity:       SevHigh,
			Title:          "CI failing",
			Description:    fmt.Sprintf("%s CI status reports failures", snap.Slug),
			Recommendation: "Run yagura_health for the latest CI snapshot. Block release_radar until passing.",
			SuggestedTool:  "yagura_health",
			SuggestedArgs:  map[string]any{"slug": snap.Slug},
			DetectedAt:     now(),
		})
	}

	// ─── Visibility mismatch (visibility literacy) ──
	// 人間が internal/confidential 等を宣言した project の repo が実際には Public。
	// 「Public のまま公開されてた!」を portfolio 単位で検出する。
	if snap.RepoPublic {
		if tag, ok := findSensitivityTag(snap.Tags); ok {
			alerts = append(alerts, Alert{
				ID:       buildID(snap.Slug, SourceVisibility, ""),
				Project:  snap.Slug,
				Source:   SourceVisibility,
				Severity: SevHigh,
				Title:    "Repository is PUBLIC despite a sensitivity tag",
				Description: fmt.Sprintf(
					"%s is tagged %q (declared internal/confidential) but its repository is public — anyone on the internet can read it.",
					snap.Slug, tag),
				Recommendation: "Confirm the repo's intended visibility. If it must stay internal, set the repository to Private on GitHub; otherwise remove the misleading sensitivity tag.",
				DetectedAt:     now(),
			})
		}
	}

	// ─── Plan.md ───────────────────────────────────
	if snap.HasPlanMd && !snap.PlanIsHealthy {
		issues := strings.Join(snap.PlanIssues, "; ")
		alerts = append(alerts, Alert{
			ID:       buildID(snap.Slug, SourcePlan, ""),
			Project:  snap.Slug,
			Source:   SourcePlan,
			Severity: SevMedium,
			Title:    "Plan.md missing required sections",
			Description: fmt.Sprintf("%s Plan.md issues: %s (harness G1.P required: 目的/スコープ/フェーズ/DoD)",
				snap.Slug, issues),
			Recommendation: "Edit Plan.md to add missing sections. Run yagura_plan_status to re-verify.",
			SuggestedTool:  "yagura_plan_status",
			SuggestedArgs:  map[string]any{"slug": snap.Slug},
			DetectedAt:     now(),
		})
	}

	// ─── Stale ─────────────────────────────────────
	if !snap.LatestActivity.IsZero() && th.StaleDays > 0 {
		age := now().Sub(snap.LatestActivity)
		if age > time.Duration(th.StaleDays)*24*time.Hour {
			days := int(age.Hours() / 24)
			alerts = append(alerts, Alert{
				ID:       buildID(snap.Slug, SourceStale, ""),
				Project:  snap.Slug,
				Source:   SourceStale,
				Severity: SevLow,
				Title:    fmt.Sprintf("No activity in %d days", days),
				Description: fmt.Sprintf("%s last activity was %s (>%d days)",
					snap.Slug, snap.LatestActivity.Format("2006-01-02"), th.StaleDays),
				Recommendation: "Check yagura_today for portfolio prioritization. Consider archiving or assigning maintainer.",
				SuggestedTool:  "yagura_today",
				MetricInt:      days,
				DetectedAt:     now(),
			})
		}
	}

	// ─── Scorecard ─────────────────────────────────
	if snap.ScorecardScore > 0 && snap.ScorecardScore < th.ScorecardMin {
		alerts = append(alerts, Alert{
			ID:       buildID(snap.Slug, SourceScorecard, ""),
			Project:  snap.Slug,
			Source:   SourceScorecard,
			Severity: SevMedium,
			Title:    fmt.Sprintf("OpenSSF Scorecard %.1f/10", snap.ScorecardScore),
			Description: fmt.Sprintf("%s Scorecard score %.1f below threshold %.1f",
				snap.Slug, snap.ScorecardScore, th.ScorecardMin),
			Recommendation: "Run yagura_scorecard for failing checks. Common wins: branch protection, signed releases, pinned deps.",
			SuggestedTool:  "yagura_scorecard",
			SuggestedArgs:  map[string]any{"slug": snap.Slug},
			MetricFloat:    snap.ScorecardScore,
			DetectedAt:     now(),
		})
	}

	// ─── Open issues ───────────────────────────────
	if th.OpenIssuesHigh > 0 && snap.OpenIssues >= th.OpenIssuesHigh {
		alerts = append(alerts, Alert{
			ID:       buildID(snap.Slug, SourceOpenIssues, ""),
			Project:  snap.Slug,
			Source:   SourceOpenIssues,
			Severity: SevLow,
			Title:    fmt.Sprintf("%d open issues", snap.OpenIssues),
			Description: fmt.Sprintf("%s has %d open issues (≥ threshold %d)",
				snap.Slug, snap.OpenIssues, th.OpenIssuesHigh),
			Recommendation: "Run yagura_get for issue list. Triage by label, close stale, prioritize SLA bugs (m's harness G11).",
			SuggestedTool:  "yagura_get",
			SuggestedArgs:  map[string]any{"slug": snap.Slug},
			MetricInt:      snap.OpenIssues,
			DetectedAt:     now(),
		})
	}

	rankAlerts(alerts)
	return alerts
}

// EvaluateAll は複数 project の snapshot を一括評価する。
func EvaluateAll(snaps []ProjectSnapshot, th Thresholds) Report {
	now := th.NowFn
	if now == nil {
		now = time.Now
	}
	report := Report{
		BySeverity:      map[Severity]int{},
		BySource:        map[Source]int{},
		ByProject:       map[string]int{},
		ProjectsScanned: len(snaps),
		GeneratedAt:     now(),
	}
	for _, s := range snaps {
		alerts := Evaluate(s, th)
		for _, a := range alerts {
			report.Alerts = append(report.Alerts, a)
			report.BySeverity[a.Severity]++
			report.BySource[a.Source]++
			report.ByProject[a.Project]++
			if a.Severity == SevCritical {
				report.HasCritical = true
			}
		}
	}
	report.Total = len(report.Alerts)
	rankAlerts(report.Alerts)
	return report
}

// rankAlerts は severity 降順 + source alphabetical + project alphabetical の
// deterministic sort を行う。
func rankAlerts(as []Alert) {
	sevWeight := map[Severity]int{
		SevCritical: 0,
		SevHigh:     1,
		SevMedium:   2,
		SevLow:      3,
	}
	sort.SliceStable(as, func(i, j int) bool {
		if sevWeight[as[i].Severity] != sevWeight[as[j].Severity] {
			return sevWeight[as[i].Severity] < sevWeight[as[j].Severity]
		}
		if as[i].Source != as[j].Source {
			return as[i].Source < as[j].Source
		}
		return as[i].Project < as[j].Project
	})
}

// buildID は alert の安定 ID を生成する。
//
// 同じ project + source + qualifier の組合せで同じ ID が得られる(de-dup 用途)。
func buildID(project string, src Source, qualifier string) string {
	var sb strings.Builder
	sb.WriteString(project)
	sb.WriteByte(':')
	sb.WriteString(string(src))
	if qualifier != "" {
		sb.WriteByte(':')
		sb.WriteString(qualifier)
	}
	return sb.String()
}

// Summary は人間可読 1 行サマリを返す。
func (r Report) Summary() string {
	if r.Total == 0 {
		return fmt.Sprintf("0 alerts across %d projects (healthy)", r.ProjectsScanned)
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("%d alerts", r.Total))
	if c := r.BySeverity[SevCritical]; c > 0 {
		parts = append(parts, fmt.Sprintf("CRIT=%d", c))
	}
	if h := r.BySeverity[SevHigh]; h > 0 {
		parts = append(parts, fmt.Sprintf("HIGH=%d", h))
	}
	parts = append(parts, fmt.Sprintf("across %d projects", r.ProjectsScanned))
	return strings.Join(parts, " ")
}
