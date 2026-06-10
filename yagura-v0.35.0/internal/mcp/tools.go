// tools.go: 5 つの初期 MCP tool 実装。
//
// 全 tool は以下の規約に従う:
//   - 入力 args は JSON object として受け取り、型ごとの struct に Unmarshal
//   - 出力は JSON-serializable な map / struct を返す
//   - 失敗時は &ToolError{...} を返す(ユーザ向けメッセージと内部 cause を分離)
//   - registry / scanner / time は依存注入で受け取り、テストしやすくする
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shizukutanaka/yagura/internal/agentmd"
	"github.com/shizukutanaka/yagura/internal/atomicfile"
	"github.com/shizukutanaka/yagura/internal/featurelist"
	"github.com/shizukutanaka/yagura/internal/handoff"
	"github.com/shizukutanaka/yagura/internal/harness"
	"github.com/shizukutanaka/yagura/internal/initps1"
	"github.com/shizukutanaka/yagura/internal/initsh"
	"github.com/shizukutanaka/yagura/internal/osv"
	"github.com/shizukutanaka/yagura/internal/plantracker"
	"github.com/shizukutanaka/yagura/internal/progressfile"
	"github.com/shizukutanaka/yagura/internal/quotamonitor"
	"github.com/shizukutanaka/yagura/internal/registry"
	"github.com/shizukutanaka/yagura/internal/scorecard"
	"github.com/shizukutanaka/yagura/internal/secretscan"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Deps はツールが必要とする依存。
type Deps struct {
	Registry       *registry.Registry
	Now            func() time.Time // テストで時刻固定するためのフック(nil なら time.Now)
	OSV            OSVQuerier       // 脆弱性スキャナ。nil なら yagura_vulns は使用不可エラーを返す
	Scorecard      ScorecardFetcher // 公開 Scorecard 取得。nil なら yagura_scorecard は使用不可
	SecretScanner  SecretScanner    // ローカル secret leak 検出。nil なら yagura_secretscan は使用不可
	Sbom           SbomGenerator    // CycloneDX SBOM 生成器。nil なら yagura_sbom は使用不可
	Ghaaudit       GhaAuditor       // GHA workflow audit 器。nil なら yagura_gha_audit は使用不可
	PinDrift       PinDriftChecker  // SHA pin drift 検証器。nil なら yagura_pin_drift は使用不可
	MainModulePath string           // SBOM 生成時のメイン module path(例: "github.com/shizukutanaka/yagura")
	MainVersion    string           // SBOM 生成時のメイン version(例: "0.9.0")

	// v0.13.0: agent handoff layer
	QuotaMonitor  QuotaMonitor  // 両 agent の quota state 管理。nil なら quota tool は使用不可
	HandoffStore  HandoffStore  // session context 永続化。nil なら session tool は使用不可
	AgentLauncher AgentLauncher // Windsurf / Claude Code 起動。nil なら handoff は dry-run のみ
	WorkspaceRoot string        // 現在の workspace ルートパス(handoff 既定値)
}

// QuotaMonitor は両 agent の quota 状態を管理する interface。
// 実装は internal/quotamonitor.Monitor。
type QuotaMonitor interface {
	Report(agent quotamonitor.Agent, remainingPercent int, source string,
		windowReset, weeklyReset time.Time) error
	Status(agent quotamonitor.Agent) (quotamonitor.AgentStatus, error)
	AllStatuses() map[quotamonitor.Agent]quotamonitor.AgentStatus
	Recommend() (quotamonitor.Agent, string)
	ShouldHandoff(current quotamonitor.Agent) (bool, quotamonitor.Agent, string)
	MarkSwitched(agent quotamonitor.Agent) error
	// v0.14.0: heartbeat protocol
	RecordHeartbeat(agent quotamonitor.Agent) error
	IsStale(agent quotamonitor.Agent, idleTimeout time.Duration) (bool, time.Duration)
	AnyStale(idleTimeout time.Duration) []quotamonitor.Agent
	// v0.15.0: quota forecasting
	Forecast(agent quotamonitor.Agent) quotamonitor.ForecastResult
	// v0.16.0: usage summaries(per-agent + both)
	UsageSummary(agent quotamonitor.Agent) quotamonitor.UsageSummary
	AllUsageSummaries() map[quotamonitor.Agent]quotamonitor.UsageSummary
}

// HandoffStore は handoff context の永続化 interface。
// 実装は internal/handoff.Store。
type HandoffStore interface {
	Save(ctx *handoff.Context) error
	Load() (*handoff.Context, error)
	Clear() error
	Path() string
}

// AgentLauncher は agent process 起動 interface。
// 実装は internal/agentlauncher.Launcher。
type AgentLauncher interface {
	LaunchWindsurf(ctx context.Context, workspaceDir string) error
	LaunchClaudeCode(ctx context.Context, workspaceDir string) error
	LastCommand() (string, []string)
}

// OSVQuerier は OSV.dev 風 API への問合せインターフェース。
// 実装は internal/osv.Client。テストでは mock を差し込む。
type OSVQuerier interface {
	Query(ctx context.Context, ecosystem, pkg, version string) ([]osv.Vuln, error)
}

// ScorecardFetcher は OpenSSF Scorecard API への問合せインターフェース。
// 実装は internal/scorecard.Client。
type ScorecardFetcher interface {
	Fetch(ctx context.Context, repo string) (*scorecard.Score, error)
}

// SecretScanner は secret leak 検出インターフェース。
// 実装は internal/secretscan.Scanner。
type SecretScanner interface {
	ScanBatch(items []secretscan.ScanItem) secretscan.BatchResult
}

// RegisterDefaultTools は 38 つの初期 tool を server に登録する。
//
// v0.18.0 (cortex/aircloset の Harness Engineering を参考):
// 各 tool description は [G] (Guides: 事前制御 = context 供給 / 逸脱防止) または
// [S] (Sensors: 事後制御 = 観測 / 修正) で分類される。Fowler 2026 の枠組み準拠。
func RegisterDefaultTools(s *Server, d Deps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	// [G] Guides — registry context provision
	s.Register(buildListTool(d))
	s.Register(buildGetTool(d))
	s.Register(buildSearchTool(d))
	s.Register(buildTodayTool(d))
	s.Register(buildRegisterTool(d))
	s.Register(buildUnregisterTool(d))
	s.Register(buildUpdateTool(d))
	s.Register(buildStatsTool(d))
	// [G] Guides — quality / supply chain audit (pre-emptive)
	s.Register(buildSecretScanTool(d))
	s.Register(buildSbomTool(d))
	s.Register(buildGhaAuditTool(d))
	s.Register(buildPinDriftTool(d))
	// [S] Sensors — vulnerability / security health (observe)
	s.Register(buildVulnsTool(d))
	s.Register(buildScorecardTool(d))
	s.Register(buildHealthTool(d))
	// [S] Sensors — v0.13.0+ agent handoff layer
	s.Register(buildQuotaReportTool(d))
	s.Register(buildAgentStatusTool(d))
	s.Register(buildSessionSaveTool(d))
	s.Register(buildSessionLoadTool(d))
	s.Register(buildHandoffTool(d))
	s.Register(buildHeartbeatTool(d))
	s.Register(buildQuotaForecastTool(d))
	s.Register(buildUsageSummaryTool(d))
	// [S] Sensors — v0.17.0 self-referential measurement
	s.Register(buildTokenStatsTool(s.AllToolStats))
	// [G] Guides — v0.18.0 Product Graph(cortex/aircloset 参考)
	s.Register(buildGraphNeighborsTool(d))
	s.Register(buildGraphImpactTool(d))
	s.Register(buildGraphStatsTool(d))
	// [G] Guides — v0.19.0 Claude Code Harness scaffolding
	s.Register(buildHarnessRecommendTool(d))
	s.Register(buildSkillAuditTool(d))
	s.Register(buildSubagentAuditTool(d))
	// [G] Guides — v0.19.0 quality gate enforcement(cortex 流「逸脱を物理的に潰す」)
	s.Register(buildQualityCheckTool(d, s.cache))
	// [G] v0.22.0 — Tools catalog for compact mode補完
	s.Register(buildToolsCatalogTool(s))
	// [S] v0.23.0 — dedupe cache stats
	s.Register(buildDedupeStatsTool(s))
	// [G][S] v0.24.0 — Plan.md aware portfolio orchestration
	s.Register(buildPlanStatusTool(d, s.cache))
	s.Register(buildReleaseRadarTool(d, s.cache))
	// [G] v0.25.0 — AI code verifier (m's harness G0.7 直接対応)
	s.Register(buildAIVerifyTool(d, s.cache))
	// [G] v0.26.0 — test coverage detection (m's G0.7 直接対応)
	s.Register(buildTestAuditTool(d))
	// [S] v0.27.0 — cortex flywheel ④ Alert-Fix (rule-based recommendation hub)
	s.Register(buildAlertFixTool(d, s.cache, s.alertStore))
	// [G] v0.30.0 — alert lifecycle (resolve/snooze/reopen)
	s.Register(buildAlertResolveTool(s.alertStore))
	// [G] v0.32.0 — bilateral harness (guides side)
	s.Register(buildAgentsMdTool(d))
	s.Register(buildFeatureListTool(d))
	s.Register(buildHarnessCoverageTool(d))
	// [v0.33.0] closing the loop
	s.Register(buildHookTimelineTool(s))
	s.Register(buildHookStatsTool(s))
	s.Register(buildProgressFileTool(d, s))
	s.Register(buildInitShTool(d))
	// [v0.35.0] 複数 AI を使った処理の並列化(deterministic dispatch planner)
	s.Register(buildParallelPlanTool(d))
	// [v0.35.0] Cyber Risk Reasoning Layer(複合判断による脆弱性 triage)
	s.Register(buildRiskTriageTool(d))
	// [v0.35.0] .claude/ artifact 監査を MCP からも(content-based)
	s.Register(buildWorkflowAuditTool(d))
	s.Register(buildSettingsAuditTool(d))
	s.Register(buildAgentConfigAuditTool(d))
	s.Register(buildPluginAuditTool(d))
	s.Register(buildPublicityScanTool(d))
	s.Register(buildMCPAuditTool(d))
	// [v0.35.0] self-healing orchestration の決定論的 recovery 判断
	s.Register(buildRecoveryDecideTool(d))
	// [v0.35.0] OpenVEX v0.2.0 文書の決定論的生成 + 構造 lint(SBOM の過剰報告を補完)
	s.Register(buildVEXTool(d))
	// [v0.35.0] harness レベル再帰的自己改善(RSI)の決定論的カーネル(STOP/Darwin Gödel)。
	// tools 省略時は s.AllToolStats で live メトリクスを自己収集してループを閉じる。
	// record=true なら s.emitAudit で自己評価を append-only 監査証跡に残す(memories auditable)。
	s.Register(buildSelfImproveTool(d, s.AllToolStats, s.emitAudit))
	// [v0.35.0] 変更パスを glob ルールで gate する deterministic guardrail
	s.Register(buildPathPolicyTool(d))
	// [v0.35.0] 操作の段階的自律性(autonomy tier)を決定論的に分類(Zenn governance 調査)
	s.Register(buildOpsRiskTool(d))
	// [v0.35.0] untrusted content の間接プロンプトインジェクション検出(多言語、S4.3)
	s.Register(buildInjectScanTool(d))
	// [v0.35.0] 任意エージェントの lifecycle イベントを OTel GenAI semconv へ正規化(汎用化)
	s.Register(buildAgentEventTool(d))
	// [v0.35.0] エージェントセッションの構造化 tool-call サマリ(Hermes Desktop 参照)
	s.Register(buildSessionSummaryTool(d, s))
}

// ─── yagura_token_stats (v0.17.0) ────────────────────────────

// buildTokenStatsTool は MCP tool 呼出の累積 byte 数を返す。
// caller は Server 自身を Stats 取得元として渡す必要があるが、
// tool 構築時に server reference を持っていないので、Deps 経由で
// 外から注入する設計にする。
func buildTokenStatsTool(getStats func() []ToolStats) *Tool {
	return &Tool{
		Name:        "yagura_token_stats",
		Description: "[S] Per-tool byte counts since daemon start.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if getStats == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "stats provider not configured"}
			}
			all := getStats()
			// 集計
			var totalCalls, totalReq, totalResp, totalErr uint64
			for _, s := range all {
				totalCalls += s.Calls
				totalReq += s.RequestBytes
				totalResp += s.ResponseBytes
				totalErr += s.ErrorCount
			}
			return map[string]any{
				"per_tool": all,
				"totals": map[string]any{
					"calls":          totalCalls,
					"request_bytes":  totalReq,
					"response_bytes": totalResp,
					"errors":         totalErr,
				},
			}, nil
		},
	}
}

// ─── yagura_harness_recommend / skill_audit / subagent_audit (v0.19.0) ─

func buildHarnessRecommendTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_harness_recommend",
		Description: "[G] Claude Code .claude/ scaffold by slug or language.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":     map[string]any{"type": "string", "description": "project slug (looks up language from registry)"},
				"language": map[string]any{"type": "string", "description": "language override (go/typescript/python/rust/...)"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug     string `json:"slug"`
				Language string `json:"language"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			lang := in.Language
			if lang == "" && in.Slug != "" {
				p, err := d.Registry.Get(in.Slug)
				if err == nil && p != nil {
					lang = p.Language
				}
			}
			if lang == "" {
				return nil, &ToolError{Code: "invalid_input",
					Message: "either slug (with registered language) or language must be provided"}
			}
			return harness.RecommendForLanguage(lang), nil
		},
	}
}

func buildSkillAuditTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_skill_audit",
		Description: "[G] SKILL.md audit: trigger, Gotchas, length. 0-100 score + retire signal.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string", "description": "full SKILL.md text including frontmatter"},
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
			return harness.AuditSkill(in.Content), nil
		},
	}
}

func buildSubagentAuditTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_subagent_audit",
		Description: "[G] Subagent .md audit: prompt style, tools, description.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string", "description": "full subagent .md text including frontmatter"},
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
			return harness.AuditSubagent(in.Content), nil
		},
	}
}

// ─── yagura_tools_catalog (v0.22.0) ──────────────────────────
//
// Compact mode の補完: 短縮 description で意味が掴めない場合の lazy detail fetch。
// cortex の Product Graph 哲学 — 必要なときだけ詳細を返す — を tool layer に適用。

func buildToolsCatalogTool(s *Server) *Tool {
	return &Tool{
		Name:        "yagura_tools_catalog",
		Description: "[G] Full tool details lookup. Use when compact mode hides info you need.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":  map[string]any{"type": "string"},
				"query": map[string]any{"type": "string"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Name  string `json:"name"`
				Query string `json:"query"`
			}
			_ = json.Unmarshal(args, &in)

			s.mu.RLock()
			defer s.mu.RUnlock()

			// 単一 tool 名指定
			if in.Name != "" {
				t, ok := s.tools[in.Name]
				if !ok {
					return nil, &ToolError{Code: "not_found",
						Message: "tool not found: " + in.Name}
				}
				return map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"inputSchema": t.InputSchema,
				}, nil
			}

			// query 検索(name と description の case-insensitive 部分一致)
			q := strings.ToLower(in.Query)
			matches := []map[string]any{}
			for _, t := range s.tools {
				if q != "" && !strings.Contains(strings.ToLower(t.Name), q) &&
					!strings.Contains(strings.ToLower(t.Description), q) {
					continue
				}
				matches = append(matches, map[string]any{
					"name":        t.Name,
					"description": t.Description,
				})
			}
			sort.Slice(matches, func(i, j int) bool {
				return matches[i]["name"].(string) < matches[j]["name"].(string)
			})
			return map[string]any{
				"matches": matches,
				"count":   len(matches),
			}, nil
		},
	}
}

// ─── yagura_dedupe_stats (v0.23.0) ───────────────────────────
//
// Cache 利用状況の可視化。同じ source content を 2 回 scan した時の savings 計測。

func buildDedupeStatsTool(s *Server) *Tool {
	return &Tool{
		Name:        "yagura_dedupe_stats",
		Description: "[S] Content cache stats: hits/misses/bytes saved. Visualizes redundant-read prevention.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			return s.CacheStats(), nil
		},
	}
}


// ─── v0.32.0: Bilateral harness — guides (feedforward) ────────────
//
// Martin Fowler's harness taxonomy: Computational × Inferential × Guide ×
// Sensor. yagura v0.31 had 8 sensors but 0 guides (feedback-only). These
// tools add the missing Inferential Guide axis.

func buildAgentsMdTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_agents_md",
		Description: "[G] Generate AGENTS.md for a registered project from Plan.md + registry facts. Cross-tool: Claude Code / Codex / Cursor.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":          map[string]any{"type": "string"},
				"include_rules": map[string]any{"type": "boolean", "description": "Include house rules section (default true)"},
				"write":         map[string]any{"type": "boolean", "description": "Also write to {local_path}/AGENTS.md (v0.33.0)"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug         string `json:"slug"`
				IncludeRules *bool  `json:"include_rules"`
				Write        bool   `json:"write"`
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
			facts := agentmd.ProjectFacts{
				Slug:         p.Slug,
				DisplayName:  p.DisplayName,
				Repository:   p.Repository,
				Language:     p.Language,
				Stage:        string(p.Stage),
				LocalPath:    p.LocalPath,
				Tags:         p.Tags,
				DependsOn:    p.DependsOn,
				CIStatus:     string(p.CIStatus),
				OpenIssues:   p.OpenIssues,
				OpenPRs:      p.OpenPRs,
				VulnCritical: p.VulnCritical,
				VulnHigh:     p.VulnHigh,
				GeneratedBy:  "yagura " + version(),
			}
			// Plan.md があれば parse して description/phases を埋める
			// (DoD / Purpose / Scope の細粒度抽出は plantracker 拡張待ち)
			if p.LocalPath != "" {
				if content, _, err := loadPlanMd(p.LocalPath); err == nil {
					state := plantracker.Parse(content)
					// plantracker は全 ## を Phase とみなすので、
					// "フェーズ" / "Phase" header 配下の項目のみを真の phase
					// として扱う。それ以外は description/scope/DoD で拾う。
					for _, ph := range state.Phases {
						lower := strings.ToLower(ph.Name)
						if !strings.Contains(lower, "phase") &&
							!strings.Contains(ph.Name, "フェーズ") {
							continue
						}
						facts.Phases = append(facts.Phases,
							fmt.Sprintf("%s (%d/%d)", ph.Name, ph.CompletedTasks, ph.TotalTasks))
					}
					facts.Description = extractSection(content, []string{"目的", "Purpose"})
					facts.Scope = extractSection(content, []string{"スコープ", "Scope"})
					facts.DoD = extractDoDItems(content)
				}
			}
			body := agentmd.Generate(facts)
			result := map[string]any{
				"slug":     in.Slug,
				"body":     body,
				"length":   len(body),
				"filename": "AGENTS.md",
			}
			if in.Write && p.LocalPath != "" {
				path := filepath.Join(p.LocalPath, "AGENTS.md")
				if err := atomicWriteFile(path, []byte(body), 0o644); err != nil {
					return nil, &ToolError{Code: "write_failed", Message: err.Error()}
				}
				result["written_to"] = path
			}
			return result, nil
		},
	}
}

func buildFeatureListTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_feature_list",
		Description: "[G] Convert Plan.md into Anthropic-style feature-list.json for long-running agent harnesses.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":  map[string]any{"type": "string"},
				"write": map[string]any{"type": "boolean", "description": "Also write to {local_path}/feature-list.json (v0.33.0)"},
			},
			"required": []string{"slug"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug  string `json:"slug"`
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
			if p.LocalPath == "" {
				return nil, &ToolError{Code: "invalid_input", Message: "project has no local_path; cannot read Plan.md"}
			}
			content, _, err := loadPlanMd(p.LocalPath)
			if err != nil {
				return nil, &ToolError{Code: "not_found", Message: "Plan.md not found", Cause: err}
			}
			state := plantracker.Parse(content)
			pin := planStateToFeatureInput(in.Slug, content, state)
			fl := featurelist.Build(pin, nil)
			result := map[string]any{
				"slug":         in.Slug,
				"feature_list": fl,
				"stats":        fl.Stats,
				"filename":     "feature-list.json",
			}
			if in.Write {
				raw, err := featurelist.Marshal(fl)
				if err != nil {
					return nil, &ToolError{Code: "write_failed", Message: err.Error()}
				}
				path := filepath.Join(p.LocalPath, "feature-list.json")
				if err := atomicWriteFile(path, raw, 0o644); err != nil {
					return nil, &ToolError{Code: "write_failed", Message: err.Error()}
				}
				result["written_to"] = path
			}
			return result, nil
		},
	}
}

// ─── v0.32.0: Harness coverage self-audit ────────────────────────
//
// Fowler taxonomy: report which Computational × Inferential × Guide × Sensor
// quadrants yagura covers for this portfolio. Surfaces the formerly hidden
// "yagura has 0 guides" gap — and confirms it's resolved.

func buildHarnessCoverageTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_harness_coverage",
		Description: "[G] Self-audit: which Fowler taxonomy quadrants does yagura cover? Returns guides/sensors × computational/inferential matrix.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			// 自分自身が提供する harness 要素を列挙する。
			// 既存 MCP tool を taxonomy にマップ:
			matrix := map[string]map[string][]string{
				"guide": {
					"computational": {
						"yagura_feature_list (Plan.md → feature-list.json scaffold)",
					},
					"inferential": {
						"yagura_agents_md (AGENTS.md scaffold for Claude Code/Codex/Cursor)",
						"yagura_harness_recommend (per-project guidance)",
						"yagura_skill_audit / yagura_subagent_audit (skill scaffolding)",
					},
				},
				"sensor": {
					"computational": {
						"yagura_quality_check (static code analysis)",
						"yagura_secretscan (secret detection)",
						"yagura_gha_audit (workflow audit)",
						"yagura_pin_drift (dep pin drift)",
						"yagura_ai_verify (AI-generated code patterns)",
						"yagura_test_audit (source-test coverage)",
						"yagura_vulns (OSV.dev)",
						"yagura_scorecard (OpenSSF)",
						"yagura_sbom (CycloneDX)",
					},
					"inferential": {
						"(intentionally none — ADR-0001 zero-dep precludes LLM-as-judge in-process)",
					},
				},
			}
			counts := map[string]int{}
			for axis, ci := range matrix {
				for kind, tools := range ci {
					counts[axis+"_"+kind] = len(tools)
				}
			}
			return map[string]any{
				"taxonomy_source":       "https://martinfowler.com/articles/harness-engineering.html",
				"matrix":                matrix,
				"counts":                counts,
				"feedback_only_warning": false, // v0.32 で解消
				"note":                  "yagura intentionally leaves the inferential-sensor quadrant empty (LLM-as-judge would violate ADR-0001 zero-deps). External Claude/GPT review can be plugged in via Claude Code subagents and reported back via /hooks/claude-code.",
			}, nil
		},
	}
}

// version returns the running yagura version. main.go reflects its own
// version into a package-level var via SetVersion at init.
var serverVersion = "unknown"

// SetVersion is called from main() to inject the build version.
// Allows internal packages to render provenance without a circular import.
func SetVersion(v string) {
	serverVersion = v
}

func version() string {
	return serverVersion
}

// extractSection は Plan.md の本文から `## <header>` 直下の段落(空行か次の
// ## まで)を抜き出す。複数候補があれば最初に見つかった方を返す。
//
// v0.32 で agentmd が Purpose/Scope を埋めるために必要。plantracker は
// section の有無 (HasPurpose/HasScope) のみ持ち、本文は持っていなかった。
func extractSection(content string, headers []string) string {
	lines := strings.Split(content, "\n")
	for _, h := range headers {
		for i, line := range lines {
			ts := strings.TrimSpace(line)
			if !strings.HasPrefix(ts, "##") {
				continue
			}
			rest := strings.TrimSpace(strings.TrimLeft(ts, "#"))
			if !strings.EqualFold(rest, h) {
				continue
			}
			// 次の ## まで集める
			var body []string
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "##") {
					break
				}
				body = append(body, lines[j])
			}
			out := strings.TrimSpace(strings.Join(body, "\n"))
			if out != "" {
				return out
			}
		}
	}
	return ""
}

// extractDoDItems は DoD / 完了定義 / Definition of Done section の bullet を
// list で返す。featurelist の acceptance_criteria + agentmd の DoD 表示に使う。
func extractDoDItems(content string) []string {
	body := extractSection(content, []string{"完了定義", "Definition of Done", "DoD"})
	if body == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(body, "\n") {
		ts := strings.TrimSpace(line)
		// "- [ ] xxx", "- [x] xxx", "- xxx" 全て対象
		if !strings.HasPrefix(ts, "- ") && !strings.HasPrefix(ts, "* ") {
			continue
		}
		item := strings.TrimSpace(ts[2:])
		// checkbox を除去
		if strings.HasPrefix(item, "[ ]") || strings.HasPrefix(item, "[x]") || strings.HasPrefix(item, "[X]") {
			item = strings.TrimSpace(item[3:])
		}
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// planStateToFeatureInput は plantracker.PlanState + 元 content から
// featurelist.PlanInput を組み立てる。
//
// plantracker は phase 単位の集計のみ持ち、個別 task title は捨てているので、
// content を再 scan して checkbox 行を拾う。
//
// "フェーズ" / "Phase" header の子のみを feature とみなす(他の section の
// checkbox は features にしない — DoD は acceptance_criteria 側に集約済)。
func planStateToFeatureInput(project, content string, state plantracker.PlanState) featurelist.PlanInput {
	pin := featurelist.PlanInput{
		Project: project,
		DoD:     extractDoDItems(content),
	}
	lines := strings.Split(content, "\n")
	for i, ph := range state.Phases {
		lower := strings.ToLower(ph.Name)
		isPhaseSection := strings.Contains(lower, "phase") || strings.Contains(ph.Name, "フェーズ")
		if !isPhaseSection {
			continue
		}
		startLine := ph.LineStart // 1-indexed (plantracker convention)
		endLine := len(lines)
		if i+1 < len(state.Phases) {
			endLine = state.Phases[i+1].LineStart - 1
		}
		phIn := featurelist.PhaseInput{Name: ph.Name}
		for j := startLine; j < endLine && j < len(lines); j++ {
			ts := strings.TrimSpace(lines[j])
			if !strings.HasPrefix(ts, "- [") && !strings.HasPrefix(ts, "* [") {
				continue
			}
			done := false
			var item string
			if strings.HasPrefix(ts, "- [x]") || strings.HasPrefix(ts, "* [x]") ||
				strings.HasPrefix(ts, "- [X]") || strings.HasPrefix(ts, "* [X]") {
				done = true
				item = strings.TrimSpace(ts[5:])
			} else if strings.HasPrefix(ts, "- [ ]") || strings.HasPrefix(ts, "* [ ]") {
				item = strings.TrimSpace(ts[5:])
			} else {
				continue
			}
			if item == "" {
				continue
			}
			phIn.Tasks = append(phIn.Tasks, featurelist.TaskInput{Title: item, Done: done})
		}
		if len(phIn.Tasks) > 0 {
			pin.Phases = append(pin.Phases, phIn)
		}
	}
	return pin
}

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

// atomicWriteFile writes via tmp + fsync + rename so partial writes never
// leave a torn file on disk. Permissions are explicit so callers can request
// 0o755 (executable) for init.sh.
//
// Idempotent — re-running with same content is a no-op visible to callers
// (rename is atomic on POSIX).
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	return atomicfile.Write(path, data, mode)
}
