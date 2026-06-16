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
	"github.com/shizukutanaka/yagura/internal/atomicfile"
	"github.com/shizukutanaka/yagura/internal/handoff"
	"github.com/shizukutanaka/yagura/internal/harness"
	"github.com/shizukutanaka/yagura/internal/osv"
	"github.com/shizukutanaka/yagura/internal/quotamonitor"
	"github.com/shizukutanaka/yagura/internal/registry"
	"github.com/shizukutanaka/yagura/internal/scorecard"
	"github.com/shizukutanaka/yagura/internal/secretscan"
	"os"
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
	// [G] v0.36.0 — Go AST structural audit (Roadmap #6)
	s.Register(buildASTCheckTool(d))
	// [Q] v0.36.0 — test assertion density (hollow test detection)
	s.Register(buildAssertCheckTool(d))
	// [Q] v0.36.0 — error-context discipline (wrap ratio + blank-discard)
	s.Register(buildErrPolicyTool(d))
	// [Q] v0.36.0 — cyclomatic complexity (testability precondition)
	s.Register(buildComplexityTool(d))
	// [Q] v0.36.0 — package import coupling (architecture / SDP)
	s.Register(buildCouplingTool(d))
	// [Q] v0.36.0 — exported-API doc discipline (public contract)
	s.Register(buildAPIDocTool(d))
	// [Q] v0.36.0 — dead unexported declarations (internal reachability)
	s.Register(buildDeadCodeTool(d))
	// [Q] v0.36.0 — method receiver consistency (self-consistency)
	s.Register(buildRecvCheckTool(d))
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



// atomicWriteFile writes via tmp + fsync + rename so partial writes never
// leave a torn file on disk. Permissions are explicit so callers can request
// 0o755 (executable) for init.sh.
//
// Idempotent — re-running with same content is a no-op visible to callers
// (rename is atomic on POSIX).
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	return atomicfile.Write(path, data, mode)
}
