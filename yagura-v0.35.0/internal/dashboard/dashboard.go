// Package dashboard は Yagura のミニマル HTML ダッシュボードを提供する。
//
// 設計判断:
//   - zero-dep: html/template のみ(stdlib)
//   - サーバサイドレンダリング基本(最小帯域)。表示は完全 SSR。
//   - progressive enhancement: 「+ Add a project」フォームだけは小さな inline JS で
//     /mcp(yagura_register)を叩く。非 CLI ユーザーが GUI から登録できるようにするため。
//     JS が無くても表示は壊れない。状態変更は MCP サーバ経由のみ(監査される。sensor 値は
//     scanner 専用のまま=trust base は不変)。
//   - 単一ページ: 全プロジェクトをテーブルで一覧表示
//   - スタイル: インライン CSS(外部リソース読込なし)
//   - 200ms 以内のレスポンスを目標(in-memory データなので余裕)
//
// 認証は呼出側(main.go)で HTTP middleware を被せる前提で、
// このパッケージは認証を持たない。
package dashboard

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/quotamonitor"
	"github.com/shizukutanaka/yagura/internal/registry"
)

// Handler は /dashboard HTTP ハンドラを生成する。
type Handler struct {
	registry     *registry.Registry
	logger       *slog.Logger
	tmpl         *template.Template
	activityTmpl *template.Template
	alertsTmpl   *template.Template
	now          func() time.Time
	// v0.14.0: optional agent status provider (Claude Code ↔ Windsurf handoff)
	quota AgentStatusProvider
	// v0.35: optional per-project hook activity (any agent's recorded tool calls)
	hooks HookActivityProvider
	// v0.35: optional portfolio health summary (latest alert_fix sweep)
	health PortfolioHealthProvider
}

// HookActivity は 1 プロジェクトのエージェント活動の最小 view(read-only 表示用)。
type HookActivity struct {
	Total     int
	Errors    int
	TopTool   string
	LastEvent time.Time
}

// ActivityDetail は 1 プロジェクトの構造化された agent 活動サマリ(read-only)。
// session_summary(internal/sessionsummary)の view 用写し。dashboard を薄く保つため
// sessionsummary を import せず、adapter 側で写像する(HookActivity と同じ流儀)。
type ActivityDetail struct {
	Slug            string
	Summary         string
	ToolInvocations int
	DistinctTools   int
	ErrorRate       float64
	Agents          []string
	ByTool          []LabelCount    // tool 別件数(降順 → 同数は name 昇順)
	ByOperation     []LabelCount    // operation 別件数
	Errors          []ActivityError // 直近のエラー
	ToolSequence    []string        // 実行順(切り詰めあり)
	SequenceTrunc   bool
	Anomalies       []string
}

// LabelCount は「ラベル → 件数」の決定論的に並べ替え済みエントリ。
type LabelCount struct {
	Label string
	Count int
}

// ActivityError は単一のエラーイベント(tool / 種別 / agent)。
type ActivityError struct {
	Tool      string
	ErrorType string
	Agent     string
}

// HookActivityProvider は project ごとの記録済みフック活動を返す。
// 実装は cmd/yagura の hookreceiver adapter。nil の場合は Activity 列が "—"。
//
// ProjectActivityDetail は Activity 列のドリルダウン(/dashboard/activity?slug=…)用に
// 構造化サマリを返す。記録が無ければ ok=false。
type HookActivityProvider interface {
	ProjectActivity(slug string) (HookActivity, bool)
	ProjectActivityDetail(slug string) (ActivityDetail, bool)
}

// HealthSummary は直近の alert_fix health sweep のポートフォリオ集計(read-only 表示用)。
type HealthSummary struct {
	Total       int
	Critical    int
	High        int
	Medium      int
	Low         int
	HasCritical bool
	At          time.Time
}

// AlertItem は health sweep が出した 1 件の alert(read-only 表示用)。dashboard を
// 薄く保つため alertfix を import せず、adapter 側で写像する(HealthSummary と同じ流儀)。
type AlertItem struct {
	ID             string // 安定 alert ID(resolve/snooze の対象)
	Project        string
	Source         string
	Severity       string
	Title          string
	Recommendation string
}

// PortfolioHealthProvider は直近の health sweep 結果を返す。実装は cmd/yagura の
// scanner AfterScan が更新する holder。nil / ok=false なら banner を出さない。
//
// PortfolioAlerts は banner のドリルダウン(/dashboard/alerts)用に個別 alert を
// severity 降順で返す。sweep 前は ok=false。
type PortfolioHealthProvider interface {
	PortfolioHealth() (HealthSummary, bool)
	PortfolioAlerts() (alerts []AlertItem, at time.Time, ok bool)
}

// AgentStatusProvider は agent quota の最小 view interface。
// 実装は internal/quotamonitor.Monitor。
// nil の場合は dashboard に agent panel を表示しない。
type AgentStatusProvider interface {
	AllStatuses() map[quotamonitor.Agent]quotamonitor.AgentStatus
	Recommend() (quotamonitor.Agent, string)
	AnyStale(idleTimeout time.Duration) []quotamonitor.Agent
	// v0.16.0: usage history (sparkline + numeric metrics)
	AllUsageSummaries() map[quotamonitor.Agent]quotamonitor.UsageSummary
}

// New は Handler を生成する。テンプレートは起動時に 1 度だけパースする。
func New(reg *registry.Registry, logger *slog.Logger) (*Handler, error) {
	if reg == nil {
		return nil, fmt.Errorf("registry is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	tmpl, err := template.New("dashboard").Funcs(template.FuncMap{
		"stageColor":    stageColor,
		"ciColor":       ciColor,
		"staleClass":    staleClassByTime,
		"priorityClass": priorityClass,
		"daysSince":     daysSince,
		"truncate":      truncate,
		"fmtTime":       fmtTime,
		"securityCell":  securityCell,
	}).Parse(htmlTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	atmpl, err := template.New("activity").Funcs(template.FuncMap{
		"pct": func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
	}).Parse(activityHTMLTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse activity template: %w", err)
	}
	altmpl, err := template.New("alerts").Funcs(template.FuncMap{
		"fmtTime": fmtTime,
	}).Parse(alertsHTMLTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse alerts template: %w", err)
	}
	return &Handler{registry: reg, logger: logger, tmpl: tmpl, activityTmpl: atmpl, alertsTmpl: altmpl, now: time.Now}, nil
}

// serveAlertDetail は /dashboard/alerts を捌く(health banner のドリルダウン、read-only)。
// provider 未設定 / sweep 前 / alert 0 件のいずれでも 404 ではなく案内文 + 戻りリンクを返す。
func (h *Handler) serveAlertDetail(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"Nonce": NonceFromContext(r.Context())}
	if h.health != nil {
		if alerts, at, ok := h.health.PortfolioAlerts(); ok {
			data["Found"] = true
			data["Alerts"] = alerts
			data["At"] = at
			data["Total"] = len(alerts)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.alertsTmpl.Execute(w, data); err != nil {
		h.logger.Error("render alert detail", "err", err)
	}
}

// serveActivityDetail は /dashboard/activity?slug=… を捌く(read-only ドリルダウン)。
// slug が registry に無い / activity provider 未設定 / 記録無しのいずれでも、404 ではなく
// 案内文 + dashboard への戻りリンクを返す(非 CLI ユーザの行き止まり回避)。
func (h *Handler) serveActivityDetail(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	data := map[string]any{"Slug": slug, "Nonce": NonceFromContext(r.Context())}

	var detail ActivityDetail
	var ok bool
	if slug != "" && h.hooks != nil {
		detail, ok = h.hooks.ProjectActivityDetail(slug)
	}
	// 表示名は registry から(無ければ slug のまま)。
	if p, err := h.registry.Get(slug); err == nil && p != nil {
		data["DisplayName"] = p.DisplayName
	} else {
		data["DisplayName"] = slug
	}
	data["Found"] = ok
	if ok {
		data["Detail"] = detail
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.activityTmpl.Execute(w, data); err != nil {
		h.logger.Error("render activity detail", "slug", slug, "err", err)
	}
}

// SetNowFunc lets tests pin the current time (used for stale calculations).
func (h *Handler) SetNowFunc(f func() time.Time) { h.now = f }

// SetAgentStatusProvider attaches a quota monitor for the agent panel.
// Pass nil to disable the panel.
func (h *Handler) SetAgentStatusProvider(p AgentStatusProvider) {
	h.quota = p
}

// SetHookActivityProvider attaches a per-project agent-activity source.
// Pass nil to disable the Activity column.
func (h *Handler) SetHookActivityProvider(p HookActivityProvider) {
	h.hooks = p
}

// SetPortfolioHealthProvider attaches the latest health-sweep summary source.
// Pass nil to hide the portfolio health banner.
func (h *Handler) SetPortfolioHealthProvider(p PortfolioHealthProvider) {
	h.health = p
}

// ServeHTTP は GET /dashboard を捌く。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.dispatchKnownSubPath(w, r) {
		return
	}

	now := h.now()
	projects := h.registry.List()
	sortProjectsForDashboard(projects)
	counts, failingCI, stale := summarizeProjects(projects, now)

	data := map[string]any{
		"Now":         now,
		"Total":       len(projects),
		"Active":      counts[project.StageActive],
		"Maintenance": counts[project.StageMaintenance],
		"Paused":      counts[project.StagePaused],
		"Archived":    counts[project.StageArchived],
		"FailingCI":   failingCI,
		"Stale":       stale,
		"Projects":    projects,
		"Nonce":       NonceFromContext(r.Context()),
	}
	// v0.35: per-project agent activity (Activity column). 活動のある project だけ載せる。
	data["Activity"] = h.buildActivityMap(projects)

	// v0.35: portfolio health banner (latest alert_fix sweep). Only shown when a
	// provider is wired and the sweep found alerts.
	if h.health != nil {
		if hs, ok := h.health.PortfolioHealth(); ok && hs.Total > 0 {
			data["Health"] = hs
		}
	}

	// v0.14.0: agent panel data
	if panel := h.buildAgentsPanel(); panel != nil {
		data["AgentsPanel"] = panel
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.tmpl.Execute(w, data); err != nil {
		h.logger.Error("dashboard: template execute failed", "err", err)
	}
}

// dispatchKnownSubPath は PWA アセット / Activity ドリルダウン / Alert
// ドリルダウンの既知 sub-path を処理する。処理済みなら true(呼び出し元は即 return)。
// 既存の HTML 描画には触れず、既知 path のときだけ分岐する(additive)。
func (h *Handler) dispatchKnownSubPath(w http.ResponseWriter, r *http.Request) bool {
	if serveAsset(w, r.URL.Path) {
		return true
	}
	if r.URL.Path == "/dashboard/activity" {
		h.serveActivityDetail(w, r)
		return true
	}
	if r.URL.Path == "/dashboard/alerts" {
		h.serveAlertDetail(w, r)
		return true
	}
	return false
}

// sortProjectsForDashboard は stage → priority(降順)→ slug の順に安定ソートする。
func sortProjectsForDashboard(projects []*project.Project) {
	stageRank := map[project.Stage]int{
		project.StageActive:      0,
		project.StageMaintenance: 1,
		project.StagePaused:      2,
		project.StageArchived:    3,
	}
	sort.SliceStable(projects, func(i, j int) bool {
		if stageRank[projects[i].Stage] != stageRank[projects[j].Stage] {
			return stageRank[projects[i].Stage] < stageRank[projects[j].Stage]
		}
		if projects[i].Priority != projects[j].Priority {
			return projects[i].Priority > projects[j].Priority
		}
		return projects[i].Slug < projects[j].Slug
	})
}

// summarizeProjects は stage 別件数・failing CI 件数・stale(active かつ 14 日超未活動)
// 件数を集計する。
func summarizeProjects(projects []*project.Project, now time.Time) (counts map[project.Stage]int, failingCI, stale int) {
	counts = map[project.Stage]int{}
	for _, p := range projects {
		counts[p.Stage]++
		if p.CIStatus == project.CIStatusFailing {
			failingCI++
		}
		if p.Stage == project.StageActive && !p.LatestActivity.IsZero() &&
			now.Sub(p.LatestActivity).Hours()/24 >= 14 {
			stale++
		}
	}
	return counts, failingCI, stale
}

// buildActivityMap は活動のある project だけの Activity 列データを組み立てる。
// h.hooks 未設定時は空 map(hooks なしでも Activity キーは常に存在する)。
func (h *Handler) buildActivityMap(projects []*project.Project) map[string]HookActivity {
	activity := map[string]HookActivity{}
	if h.hooks == nil {
		return activity
	}
	for _, p := range projects {
		if a, ok := h.hooks.ProjectActivity(p.Slug); ok && a.Total > 0 {
			activity[p.Slug] = a
		}
	}
	return activity
}

// buildAgentsPanel は v0.14.0 agent panel データ(表示順固定: Claude Code →
// Windsurf)を組み立てる。h.quota 未設定時は nil(呼び出し元は data キーを
// 設定しない)。
func (h *Handler) buildAgentsPanel() map[string]any {
	if h.quota == nil {
		return nil
	}
	statuses := h.quota.AllStatuses()
	recommended, reason := h.quota.Recommend()
	staleAgents := h.quota.AnyStale(quotamonitor.DefaultIdleTimeout)
	staleSet := map[quotamonitor.Agent]bool{}
	for _, a := range staleAgents {
		staleSet[a] = true
	}
	// v0.16.0: usage summaries(sparkline + numeric metrics)
	usages := h.quota.AllUsageSummaries()

	var agents []map[string]any
	for _, a := range []quotamonitor.Agent{
		quotamonitor.AgentClaudeCode, quotamonitor.AgentWindsurf,
	} {
		s, ok := statuses[a]
		if !ok {
			continue
		}
		u := usages[a]
		agents = append(agents, map[string]any{
			"Name":             string(s.Agent),
			"State":            string(s.State),
			"RemainingPercent": s.RemainingPercent,
			"LastReportSource": s.LastReportSource,
			"LastReportAt":     s.LastReportAt,
			"WindowResetsAt":   s.WindowResetsAt,
			"HandoffAt":        s.HandoffAt,
			"LastHeartbeatAt":  s.LastHeartbeatAt,
			"Stale":            staleSet[a],
			// v0.16.0: usage metrics
			"TotalReports":      u.TotalReports,
			"Consumed1h":        u.Consumed1h,
			"Consumed24h":       u.Consumed24h,
			"AvgConsumePerHour": u.AvgConsumePerHour,
			"WindowHours":       u.WindowHours,
			"HasUsageHistory":   u.TotalReports >= 2,
			"SparklinePath":     buildSparklinePath(u.Samples),
		})
	}
	return map[string]any{
		"Agents":               agents,
		"RecommendedAgent":     string(recommended),
		"RecommendationReason": reason,
	}
}

// ─── template helpers ────────────────────────────────────────

func stageColor(s project.Stage) string {
	switch s {
	case project.StageActive:
		return "stage-active"
	case project.StageMaintenance:
		return "stage-maintenance"
	case project.StagePaused:
		return "stage-paused"
	case project.StageArchived:
		return "stage-archived"
	}
	return ""
}

func ciColor(s project.CIStatus) string {
	switch s {
	case project.CIStatusPassing:
		return "ci-pass"
	case project.CIStatusFailing:
		return "ci-fail"
	}
	return "ci-unk"
}

func staleClass(days int) string {
	switch {
	case days < 0:
		return "unknown"
	case days <= 7:
		return "fresh"
	case days <= 30:
		return "warm"
	case days <= 90:
		return "cool"
	default:
		return "cold"
	}
}

// buildSparklinePath は ReportEvent サンプル列から SVG polyline 用の "x,y" 文字列を返す。
//
// viewBox は 100×30 を想定。
//   - x 軸: サンプル index を [0, 100] にマッピング
//   - y 軸: remaining_percent を [0, 30] にマッピング(0% が下端 30、100% が上端 0)
//
// サンプル不足(< 2)は空文字を返す(template 側で非表示)。
//
// 戻り値例: "0,10 25,15 50,20 75,18 100,25"
func buildSparklinePath(samples []quotamonitor.ReportEvent) string {
	const w, hgt = 100, 30
	if len(samples) < 2 {
		return ""
	}
	var sb strings.Builder
	n := len(samples)
	for i, ev := range samples {
		if i > 0 {
			sb.WriteByte(' ')
		}
		x := float64(i) * (float64(w) / float64(n-1))
		// remaining 100 → y=0、0 → y=30
		y := float64(hgt) - float64(ev.RemainingPercent)*float64(hgt)/100.0
		if y < 0 {
			y = 0
		}
		if y > float64(hgt) {
			y = float64(hgt)
		}
		fmt.Fprintf(&sb, "%.1f,%.1f", x, y)
	}
	return sb.String()
}

// staleClassByTime は template から呼ばれる版で、stage を考慮して
// active 以外には何も返さない(意図的な非活動を強調しないため)。
func staleClassByTime(t time.Time, now time.Time, stage project.Stage) string {
	if stage != project.StageActive || t.IsZero() {
		return ""
	}
	d := int(now.Sub(t).Hours() / 24)
	if d >= 30 {
		return "stale-red"
	}
	if d >= 14 {
		return "stale-amber"
	}
	return ""
}

func stageOrder(s project.Stage) int {
	switch s {
	case project.StageActive:
		return 0
	case project.StageMaintenance:
		return 1
	case project.StagePaused:
		return 2
	case project.StageArchived:
		return 3
	}
	return 99
}

func priorityClass(p int) string {
	if p >= 4 {
		return "prio-hi"
	}
	if p >= 2 {
		return "prio-mid"
	}
	return "prio-lo"
}

func daysSince(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return fmt.Sprintf("%dd", int(now.Sub(t).Hours()/24))
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04")
}

func truncate(s string, n int) string {
	if n <= 0 {
		return "" // nothing fits — avoid r[:n-1] negative index
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// securityCell は Project の security 状態を HTML 安全な文字列で返す。
// 表示パターン:
//   - 未スキャン:    "—"
//   - スコアあり:    "8.5 ✔︎"  or  "3.2 ⚠"  or  "1.0 ✗"
//   - vuln あり:     "8.5 ✔︎  ! 2C 1H"   (Critical 2 / High 1)
//   - スコアのみ:    "—  ! 2C"
func securityCell(p project.Project) template.HTML {
	hasScorecard := !p.ScorecardAt.IsZero()
	hasVulns := !p.VulnScanAt.IsZero()
	if !hasScorecard && !hasVulns {
		return template.HTML(`<span class="sec-na">—</span>`)
	}

	var b strings.Builder
	if hasScorecard {
		cls := securityScoreClass(p.ScorecardScore)
		b.WriteString(fmt.Sprintf(`<span class="%s" title="OpenSSF Scorecard: %s">%s</span>`,
			cls, scorecardTitle(p.ScorecardScore), formatScore(p.ScorecardScore)))
	} else {
		b.WriteString(`<span class="sec-na">—</span>`)
	}

	if hasVulns && p.TotalVulns() > 0 {
		parts := []string{}
		if p.VulnCritical > 0 {
			parts = append(parts, fmt.Sprintf(`<span class="vuln-crit">%dC</span>`, p.VulnCritical))
		}
		if p.VulnHigh > 0 {
			parts = append(parts, fmt.Sprintf(`<span class="vuln-high">%dH</span>`, p.VulnHigh))
		}
		if p.VulnMedium > 0 {
			parts = append(parts, fmt.Sprintf(`<span class="vuln-med">%dM</span>`, p.VulnMedium))
		}
		if p.VulnLow > 0 {
			parts = append(parts, fmt.Sprintf(`<span class="vuln-low">%dL</span>`, p.VulnLow))
		}
		b.WriteString(` <span class="vuln-sep">!</span> `)
		b.WriteString(strings.Join(parts, " "))
	}
	return template.HTML(b.String())
}

// securityScoreClass は Scorecard score に応じた CSS class を返す。
func securityScoreClass(s float64) string {
	switch {
	case s >= 8.0:
		return "sec-excellent"
	case s >= 6.0:
		return "sec-good"
	case s >= 4.0:
		return "sec-fair"
	case s > 0:
		return "sec-poor"
	}
	return "sec-na"
}

// formatScore は score を 1 桁小数で整形する。
func formatScore(s float64) string {
	return fmt.Sprintf("%.1f", s)
}

// scorecardTitle は score のカテゴリ名を返す(tooltip 用)。
func scorecardTitle(s float64) string {
	switch {
	case s >= 8.0:
		return "excellent"
	case s >= 6.0:
		return "good"
	case s >= 4.0:
		return "fair"
	default:
		return "poor"
	}
}

// activityHTMLTemplate は Activity 列ドリルダウン(/dashboard/activity?slug=…)の
// 単一プロジェクト活動詳細ページ。read-only。dashboard 本体と同じダークテーマ。
const activityHTMLTemplate = `<!DOCTYPE html>
<html lang="ja">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Activity — {{.DisplayName}} — Yagura</title>
<link rel="manifest" href="/dashboard/manifest.webmanifest">
<link rel="icon" type="image/svg+xml" href="/dashboard/icon.svg">
<style nonce="{{.Nonce}}">
:root{color-scheme:light dark;--bg:#0d1117;--surface:#161b22;--text:#e6edf3;--muted:#8b949e;--faint:#6e7681;--border:#30363d;--border-subtle:#21262d;--accent:#58a6ff;--accent-bar:#1f6feb;--danger:#f85149;--warn:#d29922;--ok:#3fb950;--danger-fg:#ffa198;--warn-fg:#e3b341;--danger-bg:#2d1213;--warn-bg:#2b2412;--btn:#238636;--btn-hover:#2ea043;--stripe:#1a1f26;--ok-bg:#1a4730;--warn-bg-strong:#4a3617;--danger-bg-strong:#4a1a1a;--neutral-bg:#2d333b}
@media (prefers-color-scheme:light){:root{--bg:#ffffff;--surface:#f6f8fa;--text:#1f2328;--muted:#59636e;--faint:#818b98;--border:#d0d7de;--border-subtle:#d8dee4;--accent:#0969da;--accent-bar:#0969da;--danger:#cf222e;--warn:#9a6700;--ok:#1a7f37;--danger-fg:#cf222e;--warn-fg:#7d4e00;--danger-bg:#ffebe9;--warn-bg:#fff8c5;--btn:#1f883d;--btn-hover:#1a7f37;--stripe:#eaeef2;--ok-bg:#dafbe1;--warn-bg-strong:#fff8c5;--danger-bg-strong:#ffebe9;--neutral-bg:#eaeef2}}
:root[data-theme="dark"]{--bg:#0d1117;--surface:#161b22;--text:#e6edf3;--muted:#8b949e;--faint:#6e7681;--border:#30363d;--border-subtle:#21262d;--accent:#58a6ff;--accent-bar:#1f6feb;--danger:#f85149;--warn:#d29922;--ok:#3fb950;--danger-fg:#ffa198;--warn-fg:#e3b341;--danger-bg:#2d1213;--warn-bg:#2b2412;--btn:#238636;--btn-hover:#2ea043;--stripe:#1a1f26;--ok-bg:#1a4730;--warn-bg-strong:#4a3617;--danger-bg-strong:#4a1a1a;--neutral-bg:#2d333b}
:root[data-theme="light"]{--bg:#ffffff;--surface:#f6f8fa;--text:#1f2328;--muted:#59636e;--faint:#818b98;--border:#d0d7de;--border-subtle:#d8dee4;--accent:#0969da;--accent-bar:#0969da;--danger:#cf222e;--warn:#9a6700;--ok:#1a7f37;--danger-fg:#cf222e;--warn-fg:#7d4e00;--danger-bg:#ffebe9;--warn-bg:#fff8c5;--btn:#1f883d;--btn-hover:#1a7f37;--stripe:#eaeef2;--ok-bg:#dafbe1;--warn-bg-strong:#fff8c5;--danger-bg-strong:#ffebe9;--neutral-bg:#eaeef2}

body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:var(--bg);color:var(--text);margin:0;padding:24px;line-height:1.5}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
h1{font-size:20px;margin:0 0 4px}
.muted{color:var(--muted);font-size:13px}
.back{display:inline-block;margin-bottom:16px}
.cards{display:flex;flex-wrap:wrap;gap:12px;margin:16px 0}
.card{background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:12px 16px;min-width:120px}
.card .n{font-size:22px;font-weight:600}
.card .l{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.04em}
.summary{background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:12px 16px;margin:12px 0}
h2{font-size:14px;color:var(--muted);text-transform:uppercase;letter-spacing:.04em;margin:24px 0 8px;border-bottom:1px solid var(--border-subtle);padding-bottom:4px}
table{border-collapse:collapse;width:100%;max-width:560px}
th,td{text-align:left;padding:4px 12px 4px 0;border-bottom:1px solid var(--border-subtle);font-size:13px}
th{color:var(--muted);font-weight:500}
td.num{text-align:right;font-variant-numeric:tabular-nums}
.bar{display:inline-block;height:8px;background:var(--accent-bar);border-radius:2px;vertical-align:middle;margin-left:8px}
.seq code{background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:1px 6px;margin:2px;display:inline-block;font-size:12px}
.err{color:var(--danger)}
.warn{color:var(--warn)}
.tag{background:var(--border-subtle);border-radius:10px;padding:1px 8px;font-size:12px;margin-right:4px}
.empty{color:var(--faint);font-style:italic}
</style>
<script nonce="{{.Nonce}}">(function(){try{var t=localStorage.getItem('yagura-theme');if(t==='light'||t==='dark')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
</head>
<body>
<a class="back" href="/dashboard">&larr; Portfolio dashboard</a>
<h1>{{.DisplayName}}</h1>
<div class="muted">Agent activity (any agent) recorded by the hook receiver · <code>{{.Slug}}</code></div>
{{if not .Found}}
<p class="empty" style="margin-top:24px">No recorded agent activity for this project yet. Tool calls appear here once an agent posts lifecycle events to <code>/hooks/agent</code>.</p>
{{else}}{{with .Detail}}
<div class="summary">{{.Summary}}</div>
<div class="cards">
  <div class="card"><div class="n">{{.ToolInvocations}}</div><div class="l">Tool calls</div></div>
  <div class="card"><div class="n">{{.DistinctTools}}</div><div class="l">Distinct tools</div></div>
  <div class="card"><div class="n {{if gt .ErrorRate 0.0}}err{{end}}">{{pct .ErrorRate}}</div><div class="l">Error rate</div></div>
</div>
{{if .Agents}}<div class="muted">Agents: {{range .Agents}}<span class="tag">{{.}}</span>{{end}}</div>{{end}}

<h2>By tool</h2>
{{if .ByTool}}<table>{{range .ByTool}}<tr><td>{{.Label}}</td><td class="num">{{.Count}}</td></tr>{{end}}</table>{{else}}<p class="empty">none</p>{{end}}

<h2>By operation</h2>
{{if .ByOperation}}<table>{{range .ByOperation}}<tr><td>{{.Label}}</td><td class="num">{{.Count}}</td></tr>{{end}}</table>{{else}}<p class="empty">none</p>{{end}}

{{if .ToolSequence}}<h2>Tool sequence{{if .SequenceTrunc}} (truncated){{end}}</h2>
<div class="seq">{{range .ToolSequence}}<code>{{.}}</code>{{end}}</div>{{end}}

{{if .Errors}}<h2>Errors</h2>
<table><tr><th>Tool</th><th>Type</th><th>Agent</th></tr>
{{range .Errors}}<tr><td class="err">{{if .Tool}}{{.Tool}}{{else}}—{{end}}</td><td>{{if .ErrorType}}{{.ErrorType}}{{else}}—{{end}}</td><td class="muted">{{if .Agent}}{{.Agent}}{{else}}—{{end}}</td></tr>{{end}}</table>{{end}}

{{if .Anomalies}}<h2>Anomalies</h2>
<ul>{{range .Anomalies}}<li class="warn">{{.}}</li>{{end}}</ul>{{end}}
{{end}}{{end}}
</body>
</html>`

// alertsHTMLTemplate は health banner ドリルダウン(/dashboard/alerts)の
// ポートフォリオ alert 一覧ページ。read-only。dashboard 本体と同じダークテーマ。
const alertsHTMLTemplate = `<!DOCTYPE html>
<html lang="ja">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Alerts — Yagura</title>
<link rel="manifest" href="/dashboard/manifest.webmanifest">
<link rel="icon" type="image/svg+xml" href="/dashboard/icon.svg">
<style nonce="{{.Nonce}}">
:root{color-scheme:light dark;--bg:#0d1117;--surface:#161b22;--text:#e6edf3;--muted:#8b949e;--faint:#6e7681;--border:#30363d;--border-subtle:#21262d;--accent:#58a6ff;--accent-bar:#1f6feb;--danger:#f85149;--warn:#d29922;--ok:#3fb950;--danger-fg:#ffa198;--warn-fg:#e3b341;--danger-bg:#2d1213;--warn-bg:#2b2412;--btn:#238636;--btn-hover:#2ea043;--stripe:#1a1f26;--ok-bg:#1a4730;--warn-bg-strong:#4a3617;--danger-bg-strong:#4a1a1a;--neutral-bg:#2d333b}
@media (prefers-color-scheme:light){:root{--bg:#ffffff;--surface:#f6f8fa;--text:#1f2328;--muted:#59636e;--faint:#818b98;--border:#d0d7de;--border-subtle:#d8dee4;--accent:#0969da;--accent-bar:#0969da;--danger:#cf222e;--warn:#9a6700;--ok:#1a7f37;--danger-fg:#cf222e;--warn-fg:#7d4e00;--danger-bg:#ffebe9;--warn-bg:#fff8c5;--btn:#1f883d;--btn-hover:#1a7f37;--stripe:#eaeef2;--ok-bg:#dafbe1;--warn-bg-strong:#fff8c5;--danger-bg-strong:#ffebe9;--neutral-bg:#eaeef2}}
:root[data-theme="dark"]{--bg:#0d1117;--surface:#161b22;--text:#e6edf3;--muted:#8b949e;--faint:#6e7681;--border:#30363d;--border-subtle:#21262d;--accent:#58a6ff;--accent-bar:#1f6feb;--danger:#f85149;--warn:#d29922;--ok:#3fb950;--danger-fg:#ffa198;--warn-fg:#e3b341;--danger-bg:#2d1213;--warn-bg:#2b2412;--btn:#238636;--btn-hover:#2ea043;--stripe:#1a1f26;--ok-bg:#1a4730;--warn-bg-strong:#4a3617;--danger-bg-strong:#4a1a1a;--neutral-bg:#2d333b}
:root[data-theme="light"]{--bg:#ffffff;--surface:#f6f8fa;--text:#1f2328;--muted:#59636e;--faint:#818b98;--border:#d0d7de;--border-subtle:#d8dee4;--accent:#0969da;--accent-bar:#0969da;--danger:#cf222e;--warn:#9a6700;--ok:#1a7f37;--danger-fg:#cf222e;--warn-fg:#7d4e00;--danger-bg:#ffebe9;--warn-bg:#fff8c5;--btn:#1f883d;--btn-hover:#1a7f37;--stripe:#eaeef2;--ok-bg:#dafbe1;--warn-bg-strong:#fff8c5;--danger-bg-strong:#ffebe9;--neutral-bg:#eaeef2}

body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:var(--bg);color:var(--text);margin:0;padding:24px;line-height:1.5}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
h1{font-size:20px;margin:0 0 4px}
.muted{color:var(--muted);font-size:13px}
.back{display:inline-block;margin-bottom:16px}
table{border-collapse:collapse;width:100%;max-width:900px;margin-top:12px}
th,td{text-align:left;padding:8px 12px;border-bottom:1px solid var(--border-subtle);font-size:13px;vertical-align:top}
th{color:var(--muted);font-weight:500;text-transform:uppercase;letter-spacing:.04em;font-size:11px}
.sev{font-weight:600;text-transform:uppercase;font-size:11px;letter-spacing:.04em}
.sev-critical{color:var(--danger)}
.sev-high{color:var(--warn)}
.sev-medium{color:var(--accent)}
.sev-low{color:var(--muted)}
.empty{color:var(--faint);font-style:italic;margin-top:24px}
code{background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:1px 6px;font-size:12px}
.act{display:flex;gap:6px}
.act button{height:28px;padding:0 10px;border-radius:6px;border:1px solid var(--border);background:var(--border-subtle);color:var(--text);font-size:12px;cursor:pointer}
.act .snz{height:28px;border-radius:6px;border:1px solid var(--border);background:var(--border-subtle);color:var(--text);font-size:12px}
.act .snz:disabled{opacity:.5}
.act button:hover{border-color:var(--accent)}
.act button.resolve:hover{border-color:var(--ok);color:var(--ok)}
.act button:disabled{opacity:.5;cursor:default}
#amsg{margin-top:10px;color:var(--muted);font-size:12px;min-height:1em}
tr.done{opacity:.45}
</style>
<script nonce="{{.Nonce}}">(function(){try{var t=localStorage.getItem('yagura-theme');if(t==='light'||t==='dark')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
</head>
<body>
<a class="back" href="/dashboard">&larr; Portfolio dashboard</a>
<h1>Portfolio alerts</h1>
{{if not .Found}}
<p class="empty">No alerts as of the latest health sweep — the portfolio is clean (or no sweep has run yet).</p>
{{else}}
<div class="muted">{{.Total}} alert{{if ne .Total 1}}s{{end}} from the latest alert_fix sweep · swept {{fmtTime .At}}</div>
<div id="amsg" role="status" aria-live="polite"></div>
<table>
  <thead><tr><th>Severity</th><th>Project</th><th>Source</th><th>Title</th><th>Recommendation</th><th>Actions</th></tr></thead>
  <tbody>
  {{range .Alerts}}
    <tr data-alert-id="{{.ID}}">
      <td class="sev sev-{{.Severity}}">{{.Severity}}</td>
      <td>{{.Project}}</td>
      <td class="muted">{{.Source}}</td>
      <td>{{.Title}}</td>
      <td class="muted">{{.Recommendation}}</td>
      <td><div class="act">
        <button class="resolve" data-act="resolve" title="Mark resolved (persisted, audited)">Resolve</button>
        <select class="snz" aria-label="Snooze duration" title="Snooze duration">
          <option value="1">1d</option>
          <option value="7" selected>7d</option>
          <option value="30">30d</option>
        </select>
        <button data-act="snooze" title="Snooze for the selected duration">Snooze</button>
      </div></td>
    </tr>
  {{end}}
  </tbody>
</table>
<script nonce="{{.Nonce}}">
(function(){
  var msg=document.getElementById('amsg');
  function call(id,action,btn){
    var args={alert_id:id,action:action};
    var days=7;
    if(action==='snooze'){
      var sel=btn.parentNode.querySelector('.snz');
      days=(sel&&parseInt(sel.value,10))||7;
      args.snooze_days=days;
    }
    var row=btn.closest('tr');
    row.querySelectorAll('button,select').forEach(function(b){b.disabled=true;});
    msg.textContent=(action==='resolve'?'Resolving ':'Snoozing '+days+'d ')+id+'…';
    fetch('/mcp',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'yagura_alert_resolve',arguments:args}})})
      .then(function(r){ if(!r.ok) throw new Error('HTTP '+r.status); return r.json(); })
      .then(function(j){ if(j.error) throw new Error((j.error&&j.error.message)||'action failed');
        row.classList.add('done'); msg.textContent=id+' '+(action==='resolve'?'resolved':'snoozed '+days+'d')+'. It will drop off after the next scan.'; })
      .catch(function(err){ row.querySelectorAll('button,select').forEach(function(b){b.disabled=false;});
        msg.textContent='Could not '+action+': '+err.message+' — if this instance needs a token, use: yagura (CLI) or the MCP client.'; });
  }
  document.querySelectorAll('.act button').forEach(function(btn){
    btn.addEventListener('click',function(){
      var row=btn.closest('tr'); call(row.getAttribute('data-alert-id'),btn.getAttribute('data-act'),btn);
    });
  });
})();
</script>
{{end}}
</body>
</html>`

const htmlTemplate = `<!DOCTYPE html>
<html lang="ja">
<head>
<meta charset="UTF-8">
<title>Yagura Portfolio Dashboard</title>
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="theme-color" content="#ffffff" media="(prefers-color-scheme: light)">
<meta name="theme-color" content="#0d1117" media="(prefers-color-scheme: dark)">
<link rel="manifest" href="/dashboard/manifest.webmanifest">
<link rel="icon" type="image/svg+xml" href="/dashboard/icon.svg">
<link rel="apple-touch-icon" href="/dashboard/icon.svg">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="mobile-web-app-capable" content="yes">
<meta name="apple-mobile-web-app-title" content="Yagura">
<style nonce="{{.Nonce}}">
:root{color-scheme:light dark;--bg:#0d1117;--surface:#161b22;--text:#e6edf3;--muted:#8b949e;--faint:#6e7681;--border:#30363d;--border-subtle:#21262d;--accent:#58a6ff;--accent-bar:#1f6feb;--danger:#f85149;--warn:#d29922;--ok:#3fb950;--danger-fg:#ffa198;--warn-fg:#e3b341;--danger-bg:#2d1213;--warn-bg:#2b2412;--btn:#238636;--btn-hover:#2ea043;--stripe:#1a1f26;--ok-bg:#1a4730;--warn-bg-strong:#4a3617;--danger-bg-strong:#4a1a1a;--neutral-bg:#2d333b}
@media (prefers-color-scheme:light){:root{--bg:#ffffff;--surface:#f6f8fa;--text:#1f2328;--muted:#59636e;--faint:#818b98;--border:#d0d7de;--border-subtle:#d8dee4;--accent:#0969da;--accent-bar:#0969da;--danger:#cf222e;--warn:#9a6700;--ok:#1a7f37;--danger-fg:#cf222e;--warn-fg:#7d4e00;--danger-bg:#ffebe9;--warn-bg:#fff8c5;--btn:#1f883d;--btn-hover:#1a7f37;--stripe:#eaeef2;--ok-bg:#dafbe1;--warn-bg-strong:#fff8c5;--danger-bg-strong:#ffebe9;--neutral-bg:#eaeef2}}
:root[data-theme="dark"]{--bg:#0d1117;--surface:#161b22;--text:#e6edf3;--muted:#8b949e;--faint:#6e7681;--border:#30363d;--border-subtle:#21262d;--accent:#58a6ff;--accent-bar:#1f6feb;--danger:#f85149;--warn:#d29922;--ok:#3fb950;--danger-fg:#ffa198;--warn-fg:#e3b341;--danger-bg:#2d1213;--warn-bg:#2b2412;--btn:#238636;--btn-hover:#2ea043;--stripe:#1a1f26;--ok-bg:#1a4730;--warn-bg-strong:#4a3617;--danger-bg-strong:#4a1a1a;--neutral-bg:#2d333b}
:root[data-theme="light"]{--bg:#ffffff;--surface:#f6f8fa;--text:#1f2328;--muted:#59636e;--faint:#818b98;--border:#d0d7de;--border-subtle:#d8dee4;--accent:#0969da;--accent-bar:#0969da;--danger:#cf222e;--warn:#9a6700;--ok:#1a7f37;--danger-fg:#cf222e;--warn-fg:#7d4e00;--danger-bg:#ffebe9;--warn-bg:#fff8c5;--btn:#1f883d;--btn-hover:#1a7f37;--stripe:#eaeef2;--ok-bg:#dafbe1;--warn-bg-strong:#fff8c5;--danger-bg-strong:#ffebe9;--neutral-bg:#eaeef2}

* { box-sizing: border-box; margin: 0; padding: 0; }
html { font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Hiragino Sans", "Yu Gothic UI", sans-serif; }
body { background: var(--bg); color: var(--text); padding: 24px; }
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
.wrap { max-width: 1400px; margin: 0 auto; }
header { margin-bottom: 24px; padding-bottom: 16px; border-bottom: 1px solid var(--border); display: flex; justify-content: space-between; align-items: baseline; flex-wrap: wrap; gap: 12px; }
h1 { font-size: 18px; font-weight: 700; letter-spacing: 0.5px; }
h1 .meta { font-weight: 400; color: var(--muted); font-size: 12px; margin-left: 8px; }
.kpi-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 8px; margin-bottom: 24px; }
.kpi { background: var(--surface); border: 1px solid var(--border); padding: 12px 16px; border-radius: 6px; }
.kpi .label { font-size: 11px; color: var(--muted); text-transform: uppercase; letter-spacing: 0.5px; }
.kpi .val { font-size: 22px; font-weight: 600; margin-top: 4px; }
.kpi.danger .val { color: var(--danger); }
.kpi.warn .val { color: var(--warn); }
.kpi.ok .val { color: var(--ok); }
.health-banner { border-radius: 6px; padding: 10px 14px; margin-bottom: 24px; font-size: 13px; border: 1px solid; }
.health-banner.crit { background: var(--danger-bg); border-color: var(--danger); color: var(--danger-fg); }
.health-banner.warn { background: var(--warn-bg); border-color: var(--warn); color: var(--warn-fg); }
.health-banner .hb-breakdown { margin-left: 6px; }
.health-banner .hb-crit { color: var(--danger); font-weight: 600; }
.health-banner .hb-high { color: var(--warn); font-weight: 600; }
.health-banner .hb-meta { display: block; margin-top: 4px; color: var(--muted); font-size: 11px; }
table { width: 100%; border-collapse: collapse; background: var(--bg); }
th, td { padding: 10px 12px; text-align: left; border-bottom: 1px solid var(--border-subtle); font-size: 13px; }
th { color: var(--muted); font-weight: 600; font-size: 11px; text-transform: uppercase; letter-spacing: 0.5px; background: var(--surface); }
tr:hover td { background: var(--surface); }
.slug { font-weight: 600; }
.repo a { font-size: 12px; }
.stage { display: inline-block; padding: 2px 8px; font-size: 11px; border-radius: 3px; font-weight: 600; }
.stage-active      { background: #1f6feb33; color: var(--accent); }
.stage-maintenance { background: #d2992233; color: var(--warn); }
.stage-paused      { background: #8b949e33; color: var(--muted); }
.stage-archived    { background: var(--border-subtle);   color: var(--faint); }
.ci-pass { color: var(--ok); }
.ci-fail { color: var(--danger); font-weight: 700; }
.ci-unk  { color: var(--faint); }
.prio-hi  { color: var(--danger); font-weight: 700; }
.prio-mid { color: var(--warn); }
.prio-lo  { color: var(--muted); }
.stale-red   { color: var(--danger); font-weight: 700; }
.stale-amber { color: var(--warn); }
.security { font-family: monospace; font-size: 12px; white-space: nowrap; }
.sec-na          { color: var(--faint); }
.sec-excellent   { color: var(--ok); font-weight: 700; }
.sec-good        { color: var(--accent); }
.sec-fair        { color: var(--warn); }
.sec-poor        { color: var(--danger); font-weight: 700; }
.vuln-sep        { color: var(--faint); font-weight: 700; }
.vuln-crit       { color: var(--danger); font-weight: 700; background: rgba(248,81,73,0.15); padding: 0 4px; border-radius: 3px; }
.vuln-high       { color: var(--warn); font-weight: 700; background: rgba(210,153,34,0.15); padding: 0 4px; border-radius: 3px; }
.vuln-med        { color: var(--warn); }
.vuln-low        { color: var(--muted); }
.tags { font-size: 11px; }
.tag { display: inline-block; background: var(--border-subtle); color: var(--muted); padding: 1px 6px; border-radius: 3px; margin-right: 4px; font-family: monospace; }
footer { margin-top: 32px; padding-top: 16px; border-top: 1px solid var(--border); color: var(--faint); font-size: 12px; text-align: center; }
.header-right { display: flex; align-items: center; gap: 12px; }
.theme-toggle { background: var(--surface); border: 1px solid var(--border); color: var(--text); border-radius: 6px; width: 32px; height: 32px; cursor: pointer; font-size: 15px; line-height: 1; padding: 0; }
.theme-toggle:hover { border-color: var(--accent); }
.empty { text-align: center; padding: 40px; color: var(--muted); }
.empty code { background: var(--border-subtle); padding: 2px 8px; border-radius: 3px; }
.addproj { margin-bottom: 24px; background: var(--surface); border: 1px solid var(--border); border-radius: 6px; }
.addproj > summary { cursor: pointer; padding: 12px 16px; font-weight: 600; color: var(--text); user-select: none; }
.addproj[open] > summary { border-bottom: 1px solid var(--border); }
.addproj form { padding: 16px; display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 12px; align-items: end; }
.addproj label { display: block; font-size: 12px; color: var(--muted); margin-bottom: 4px; }
.addproj input { width: 100%; height: 36px; padding: 0 10px; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; color: var(--text); }
.addproj input:focus { outline: none; border-color: var(--accent); }
.addproj .actions { grid-column: 1 / -1; display: flex; align-items: center; gap: 12px; }
.addproj button { height: 36px; padding: 0 16px; background: var(--btn); border: none; border-radius: 6px; color: #fff; font-weight: 600; cursor: pointer; }
.addproj button:hover { background: var(--btn-hover); }
.addproj .msg { color: var(--muted); font-size: 12px; }
.summary { color: var(--muted); font-size: 12px; margin-bottom: 12px; }

/* ─── WCAG 2.1 AA accessibility ─────────────────────────── */
/* Skip-to-content link (only visible when keyboard focused) */
.skip-link {
  position: absolute;
  top: -40px;
  left: 0;
  background: var(--accent);
  color: var(--bg);
  padding: 8px 16px;
  text-decoration: none;
  font-weight: 600;
  z-index: 100;
}
.skip-link:focus {
  top: 0;
}
/* Visible keyboard focus indicator (WCAG 2.4.7) */
:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
a:focus-visible {
  outline-offset: 4px;
}
/* Screen-reader-only utility class (table caption etc.) */
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
/* Respect user motion preferences (WCAG 2.3.3) */
@media (prefers-reduced-motion: reduce) {
  * { animation-duration: 0.01ms !important; transition-duration: 0.01ms !important; }
}
/* v0.14.0: agent handoff panel */
.agents { margin: 32px 0; border-top: 1px solid var(--border); padding-top: 20px; }
.agents h2 { font-size: 14px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: 1px; margin-bottom: 12px; }
.agents-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 12px; }
.agent-card { background: var(--surface); border: 1px solid var(--border); border-left: 3px solid var(--accent); padding: 14px 16px; border-radius: 6px; }
.agent-card.agent-ACTIVE { border-left-color: var(--ok); color: var(--ok); }
.agent-card.agent-WARN { border-left-color: var(--warn); color: var(--warn); }
.agent-card.agent-EXHAUSTED { border-left-color: var(--danger); color: var(--danger); }
.agent-card.agent-SWITCHED { border-left-color: var(--muted); color: var(--muted); opacity: 0.7; }
.agent-card.agent-stale { background: repeating-linear-gradient(45deg, var(--surface), var(--surface) 8px, var(--stripe) 8px, var(--stripe) 16px); }
.agent-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.agent-name { font-family: monospace; font-size: 13px; font-weight: 600; color: var(--text); }
.agent-state { font-size: 10px; padding: 2px 8px; border-radius: 3px; letter-spacing: 0.5px; }
.badge-ACTIVE { background: var(--ok-bg); color: var(--ok); }
.badge-WARN { background: var(--warn-bg-strong); color: var(--warn); }
.badge-EXHAUSTED { background: var(--danger-bg-strong); color: var(--danger); }
.badge-SWITCHED { background: var(--neutral-bg); color: var(--muted); }
.agent-quota { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.quota-bar { flex: 1; height: 8px; background: var(--bg); border: 1px solid var(--border); border-radius: 3px; overflow: hidden; }
.quota-fill { height: 100%; background: var(--ok); transition: width 0.3s; }
.agent-WARN .quota-fill { background: var(--warn); }
.agent-EXHAUSTED .quota-fill { background: var(--danger); }
.quota-pct { font-family: monospace; font-size: 12px; font-weight: 600; min-width: 38px; text-align: right; color: var(--text); }
/* v0.16.0: usage panel(sparkline + metrics)*/
.agent-usage { display: grid; grid-template-columns: 1fr auto; gap: 12px; align-items: center; margin-bottom: 10px; padding: 8px 10px; background: var(--bg); border: 1px solid var(--border-subtle); border-radius: 4px; }
.sparkline { width: 100%; max-width: 140px; height: 28px; display: block; }
.usage-metrics { display: grid; grid-template-columns: auto auto; gap: 2px 10px; font-size: 11px; font-family: monospace; align-content: center; }
.usage-row { display: contents; }
.usage-row dt { color: var(--faint); }
.usage-row dd { color: var(--text); font-weight: 600; }
.agent-meta { display: grid; grid-template-columns: auto 1fr; gap: 4px 8px; font-size: 11px; }
.agent-meta dt { color: var(--muted); }
.agent-meta dd { color: var(--text); font-family: monospace; }
.agent-meta .stale-flag { color: var(--warn); }
.agent-recommend { margin-top: 12px; padding: 8px 12px; background: var(--bg); border: 1px solid var(--border); border-radius: 4px; font-size: 12px; color: var(--muted); }
.agent-recommend strong { color: var(--accent); font-family: monospace; }
.agent-recommend small { color: var(--faint); }
</style>
<script nonce="{{.Nonce}}">(function(){try{var t=localStorage.getItem('yagura-theme');if(t==='light'||t==='dark')document.documentElement.setAttribute('data-theme',t);}catch(e){}})();</script>
</head>
<body>
<a href="#main" class="skip-link">Skip to main content</a>
<div class="wrap">
  <header>
    <h1>櫓 Yagura <span class="meta">Portfolio Dashboard</span></h1>
    <div class="header-right">
      <span style="color:var(--muted);font-size:12px" aria-label="Page generated at">{{fmtTime .Now}}</span>
      <button id="theme-toggle" class="theme-toggle" type="button" aria-label="Toggle light/dark theme" title="Toggle light/dark theme (follows your OS by default)">◐</button>
    </div>
  </header>

  <main id="main">
  <div class="summary" aria-live="polite">{{.Total}} projects</div>

  <div class="kpi-row" role="group" aria-label="Portfolio summary metrics">
    <div class="kpi" role="status"><div class="label">Total</div><div class="val">{{.Total}}</div></div>
    <div class="kpi ok" role="status"><div class="label">Active</div><div class="val">{{.Active}}</div></div>
    <div class="kpi" role="status"><div class="label">Maint.</div><div class="val">{{.Maintenance}}</div></div>
    <div class="kpi" role="status"><div class="label">Paused</div><div class="val">{{.Paused}}</div></div>
    <div class="kpi" role="status"><div class="label">Archived</div><div class="val">{{.Archived}}</div></div>
    <div class="kpi {{if gt .FailingCI 0}}danger{{else}}ok{{end}}" role="status"><div class="label">CI Failing</div><div class="val">{{.FailingCI}}</div></div>
    <div class="kpi {{if gt .Stale 0}}warn{{else}}ok{{end}}" role="status"><div class="label">Stale ≥14d</div><div class="val">{{.Stale}}</div></div>
  </div>

  {{with .Health}}
  <div class="health-banner {{if .HasCritical}}crit{{else}}warn{{end}}" role="status" aria-label="Portfolio health from the latest alert_fix sweep">
    <strong>{{if .HasCritical}}⛔{{else}}⚠{{end}} Portfolio health:</strong>
    {{.Total}} alert{{if ne .Total 1}}s{{end}}
    <span class="hb-breakdown">{{if gt .Critical 0}}<span class="hb-crit">{{.Critical}} critical</span>{{end}}{{if gt .High 0}}{{if gt .Critical 0}} · {{end}}<span class="hb-high">{{.High}} high</span>{{end}}{{if gt .Medium 0}}{{if or (gt .Critical 0) (gt .High 0)}} · {{end}}{{.Medium}} medium{{end}}{{if gt .Low 0}}{{if or (gt .Critical 0) (gt .High 0) (gt .Medium 0)}} · {{end}}{{.Low}} low{{end}}</span>
    <small class="hb-meta"><a href="/dashboard/alerts">view alerts</a> · swept {{fmtTime .At}}</small>
  </div>
  {{end}}

  <details class="addproj" {{if eq .Total 0}}open{{end}}>
    <summary>+ Add a project</summary>
    <form id="addproj-form" autocomplete="off">
      <div><label for="ap-slug">Slug (a-z0-9-)</label><input id="ap-slug" name="slug" required pattern="[a-z0-9][a-z0-9-]{0,49}" placeholder="breeze"></div>
      <div><label for="ap-repo">Repository (owner/repo)</label><input id="ap-repo" name="repository" required placeholder="shizukutanaka/breeze"></div>
      <div><label for="ap-name">Display name (optional)</label><input id="ap-name" name="display_name" placeholder="Breeze"></div>
      <div><label for="ap-lang">Language (optional)</label><input id="ap-lang" name="language" placeholder="go"></div>
      <div><label for="ap-path">Local path (optional)</label><input id="ap-path" name="local_path" placeholder="/home/you/code/breeze"></div>
      <div class="actions"><button type="submit">Register</button><span id="addproj-msg" class="msg" role="status" aria-live="polite"></span></div>
    </form>
  </details>

  {{if eq .Total 0}}
  <div class="empty" role="region" aria-label="Empty portfolio">
    No projects registered yet.<br>
    Add your first one with the <strong>+ Add a project</strong> form above — no terminal needed.
  </div>
  {{else}}
  <table aria-label="Registered projects">
    <caption class="sr-only">List of all registered projects with their status, security score, and metadata. Sorted by stage, then priority, then slug.</caption>
    <thead>
      <tr>
        <th scope="col">Slug</th>
        <th scope="col">Repository</th>
        <th scope="col">Stage</th>
        <th scope="col" abbr="Priority">Pri</th>
        <th scope="col" abbr="Language">Lang</th>
        <th scope="col" abbr="Latest version">Ver</th>
        <th scope="col" abbr="Continuous Integration status">CI</th>
        <th scope="col" abbr="Open pull requests">PR</th>
        <th scope="col" abbr="Open issues">Iss</th>
        <th scope="col">Last commit</th>
        <th scope="col" abbr="Agent tool-call activity" title="Recorded agent tool calls (any agent): total · errors · top tool">Activity</th>
        <th scope="col" title="OpenSSF Scorecard score (0-10) and vulnerability counts">Security</th>
        <th scope="col">Tags</th>
      </tr>
    </thead>
    <tbody>
    {{range .Projects}}
      <tr>
        <td class="slug">{{.Slug}}<br><small style="color:#8b949e">{{.DisplayName}}</small></td>
        <td class="repo"><a href="https://github.com/{{.Repository}}" target="_blank" rel="noopener noreferrer">{{.Repository}}</a></td>
        <td><span class="stage {{stageColor .Stage}}">{{.Stage}}</span></td>
        <td><span class="{{priorityClass .Priority}}">{{.Priority}}</span></td>
        <td>{{.Language}}</td>
        <td>{{if .LatestVersion}}{{.LatestVersion}}{{else}}—{{end}}</td>
        <td><span class="{{ciColor .CIStatus}}">{{if .CIStatus}}{{.CIStatus}}{{else}}—{{end}}</span></td>
        <td>{{.OpenPRs}}</td>
        <td>{{.OpenIssues}}</td>
        <td class="{{staleClass .LatestActivity $.Now .Stage}}">{{daysSince .LatestActivity $.Now}}</td>
        <td class="activity">{{$a := index $.Activity .Slug}}{{if gt $a.Total 0}}<a href="/dashboard/activity?slug={{.Slug}}" title="view session activity">{{$a.Total}}</a>{{if gt $a.Errors 0}} <span class="ci-fail" title="errors">{{$a.Errors}}⚠</span>{{end}}{{if $a.TopTool}} <small style="color:#8b949e" title="top tool">{{$a.TopTool}}</small>{{end}}{{else}}<span style="color:#6e7681">—</span>{{end}}</td>
        <td class="security">{{securityCell .}}</td>
        <td class="tags">{{range .Tags}}<span class="tag">{{.}}</span>{{end}}</td>
      </tr>
    {{end}}
    </tbody>
  </table>
  {{end}}

  {{with .AgentsPanel}}
  <section class="agents" role="group" aria-label="AI agent quota status">
    <h2>Agent Handoff Status</h2>
    <div class="agents-grid">
    {{range .Agents}}
      <article class="agent-card agent-{{.State}}{{if .Stale}} agent-stale{{end}}" aria-label="{{.Name}} state {{.State}}">
        <header class="agent-head">
          <span class="agent-name">{{.Name}}</span>
          <span class="agent-state badge-{{.State}}">{{.State}}</span>
        </header>
        <div class="agent-quota">
          <div class="quota-bar" role="progressbar" aria-valuenow="{{.RemainingPercent}}" aria-valuemin="0" aria-valuemax="100" aria-label="remaining quota {{.RemainingPercent}}%">
            <div class="quota-fill" style="width:{{.RemainingPercent}}%"></div>
          </div>
          <span class="quota-pct">{{.RemainingPercent}}%</span>
        </div>
        {{if .HasUsageHistory}}
        <div class="agent-usage" aria-label="usage history for {{.Name}}">
          <svg class="sparkline" viewBox="0 0 100 30" preserveAspectRatio="none" aria-label="quota over last {{.TotalReports}} reports">
            <polyline points="{{.SparklinePath}}" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/>
          </svg>
          <dl class="usage-metrics">
            <div class="usage-row"><dt>reports</dt><dd>{{.TotalReports}}</dd></div>
            {{if gt .Consumed1h 0.0}}<div class="usage-row"><dt>1h</dt><dd>−{{printf "%.0f" .Consumed1h}}%</dd></div>{{end}}
            {{if gt .Consumed24h 0.0}}<div class="usage-row"><dt>24h</dt><dd>−{{printf "%.0f" .Consumed24h}}%</dd></div>{{end}}
            {{if gt .AvgConsumePerHour 0.0}}<div class="usage-row"><dt>avg/h</dt><dd>−{{printf "%.1f" .AvgConsumePerHour}}%</dd></div>{{end}}
          </dl>
        </div>
        {{end}}
        <dl class="agent-meta">
          {{if .LastReportSource}}<dt>source</dt><dd>{{.LastReportSource}}</dd>{{end}}
          {{if not .HandoffAt.IsZero}}<dt>handoff at</dt><dd>{{fmtTime .HandoffAt}}</dd>{{end}}
          {{if not .LastHeartbeatAt.IsZero}}<dt>heartbeat</dt><dd>{{fmtTime .LastHeartbeatAt}}</dd>{{end}}
          {{if .Stale}}<dt class="stale-flag">stale</dt><dd>(no heartbeat ≥ 10m)</dd>{{end}}
        </dl>
      </article>
    {{end}}
    </div>
    {{if .RecommendedAgent}}
    <p class="agent-recommend" role="status">
      → Recommended: <strong>{{.RecommendedAgent}}</strong>
      <small>({{.RecommendationReason}})</small>
    </p>
    {{end}}
  </section>
  {{end}}
  </main>

  <footer role="contentinfo">櫓 Yagura — Portfolio Orchestrator · v0.121.0</footer>
</div>
<script nonce="{{.Nonce}}">if('serviceWorker' in navigator){navigator.serviceWorker.register('/dashboard/sw.js',{scope:'/dashboard'}).catch(function(){});}</script>
<script nonce="{{.Nonce}}">(function(){var b=document.getElementById('theme-toggle');if(!b)return;b.addEventListener('click',function(){var cur=document.documentElement.getAttribute('data-theme');var osDark=window.matchMedia&&window.matchMedia('(prefers-color-scheme: dark)').matches;var next=cur==='dark'?'light':cur==='light'?'dark':osDark?'light':'dark';document.documentElement.setAttribute('data-theme',next);try{localStorage.setItem('yagura-theme',next);}catch(e){}});})();</script>
<script nonce="{{.Nonce}}">
(function(){
  var f=document.getElementById('addproj-form'); if(!f)return;
  var msg=document.getElementById('addproj-msg');
  f.addEventListener('submit',function(e){
    e.preventDefault();
    var args={};
    ['slug','repository','display_name','language','local_path'].forEach(function(k){
      var el=f.elements[k]; if(el&&el.value.trim())args[k]=el.value.trim();
    });
    msg.textContent='Registering…';
    fetch('/mcp',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'yagura_register',arguments:args}})})
      .then(function(r){ if(!r.ok) throw new Error('HTTP '+r.status); return r.json(); })
      .then(function(j){ if(j.error) throw new Error((j.error&&j.error.message)||'register failed');
        msg.textContent='Registered. Reloading…'; location.reload(); })
      .catch(function(err){ msg.textContent='Could not register: '+err.message+
        ' — if this instance needs a token, use the CLI: yagura register <slug> <owner/repo>'; });
  });
})();
</script>
</body>
</html>`
