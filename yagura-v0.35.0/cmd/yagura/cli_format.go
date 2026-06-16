// cli_format.go: output formatters for CLI direct mode (v0.35.0).
//
// 2 つの出力経路:
//   - emitJSON: MCP tool handler が返すのと同じ struct/map を indent 付き JSON で
//     出す(--json)。既存 json タグを再利用するので shape は MCP と一致する。
//   - human*: text/tabwriter による整列テキスト(デフォルト)。verify / secret
//     subcommand と同じ「素朴で grep しやすい」世界観。

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shizukutanaka/yagura/internal/aiverify"
	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/apidoc"
	"github.com/shizukutanaka/yagura/internal/assertcheck"
	"github.com/shizukutanaka/yagura/internal/astcheck"
	"github.com/shizukutanaka/yagura/internal/audit"
	"github.com/shizukutanaka/yagura/internal/ccsecurity"
	"github.com/shizukutanaka/yagura/internal/codehealth"
	"github.com/shizukutanaka/yagura/internal/complexity"
	"github.com/shizukutanaka/yagura/internal/coupling"
	"github.com/shizukutanaka/yagura/internal/coverage"
	"github.com/shizukutanaka/yagura/internal/deadcode"
	"github.com/shizukutanaka/yagura/internal/diffscan"
	"github.com/shizukutanaka/yagura/internal/errpolicy"
	"github.com/shizukutanaka/yagura/internal/flowrisk"
	"github.com/shizukutanaka/yagura/internal/ghaaudit"
	"github.com/shizukutanaka/yagura/internal/harness"
	"github.com/shizukutanaka/yagura/internal/pathpolicy"
	"github.com/shizukutanaka/yagura/internal/pindrift"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/publicityscan"
	"github.com/shizukutanaka/yagura/internal/qualitycheck"
	"github.com/shizukutanaka/yagura/internal/recvcheck"
	"github.com/shizukutanaka/yagura/internal/reviewgate"
	"github.com/shizukutanaka/yagura/internal/sbom"
	"github.com/shizukutanaka/yagura/internal/secretscan"
	"github.com/shizukutanaka/yagura/internal/testcoverage"
	"github.com/shizukutanaka/yagura/internal/today"
)

// emitJSON は v を indent 付き JSON + 改行で書く。
func emitJSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// ─── list / search ───────────────────────────────────────────

// cliListView は MCP listOut と同じ compact shape(--json 用)。
type cliListView struct {
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

func listPayload(projects []*project.Project, now time.Time) map[string]any {
	out := make([]cliListView, 0, len(projects))
	for _, p := range projects {
		out = append(out, cliListView{
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
	return map[string]any{"count": len(out), "projects": out}
}

func humanList(w io.Writer, projects []*project.Project, now time.Time) {
	if len(projects) == 0 {
		fmt.Fprintln(w, "count: 0")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SLUG\tSTAGE\tPRI\tLANG\tCI\tPRS\tISSUES\tIDLE\tREPO")
	for _, p := range projects {
		idle := project.DaysSince(p.LatestActivity, now)
		idleStr := "-"
		if idle >= 0 {
			idleStr = fmt.Sprintf("%dd", idle)
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%d\t%d\t%s\t%s\n",
			p.Slug, p.Stage, p.Priority, dash(p.Language), dash(string(p.CIStatus)),
			p.OpenPRs, p.OpenIssues, idleStr, p.Repository)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "count: %d\n", len(projects))
}

// ─── get ─────────────────────────────────────────────────────

func humanProject(w io.Writer, p *project.Project) {
	tw := tabwriter.NewWriter(w, 0, 2, 1, ' ', 0)
	kv := func(k string, v any) { fmt.Fprintf(tw, "%s:\t%v\n", k, v) }
	kv("slug", p.Slug)
	kv("display_name", p.DisplayName)
	kv("repository", p.Repository)
	if p.LocalPath != "" {
		kv("local_path", p.LocalPath)
	}
	if p.Language != "" {
		kv("language", p.Language)
	}
	kv("stage", p.Stage)
	kv("priority", p.Priority)
	if len(p.Tags) > 0 {
		kv("tags", strings.Join(p.Tags, ", "))
	}
	if len(p.DependsOn) > 0 {
		kv("depends_on", strings.Join(p.DependsOn, ", "))
	}
	if p.CIStatus != "" {
		kv("ci_status", p.CIStatus)
	}
	if p.LatestVersion != "" {
		kv("latest_version", p.LatestVersion)
	}
	kv("open_prs", p.OpenPRs)
	kv("open_issues", p.OpenIssues)
	if p.Notes != "" {
		kv("notes", p.Notes)
	}
	if !p.LatestActivity.IsZero() {
		kv("latest_activity", p.LatestActivity.Format(time.RFC3339))
	}
	kv("created_at", p.CreatedAt.Format(time.RFC3339))
	kv("updated_at", p.UpdatedAt.Format(time.RFC3339))
	_ = tw.Flush()
}

// ─── stats ───────────────────────────────────────────────────

// statsView は MCP yagura_stats と同じ集計 shape(--json 用)。
type statsView struct {
	Total            int            `json:"total"`
	ByStage          map[string]int `json:"by_stage"`
	ByCIStatus       map[string]int `json:"by_ci_status"`
	ByLanguage       map[string]int `json:"by_language"`
	TotalOpenPRs     int            `json:"total_open_prs"`
	TotalOpenIssues  int            `json:"total_open_issues"`
	StaleActiveCount int            `json:"stale_active_count"`
	WithActiveSprint int            `json:"with_active_sprint"`
	AvgPriority      float64        `json:"avg_priority"`
	AsOf             string         `json:"as_of"`
}

func computeStats(projects []*project.Project, now time.Time) statsView {
	st := statsView{
		ByStage:    map[string]int{},
		ByCIStatus: map[string]int{},
		ByLanguage: map[string]int{},
		Total:      len(projects),
		AsOf:       now.Format(time.RFC3339),
	}
	var prioritySum, priorityN int
	for _, p := range projects {
		st.ByStage[string(p.Stage)]++
		ciKey := string(p.CIStatus)
		if ciKey == "" {
			ciKey = "unknown"
		}
		st.ByCIStatus[ciKey]++
		if p.Language != "" {
			st.ByLanguage[p.Language]++
		}
		st.TotalOpenPRs += p.OpenPRs
		st.TotalOpenIssues += p.OpenIssues
		if p.Sprint != nil {
			st.WithActiveSprint++
		}
		if p.Priority > 0 {
			prioritySum += p.Priority
			priorityN++
		}
		if p.Stage == project.StageActive && !p.LatestActivity.IsZero() &&
			int(now.Sub(p.LatestActivity).Hours()/24) >= 14 {
			st.StaleActiveCount++
		}
	}
	if priorityN > 0 {
		st.AvgPriority = float64(prioritySum) / float64(priorityN)
	}
	return st
}

func humanStats(w io.Writer, st statsView) {
	fmt.Fprintf(w, "total: %d\n", st.Total)
	fmt.Fprintf(w, "open PRs: %d   open issues: %d   stale active: %d   avg priority: %.2f\n",
		st.TotalOpenPRs, st.TotalOpenIssues, st.StaleActiveCount, st.AvgPriority)
	printCountMap(w, "by stage", st.ByStage)
	printCountMap(w, "by ci_status", st.ByCIStatus)
	printCountMap(w, "by language", st.ByLanguage)
}

func printCountMap(w io.Writer, title string, m map[string]int) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(w, "%s:\n", title)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, k := range keys {
		fmt.Fprintf(tw, "  %s\t%d\n", k, m[k])
	}
	_ = tw.Flush()
}

// ─── secretscan ──────────────────────────────────────────────

func humanSecretScan(w io.Writer, r secretscan.BatchResult, scanned, sources int) {
	fmt.Fprintf(w, "scanned_projects: %d   sources_scanned: %d   total_findings: %d\n",
		scanned, sources, r.Total)
	if r.Total == 0 {
		return
	}
	printCountMap(w, "by severity", r.BySeverity)
	for _, src := range r.SourceOrder {
		for _, f := range r.BySource[src] {
			fmt.Fprintf(w, "  [%s] %s  %s  (entropy %.2f)\n", f.Severity, src, f.RuleID, f.Entropy)
		}
	}
}

// ─── sbom ────────────────────────────────────────────────────

func humanSbom(w io.Writer, b *sbom.Bom) {
	humanSbomSummary(w, b.Summarize())
	if len(b.Components) == 0 {
		return
	}
	fmt.Fprintln(w, "components:")
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, c := range b.Components {
		fmt.Fprintf(tw, "  %s\t%s\n", c.Name, c.Version)
	}
	_ = tw.Flush()
}

func humanSbomSummary(w io.Writer, s sbom.Summary) {
	fmt.Fprintf(w, "%s %s (%s)\n", dash(s.Application), s.Version, dash(s.GoVersion))
	fmt.Fprintf(w, "spec: CycloneDX %s   components: %d   generated_at: %s\n",
		s.SpecVersion, s.TotalComponents, s.GeneratedAt)
}

// ─── gha-audit ───────────────────────────────────────────────

func humanGhaAudit(w io.Writer, results map[string][]ghaaudit.Finding) {
	s := ghaaudit.Summarize(results)
	humanGhaSummary(w, s)
	files := make([]string, 0, len(results))
	for f := range results {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		for _, fn := range results[f] {
			fmt.Fprintf(w, "  [%s] %s:%d  %s  %s\n", fn.Severity, f, fn.Line, fn.RuleID, fn.Description)
		}
	}
}

func humanGhaSummary(w io.Writer, s ghaaudit.Summary) {
	fmt.Fprintf(w, "files: %d   findings: %d\n", s.TotalFiles, s.TotalFindings)
	printCountMap(w, "by severity", s.BySeverity)
}

// ─── pin-drift ───────────────────────────────────────────────

func humanPinDrift(w io.Writer, results []pindrift.Result) {
	s := pindrift.Summarize(results)
	fmt.Fprintf(w, "total_pins: %d\n", s.TotalPins)
	printCountMap(w, "by status", s.ByStatus)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, r := range results {
		fmt.Fprintf(tw, "  %s\t%s/%s@%s\t%s\n",
			r.Status, r.Pin.Owner, r.Pin.Repo, shortSHA(r.Pin.PinnedSHA), r.Detail)
	}
	_ = tw.Flush()
}

// ─── skill-audit ─────────────────────────────────────────────

func humanSkillAudit(w io.Writer, entries []skillAuditEntry, scanned, retireCount int) {
	fmt.Fprintf(w, "scanned: %d   retire_candidates: %d\n", scanned, retireCount)
	if len(entries) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tRETIRE\tPATH")
	for _, e := range entries {
		retire := "-"
		if e.RetireRecommended {
			retire = "RETIRE"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\n", e.Score, retire, e.Path)
	}
	_ = tw.Flush()
	// retire 候補は理由も出す(人間が判断できるように)
	for _, e := range entries {
		if e.RetireRecommended {
			fmt.Fprintf(w, "  %s: %s\n", e.Path, e.RetireReason)
		}
	}
}

// ─── workflow-audit ──────────────────────────────────────────

func humanWorkflowAudit(w io.Writer, entries []workflowAuditEntry, scanned, flagged int) {
	fmt.Fprintf(w, "scanned: %d   flagged: %d\n", scanned, flagged)
	if len(entries) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tAGENTS\tSHAPE\tPATH")
	for _, e := range entries {
		fmt.Fprintf(tw, "%d\t%d\t%s\t%s\n", e.Score, e.AgentCalls, workflowShape(e.WorkflowAuditResult), e.Path)
	}
	_ = tw.Flush()
	// issue は path ごとに列挙(人間が判断・修正できるように)。
	for _, e := range entries {
		for _, iss := range e.Issues {
			fmt.Fprintf(w, "  %s: %s\n", e.Path, iss)
		}
	}
}

// workflowShape は orchestration の形を短い tag で要約する(parallel/pipeline/loop)。
func workflowShape(r harness.WorkflowAuditResult) string {
	var parts []string
	if r.UsesParallel {
		parts = append(parts, "parallel")
	}
	if r.UsesPipeline {
		parts = append(parts, "pipeline")
	}
	if r.HasLoop {
		parts = append(parts, "loop")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "+")
}

// ─── settings-audit ──────────────────────────────────────────

func humanSettingsAudit(w io.Writer, entries []settingsAuditEntry, scanned, flagged int) {
	fmt.Fprintf(w, "scanned: %d   flagged: %d\n", scanned, flagged)
	if len(entries) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tDENY\tHOOKS\tPATH")
	for _, e := range entries {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", e.Score, yesNo(e.HasDenyList), yesNo(e.HasHooks), e.Path)
	}
	_ = tw.Flush()
	// issue は path ごとに列挙(人間が判断・修正できるように)。
	for _, e := range entries {
		for _, iss := range e.Issues {
			fmt.Fprintf(w, "  %s: %s\n", e.Path, iss)
		}
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// ─── agent-config-audit ──────────────────────────────────────

func humanAgentConfigAudit(w io.Writer, e agentConfigAuditEntry, flagged int) {
	fmt.Fprintf(w, "scanned: 1   flagged: %d\n", flagged)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tPROVIDERS\tMODELS\tPRIMARY_OK\tPATH")
	fmt.Fprintf(tw, "%d\t%d\t%d\t%s\t%s\n",
		e.Score, e.ProviderCount, e.ModelCount, yesNo(e.PrimaryResolves), e.Path)
	_ = tw.Flush()
	for _, iss := range e.Issues {
		fmt.Fprintf(w, "  %s: %s\n", e.Path, iss)
	}
}

// ─── plugin-audit ────────────────────────────────────────────

func humanPluginAudit(w io.Writer, e pluginAuditEntry, flagged int) {
	fmt.Fprintf(w, "scanned: 1   flagged: %d\n", flagged)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tKIND\tNAME\tCOMPONENTS\tPATH")
	fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n",
		e.Score, e.Kind, dash(e.Name), dash(strings.Join(e.Components, ",")), e.Path)
	_ = tw.Flush()
	for _, iss := range e.Issues {
		fmt.Fprintf(w, "  %s: %s\n", e.Path, iss)
	}
}

func humanMCPAudit(w io.Writer, e mcpAuditEntry, flagged int) {
	fmt.Fprintf(w, "scanned: 1   flagged: %d\n", flagged)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tKIND\tSERVERS\tTOOLS\tPATH")
	fmt.Fprintf(tw, "%d\t%s\t%d\t%d\t%s\n",
		e.Score, e.Kind, e.ServerCount, e.ToolCount, e.Path)
	_ = tw.Flush()
	for _, iss := range e.Issues {
		fmt.Fprintf(w, "  %s: %s\n", e.Path, iss)
	}
}

// ─── publicity-scan ──────────────────────────────────────────

func humanPublicityScan(w io.Writer, findings []publicityFinding, scanned int) {
	bare := make([]publicityscan.Finding, len(findings))
	for i, f := range findings {
		bare[i] = f.Finding
	}
	s := publicityscan.Summarize(bare)
	fmt.Fprintf(w, "scanned: %d   findings: %d\n", scanned, s.Total)
	printCountMap(w, "by severity", s.BySeverity)
	for _, f := range findings {
		fmt.Fprintf(w, "  [%s] %s:%d  %s  %s\n",
			f.Severity, f.Path, f.Line, f.RuleID, f.Snippet)
	}
}

// ─── vex-audit ───────────────────────────────────────────────

func humanVexAudit(w io.Writer, entries []vexAuditEntry, scanned, flagged int) {
	fmt.Fprintf(w, "scanned: %d   flagged: %d\n", scanned, flagged)
	if scanned == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "OK\tSTMTS\tPATH")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", yesNoMark(e.OK), e.Statements, e.Path)
	}
	_ = tw.Flush()
	for _, e := range entries {
		if e.Error != "" {
			fmt.Fprintf(w, "  %s: parse error: %s\n", e.Path, e.Error)
		}
		for _, iss := range e.Issues {
			fmt.Fprintf(w, "  %s: %s\n", e.Path, iss)
		}
	}
}

func yesNoMark(ok bool) string {
	if ok {
		return "ok"
	}
	return "X"
}

// ─── self-improve-history ────────────────────────────────────

func humanSelfImproveHistory(w io.Writer, recs []audit.Record) {
	fmt.Fprintf(w, "assessments: %d\n", len(recs))
	if len(recs) == 0 {
		fmt.Fprintln(w, "(none recorded — call yagura_self_improve with record=true to start the trajectory)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tHIGH\tMED\tLOW\tPROPOSALS\tSELF")
	var firstHigh, lastHigh int
	for i, r := range recs {
		high, med, low, props := assessmentCounts(r)
		if i == 0 {
			firstHigh = high
		}
		lastHigh = high
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%v\n",
			r.Time.UTC().Format(time.RFC3339), high, med, low, props, fieldBool(r, "self_collected"))
	}
	_ = tw.Flush()
	if len(recs) >= 2 {
		switch {
		case lastHigh < firstHigh:
			fmt.Fprintf(w, "trend: high-severity %d → %d (converging)\n", firstHigh, lastHigh)
		case lastHigh > firstHigh:
			fmt.Fprintf(w, "trend: high-severity %d → %d (regressing — investigate)\n", firstHigh, lastHigh)
		default:
			fmt.Fprintf(w, "trend: high-severity flat at %d\n", lastHigh)
		}
	}
}

// assessmentCounts は self_improve record の Fields から severity 別件数と提案数を取る。
// Fields は JSON 由来なので数値は float64。欠損は 0 として扱う。
func assessmentCounts(r audit.Record) (high, med, low, proposals int) {
	if sev, ok := r.Fields["by_severity"].(map[string]any); ok {
		high = mapNum(sev, "high")
		med = mapNum(sev, "medium")
		low = mapNum(sev, "low")
	}
	if ps, ok := r.Fields["proposals"].([]any); ok {
		proposals = len(ps)
	}
	return
}

func mapNum(m map[string]any, k string) int {
	if f, ok := m[k].(float64); ok {
		return int(f)
	}
	return 0
}

func fieldBool(r audit.Record, k string) bool {
	b, _ := r.Fields[k].(bool)
	return b
}

// ─── path-policy ─────────────────────────────────────────────

func humanPathPolicy(w io.Writer, r pathpolicy.Result) {
	fmt.Fprintf(w, "worst: %s   denied: %d   review: %d   allowed: %d\n",
		r.Worst, len(r.Denied), len(r.Review), r.Allowed)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ACTION\tPATH\tRULE")
	for _, d := range r.Decisions {
		if d.Action == pathpolicy.ActionAllow {
			continue // 既定の allow はノイズなので隠す(deny/review に集中)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", d.Action, d.Path, dash(d.Rule))
	}
	_ = tw.Flush()
	for _, d := range r.Decisions {
		if d.Action != pathpolicy.ActionAllow && d.Reason != "" {
			fmt.Fprintf(w, "  %s: %s\n", d.Path, d.Reason)
		}
	}
}

// ─── inject-scan ─────────────────────────────────────────────

func humanInjectScan(w io.Writer, findings []injectFinding, scanned int) {
	fmt.Fprintf(w, "scanned: %d   signals: %d\n", scanned, len(findings))
	for _, f := range findings {
		fmt.Fprintf(w, "  [%s] %s:%d  %s  %s\n",
			f.Severity, f.Path, f.Line, f.Category, f.Snippet)
	}
}

// ─── cc-security ─────────────────────────────────────────────

func humanCCSecurity(w io.Writer, r ccsecurity.Report) {
	fmt.Fprintf(w, "score: %d/100   checked: %d   passed: %d   warned: %d   failed: %d\n",
		r.Score, r.Checked, r.Passed, r.Warned, r.Failed)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tSEV\tID\tTITLE")
	for _, p := range r.Practices {
		sev := string(p.Severity)
		if sev == "" {
			sev = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", strings.ToUpper(string(p.Status)), sev, p.ID, p.Title)
	}
	_ = tw.Flush()
	// fail / warn は detail と remediation を併記(人間が直せるように)。
	for _, p := range r.Practices {
		if p.Status == ccsecurity.StatusFail || p.Status == ccsecurity.StatusWarn {
			fmt.Fprintf(w, "  %s: %s\n", p.ID, p.Detail)
			if p.Remediation != "" {
				fmt.Fprintf(w, "    → %s\n", p.Remediation)
			}
		}
	}
	// 機械判定できない人手プロセス項目をガイダンスとして提示。
	if len(r.ManualPractices) > 0 {
		fmt.Fprintln(w, "manual checklist (not machine-checked):")
		for _, m := range r.ManualPractices {
			fmt.Fprintf(w, "  [ ] %s  (%s)\n", m.Title, m.ID)
		}
	}
}

// ─── claudemd-audit ──────────────────────────────────────────

func humanClaudeMdAudit(w io.Writer, path string, r harness.ClaudeMdAuditResult) {
	fmt.Fprintf(w, "%s\n", path)
	fmt.Fprintf(w, "score: %d/100   title: %s   sections: %s   instructions: %d\n",
		r.Score, yesNo(r.HasTitle),
		dash(strings.Join(r.SectionsFound, "+")), r.InstructionCount)
	if len(r.MissingSections) > 0 {
		fmt.Fprintf(w, "missing sections: %s\n", strings.Join(r.MissingSections, ", "))
	}
	for _, iss := range r.Issues {
		fmt.Fprintf(w, "  issue: %s\n", iss)
	}
	for _, s := range r.Suggestions {
		fmt.Fprintf(w, "  suggest: %s\n", s)
	}
}

// ─── small helpers ───────────────────────────────────────────

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ─── ai-verify (v0.36.0) ─────────────────────────────────────

func humanAIVerify(w io.Writer, res aiverify.Result, summaryOnly bool) {
	fmt.Fprintf(w, "files_scanned: %d  total_lines: %d  ai_gen_lines: %d\n",
		res.FilesScanned, res.TotalLines, res.AIGenLines)
	fmt.Fprintf(w, "risk_score: %d  has_critical: %v\n", res.RiskScore, res.HasCritical)
	fmt.Fprintf(w, "findings: %d  (CRIT=%d HIGH=%d MED=%d LOW=%d)\n",
		len(res.Findings),
		res.BySeverity[aiverify.RiskCritical],
		res.BySeverity[aiverify.RiskHigh],
		res.BySeverity[aiverify.RiskMedium],
		res.BySeverity[aiverify.RiskLow])
	if len(res.AIGenWithoutTests) > 0 {
		fmt.Fprintf(w, "ai_gen_without_tests: %s\n", strings.Join(res.AIGenWithoutTests, ", "))
	}
	if summaryOnly || len(res.Findings) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RISK\tFILE\tLINE\tRULE\tMESSAGE")
	for _, f := range res.Findings {
		ai := ""
		if f.AIGen {
			ai = "*"
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%d\t%s\t%s\n",
			f.Risk, ai, f.File, f.Line, f.RuleID, f.Message)
	}
	_ = tw.Flush()
}

// ─── quality-check (v0.36.0) ─────────────────────────────────

func humanQualityCheck(w io.Writer, res qualitycheck.Result, summaryOnly bool) {
	fmt.Fprintf(w, "files_scanned: %d  total_lines: %d\n", res.FilesScanned, res.TotalLines)
	fmt.Fprintf(w, "findings: %d  (prohibited=%d warning=%d info=%d)  has_prohibited: %v\n",
		len(res.Findings),
		res.BySeverity[qualitycheck.SevProhibited],
		res.BySeverity[qualitycheck.SevWarning],
		res.BySeverity[qualitycheck.SevInfo],
		res.HasProhibited())
	if summaryOnly || len(res.Findings) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tFILE\tLINE\tRULE\tDESCRIPTION")
	for _, f := range res.Findings {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n",
			f.Severity, f.File, f.Line, f.RuleID, f.Description)
	}
	_ = tw.Flush()
}

// ─── test-audit (v0.36.0) ────────────────────────────────────

func humanTestAudit(w io.Writer, res testcoverage.AuditResult, untestedOnly bool) {
	fmt.Fprintf(w, "files_scanned: %d  source_files: %d  test_files: %d\n",
		res.FilesScanned, res.SourceFiles, res.TestFiles)
	fmt.Fprintf(w, "sources_with_test: %d  sources_no_test: %d  coverage_ratio: %.2f\n",
		res.SourcesWithTest, res.SourcesNoTest, res.CoverageRatio)

	if untestedOnly {
		if len(res.UntestedFiles) == 0 {
			fmt.Fprintln(w, "no untested sources")
			return
		}
		fmt.Fprintln(w, "untested sources:")
		for _, p := range res.UntestedFiles {
			fmt.Fprintf(w, "  %s\n", p)
		}
		return
	}

	if len(res.ByLanguage) > 0 {
		langs := make([]string, 0, len(res.ByLanguage))
		for l := range res.ByLanguage {
			langs = append(langs, l)
		}
		sort.Strings(langs)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "LANGUAGE\tSOURCES\tTESTS\tWITH_TEST\tCOVERAGE")
		for _, l := range langs {
			s := res.ByLanguage[l]
			fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%.2f\n", l, s.Sources, s.Tests, s.WithTest, s.CoverageRatio)
		}
		_ = tw.Flush()
	}
	if len(res.UntestedFiles) > 0 {
		fmt.Fprintf(w, "untested: %s\n", strings.Join(res.UntestedFiles, ", "))
	}
}

// ─── ast-check (v0.36.0, Roadmap #6) ─────────────────────────

func humanASTCheck(w io.Writer, res astcheck.Result) {
	fmt.Fprintf(w, "files_scanned: %d  findings: %d\n", res.FilesScanned, len(res.Findings))
	if len(res.Findings) == 0 {
		return
	}
	printCountMap(w, "by severity", res.BySeverity)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tFILE\tLINE\tRULE\tMESSAGE")
	for _, f := range res.Findings {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", f.Severity, f.File, f.Line, f.Rule, f.Message)
	}
	_ = tw.Flush()
}

func humanCoverage(w io.Writer, r coverage.Report) {
	fmt.Fprintf(w, "coverage_ratio: %.2f   total_files: %d   analyzable: %d   uncovered_source: %d   non_source: %d\n",
		r.CoverageRatio, r.TotalFiles, r.Analyzable, r.UncoveredSource, r.NonSource)
	printCountMap(w, "analyzable by language", r.ByLanguage)
	if len(r.UncoveredByExt) > 0 {
		printCountMap(w, "blind spots (uncovered source, by ext)", r.UncoveredByExt)
	}
}

func humanAssertCheck(w io.Writer, r assertcheck.Report) {
	fmt.Fprintf(w, "test_files: %d   test_funcs: %d   assertions: %d   hollow_files: %d   avg_density: %.2f\n",
		r.TestFiles, r.TotalTestFuncs, r.TotalAssertions, r.HollowFiles, r.AvgDensity)
	if r.HollowFiles == 0 {
		fmt.Fprintln(w, "no hollow test files (all test functions have at least one assertion)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FILE\tTEST_FUNCS\tASSERTIONS\tDENSITY\tSTATUS")
	for _, f := range r.Files {
		status := "ok"
		if f.Hollow {
			status = "HOLLOW"
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%.2f\t%s\n", f.Path, f.TestFuncs, f.Assertions, f.Density, status)
	}
	_ = tw.Flush()
}

func humanCoupling(w io.Writer, r coupling.Report) {
	var violations int
	for _, f := range r.Findings {
		if f.Rule == "sdp-violation" {
			violations++
		}
	}
	fmt.Fprintf(w, "module: %s   packages: %d   sdp_violations: %d\n", r.ModulePath, r.PackageCount, violations)
	// fan-in 降順(チョークポイントを上に)で上位を表示。fan-in 同点は name 昇順。
	pkgs := append([]coupling.Package(nil), r.Packages...)
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].FanIn != pkgs[j].FanIn {
			return pkgs[i].FanIn > pkgs[j].FanIn
		}
		return pkgs[i].Name < pkgs[j].Name
	})
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FAN_IN\tFAN_OUT\tINSTABILITY\tPACKAGE")
	shown := pkgs
	if len(shown) > 15 {
		shown = shown[:15]
	}
	for _, p := range shown {
		fmt.Fprintf(tw, "%d\t%d\t%.2f\t%s\n", p.FanIn, p.FanOut, p.Instability, p.Name)
	}
	_ = tw.Flush()
	if len(pkgs) > 15 {
		fmt.Fprintf(w, "... %d more packages (use --json for the full graph)\n", len(pkgs)-15)
	}
	if violations > 0 {
		fmt.Fprintln(w, "SDP violations (stable package depends on more-unstable one):")
		for _, f := range r.Findings {
			if f.Rule == "sdp-violation" {
				fmt.Fprintf(w, "  %s → %s\n", f.From, f.To)
			}
		}
	}
}

func humanAPIDoc(w io.Writer, r apidoc.Report) {
	fmt.Fprintf(w, "exported: %d   documented: %d   documented_ratio: %.2f\n",
		r.ExportedTotal, r.Documented, r.DocumentedRatio)
	printCountMap(w, "exported by kind", r.ByKind)
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "all exported symbols are documented")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tFILE\tLINE\tNAME")
	shown := r.Findings
	const cap = 25
	if len(shown) > cap {
		shown = shown[:cap]
	}
	for _, f := range shown {
		if f.Rule == "parse-error" {
			fmt.Fprintf(tw, "parse-error\t%s\t%d\t%s\n", f.File, f.Line, f.Message)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", f.Kind, f.File, f.Line, f.Name)
	}
	_ = tw.Flush()
	if len(r.Findings) > cap {
		fmt.Fprintf(w, "... %d more undocumented (use --json for the full list)\n", len(r.Findings)-cap)
	}
}

func humanDeadCode(w io.Writer, r deadcode.Report) {
	fmt.Fprintf(w, "packages: %d   declared_unexported: %d   dead: %d\n",
		r.PackagesScanned, r.DeclaredUnexported, r.Dead)
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "no dead unexported declarations")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tPACKAGE\tFILE\tLINE\tNAME")
	shown := r.Findings
	const cap = 30
	if len(shown) > cap {
		shown = shown[:cap]
	}
	for _, f := range shown {
		if f.Rule == "parse-error" {
			fmt.Fprintf(tw, "parse-error\t%s\t%s\t%d\t%s\n", f.Package, f.File, f.Line, f.Message)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", f.Kind, f.Package, f.File, f.Line, f.Name)
	}
	_ = tw.Flush()
	if len(r.Findings) > cap {
		fmt.Fprintf(w, "... %d more (use --json for the full list)\n", len(r.Findings)-cap)
	}
}

func humanRecvCheck(w io.Writer, r recvcheck.Report) {
	fmt.Fprintf(w, "types_with_methods: %d   findings: %d\n", r.TypesWithMethods, len(r.Findings))
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "all method receivers are consistent")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tRULE\tTYPE\tFILE\tLINE")
	for _, f := range r.Findings {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", f.Severity, f.Rule, f.Type, f.File, f.Line)
	}
	_ = tw.Flush()
}

func humanToday(w io.Writer, now time.Time, items []today.Item) {
	fmt.Fprintf(w, "%s — top %d by score\n", now.Format("2006-01-02"), len(items))
	if len(items) == 0 {
		fmt.Fprintln(w, "no active/maintenance projects")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCORE\tSLUG\tWHY")
	for _, it := range items {
		why := strings.Join(it.Reasons, ", ")
		if why == "" {
			why = "-"
		}
		fmt.Fprintf(tw, "%.0f\t%s\t%s\n", it.Score, it.Slug, why)
	}
	_ = tw.Flush()
}

func humanCodeHealth(w io.Writer, r codehealth.Report) {
	fmt.Fprintf(w, "overall: %s (%d)   packages: %d\n", r.OverallGrade, r.OverallScore, len(r.Packages))
	if len(r.Packages) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "GRADE\tSCORE\tPACKAGE\tTOP ISSUE")
	shown := r.Packages
	const cap = 25
	if len(shown) > cap {
		shown = shown[:cap]
	}
	for _, p := range shown {
		top := "-"
		if len(p.Reasons) > 0 {
			top = p.Reasons[0]
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", p.Grade, p.Score, p.Package, top)
	}
	_ = tw.Flush()
	if len(r.Packages) > cap {
		fmt.Fprintf(w, "... %d more packages (use --json for the full report)\n", len(r.Packages)-cap)
	}
}

func humanComplexity(w io.Writer, r complexity.Report) {
	fmt.Fprintf(w, "files_scanned: %d   functions: %d   max: %d   avg: %.1f   over_threshold(>%d): %d\n",
		r.FilesScanned, len(r.Functions), r.MaxComplexity, r.AvgComplexity, r.Threshold, r.OverThreshold)
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "no functions over the complexity threshold")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tCOMPLEXITY\tFILE\tLINE\tFUNC")
	for _, f := range r.Findings {
		if f.Rule == "parse-error" {
			fmt.Fprintf(tw, "%s\t-\t%s\t%d\t%s\n", f.Severity, f.File, f.Line, f.Message)
			continue
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t%s\n", f.Severity, f.Complexity, f.File, f.Line, f.Func)
	}
	_ = tw.Flush()
}

func humanErrPolicy(w io.Writer, r errpolicy.Report) {
	fmt.Fprintf(w, "files_scanned: %d   wrap_ratio: %.2f   wrapped: %d   naked: %d   blank_discards: %d\n",
		r.FilesScanned, r.WrapRatio, r.WrappedReturns, r.NakedReturns, r.BlankDiscards)
	// Findings は actionable なものだけ(blank-discard / parse-error)。naked は
	// 上の wrap_ratio に集約済みで per-site では出さない(human/JSON で一貫)。
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "no discarded-error / parse issues (naked returns folded into wrap_ratio)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tFILE\tLINE\tRULE\tMESSAGE")
	for _, f := range r.Findings {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", f.Severity, f.File, f.Line, f.Rule, f.Message)
	}
	_ = tw.Flush()
}

func humanFlowRisk(w io.Writer, steps int, risks []flowrisk.FlowRisk) {
	fmt.Fprintf(w, "steps: %d   flows: %d\n", steps, len(risks))
	if len(risks) == 0 {
		fmt.Fprintln(w, "no risky operation sequences detected")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tKIND\tFROM\tTO\tMESSAGE")
	for _, r := range risks {
		fmt.Fprintf(tw, "%s\t%s\t#%d %s\t#%d %s\t%s\n", r.Severity, r.Kind, r.From, r.FromName, r.To, r.ToName, r.Message)
	}
	_ = tw.Flush()
}

func humanDiffScan(w io.Writer, addedLines int, hits []diffSecretHit, guards []diffscan.GuardRemoval) {
	fmt.Fprintf(w, "added_lines: %d   secret_findings: %d   guards_removed: %d\n", addedLines, len(hits), len(guards))
	if len(hits) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SEVERITY\tFILE\tLINE\tRULE")
		for _, h := range hits {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", h.Severity, h.Path, h.Line, h.RuleID)
		}
		_ = tw.Flush()
	} else {
		fmt.Fprintln(w, "no secrets introduced by this change")
	}
	if len(guards) > 0 {
		fmt.Fprintln(w, "guards removed (review — a safety construct was deleted):")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tFILE\tLINE\tTEXT")
		for _, g := range guards {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", g.Kind, g.Path, g.Line, g.Text)
		}
		_ = tw.Flush()
	}
}

func humanReviewGate(w io.Writer, sig reviewgate.Signals, dec reviewgate.Decision) {
	fmt.Fprintf(w, "verdict: %s\n", dec.Tier)
	fmt.Fprintf(w, "signals: secrets=%d ai_risk=%d ai_critical=%d lint_prohibited=%d ast_high=%d\n",
		sig.SecretFindings, sig.AIRiskScore, sig.AICritical, sig.LintProhibited, sig.ASTHigh)
	for _, b := range dec.Blockers {
		fmt.Fprintf(w, "  blocker: %s\n", b)
	}
	for _, r := range dec.Reasons {
		fmt.Fprintf(w, "  %s\n", r)
	}
}

func humanASTSurface(w io.Writer, res astcheck.SurfaceResult) {
	fmt.Fprintf(w, "files_scanned: %d  capabilities: %d\n", res.FilesScanned, len(res.ByCapability))
	if len(res.ByCapability) == 0 {
		fmt.Fprintln(w, "no exec/network/unsafe/reflect/crypto surface detected")
		return
	}
	caps := make([]string, 0, len(res.ByCapability))
	for c := range res.ByCapability {
		caps = append(caps, c)
	}
	sort.Strings(caps)
	for _, c := range caps {
		files := res.ByCapability[c]
		fmt.Fprintf(w, "%s (%d):\n", c, len(files))
		for _, f := range files {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
}

// ─── alert-fix (v0.36.0) ─────────────────────────────────────

func humanAlertFix(w io.Writer, r alertfix.Report) {
	fmt.Fprintln(w, r.Summary())
	if r.Total == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tPROJECT\tSOURCE\tTITLE\tSUGGESTED_TOOL")
	for _, a := range r.Alerts {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			a.Severity, a.Project, a.Source, a.Title, dash(a.SuggestedTool))
	}
	_ = tw.Flush()
}
