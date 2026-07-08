// cmd/yagura は Yagura portfolio orchestrator のエントリポイント。
//
// 起動シーケンス:
//  1. Config 読込(失敗 → exit 1)
//  2. Logger 初期化
//  3. Metrics registry
//  4. Project registry(JSON 永続化、~/.yagura/state/projects/)
//  5. GitHub client
//  6. Scanner 起動(goroutine)
//  7. MCP server + 5 tools 登録
//  8. Dashboard handler
//  9. HTTP server(/healthz /readyz /metrics /mcp /dashboard /debug/pprof)
//
// 10. SIGTERM 受信で graceful shutdown
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shizukutanaka/yagura/internal/agentlauncher"
	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/audit"
	"github.com/shizukutanaka/yagura/internal/config"
	"github.com/shizukutanaka/yagura/internal/dashboard"
	"github.com/shizukutanaka/yagura/internal/ghaaudit"
	"github.com/shizukutanaka/yagura/internal/github"
	"github.com/shizukutanaka/yagura/internal/handoff"
	"github.com/shizukutanaka/yagura/internal/hookreceiver"
	"github.com/shizukutanaka/yagura/internal/httplimit"
	"github.com/shizukutanaka/yagura/internal/logging"
	"github.com/shizukutanaka/yagura/internal/mcp"
	"github.com/shizukutanaka/yagura/internal/metrics"
	"github.com/shizukutanaka/yagura/internal/osv"
	"github.com/shizukutanaka/yagura/internal/pindrift"
	"github.com/shizukutanaka/yagura/internal/promexport"
	"github.com/shizukutanaka/yagura/internal/quotamonitor"
	"github.com/shizukutanaka/yagura/internal/registry"
	"github.com/shizukutanaka/yagura/internal/sbom"
	"github.com/shizukutanaka/yagura/internal/scanner"
	"github.com/shizukutanaka/yagura/internal/scorecard"
	"github.com/shizukutanaka/yagura/internal/secrets"
	"github.com/shizukutanaka/yagura/internal/secretscan"
	"github.com/shizukutanaka/yagura/internal/workspace"
)

const (
	serviceName = "yagura"
	version     = "0.106.0"

	// graceful shutdown 関連
	readyDrainGrace   = 5 * time.Second
	httpShutdownGrace = 10 * time.Second

	// HTTP server timeouts
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 60 * time.Second
	idleTimeout       = 120 * time.Second
)

func main() {
	exitCode := dispatch(os.Args[1:], os.Stdout, os.Stderr)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// dispatch は CLI 引数を解釈し、適切な subcommand を実行する。
// テスト可能化のため main() から分離。標準出力/エラーは引数で受け取る。
//
// 戻り値:
//   - 0: 正常終了(daemon は別パス)
//   - 1: エラー
//
// daemon mode(引数なし、または不明な subcommand)では run() に委譲し、
// blocking する。run() からの戻り値もここで処理する。
func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) >= 1 {
		switch args[0] {
		case "verify":
			if err := verifyAudit(stdout); err != nil {
				fmt.Fprintf(stderr, "yagura verify: %v\n", err)
				return 1
			}
			return 0
		case "version", "-v", "--version":
			fmt.Fprintf(stdout, "yagura %s (%s)\n", version, runtime.Version())
			return 0
		case "help", "-h", "--help":
			fmt.Fprint(stdout, usageText)
			return 0
		case "secret":
			if err := runSecret(args[1:], stdout, stderr); err != nil {
				fmt.Fprintf(stderr, "yagura secret: %v\n", err)
				return 1
			}
			return 0
		}
		// v0.35: CLI direct mode — registry CRUD + local scans without an MCP client.
		if isCLIVerb(args[0]) {
			return runCLI(args[0], args[1:], stdout, stderr)
		}
	}
	if err := run(); err != nil {
		fmt.Fprintf(stderr, "yagura: %v\n", err)
		return 1
	}
	return 0
}

const usageText = `yagura — portfolio orchestrator daemon

Usage:
  yagura                       Start daemon (default)
  yagura verify                Verify integrity of audit log hash chain
  yagura version               Print version and exit
  yagura help                  Print this message
  yagura secret list           List stored secret names
  yagura secret set <name>     Encrypt stdin and store as <name>
  yagura secret get <name>     Decrypt and print <name> to stdout
  yagura secret delete <name>  Remove <name>

CLI direct mode (no MCP client required):
  yagura list|get|search|stats             Registry read ops
  yagura today [--limit N]                 Top portfolio projects to focus on now (priority/PRs/CI/staleness score)
  yagura register|update|unregister        Registry mutations (audited)
  yagura graph <impact|neighbors|stats>    Dependency graph queries over registry depends_on (--json, neighbors --depth N)
  yagura plan-status <slug>                Plan.md progress for a project (checkboxes + required sections)
  yagura release-radar [--limit N]         Cross-project release readiness ranking (Plan/CI/issues/AI-risk; --scan-code for AI)
  yagura ops-risk [--file f]               Classify operation autonomy tier (auto/log/review/human) from JSON ops array
  yagura risk-triage [--file f] [--slug s] Compound CVE/vulnerability prioritization (CVSS+asset+reachability+exploitability)
  yagura recovery-decide --class <cls>     Pick recovery action (retry/replan/escalate) for a failed agent task
  yagura agents-md <slug> [--write]        Generate AGENTS.md from Plan.md + registry facts (cross-tool: Claude Code/Codex/Cursor)
  yagura feature-list <slug> [--write]     Convert Plan.md to Anthropic-style feature-list.json (--write saves to local_path)
  yagura harness-coverage                  Fowler taxonomy self-audit (Computational × Inferential × Guide × Sensor quadrants)
  yagura agent-event [--file f]            Normalize agent lifecycle event (Claude Code/Gemini/Codex/OTel) to OTel GenAI semconv
  yagura init-sh <slug> [--target posix|powershell] [--write]  Generate init.sh or init.ps1 for long-running agent sessions
  yagura progress-file <slug> [--note txt] [--write]  Generate claude-progress.txt for cross-session handoff (Plan.md + registry)
  yagura harness-recommend [--slug s|--language l]  Claude Code .claude/ scaffold by language (CLAUDE.md + settings.json + skills)
  yagura session-summary [--file f]           Aggregate agent event array to structured session summary (tool-calls, errors, anomalies)
  yagura parallel-plan [--file f]             LPT fan-out plan across AI agents (capacity + tier-aware; --json for synthesizer input)
  yagura graph-neighbors <slug> [--depth N]  BFS: direct+transitive deps/dependents
  yagura graph-impact <slug>                 Transitive reverse deps (change impact)
  yagura graph-stats                         Graph summary: nodes/edges/roots/hubs
  yagura sbom|secretscan|gha-audit|pin-drift   Local read-only scans (gha-audit/secretscan/inject-scan/publicity-scan/ast-check support --min-severity)
  yagura skill-audit                       Audit .claude/skills (score + retire)
  yagura workflow-audit                    Audit .claude/workflows (Dynamic Workflow lint)
  yagura settings-audit                    Audit .claude/settings.json (permissions/hooks)
  yagura agent-config-audit [file]         Audit OpenClaw-style openclaw.json (security/reliability)
  yagura plugin-audit [file]               Audit Claude Code plugin.json / marketplace.json
  yagura mcp-audit [file]                  Audit .mcp.json / tools for poisoning & config risk
  yagura publicity-scan [path]             Pre-publish leak scan (home paths, internal hosts, IPs, emails); --min-severity high|medium|low
  yagura vex-audit [dir]                   Validate OpenVEX docs/vex/*.json (--strict for CI)
  yagura self-improve-history              Show the recorded RSI self-assessment trajectory
  yagura path-policy [paths…]              Gate changed paths against .yagura/paths.json (--strict for CI)
  yagura inject-scan [path]                Scan untrusted content for indirect prompt injection (--strict, --min-severity critical|high|medium|low; 'copy .env' is SevMedium — use --min-severity high to skip setup-doc false positives)
  yagura cc-security [dir]                 Audit a project's Claude Code security posture (--min-score for CI)
  yagura completion [bash|zsh|fish]        Generate shell tab-completion script (default: bash)
  yagura claudemd-audit [file]             Audit CLAUDE.md structure (4 sections, instruction budget)
  yagura ai-verify [--dir .]               AI code risk audit (auth/billing/data/crypto/secret; 2x AI-zone multiplier)
  yagura quality-check [--dir .]           Code lint: prohibited patterns, TODO/FIXME, ts-ignore, as any
  yagura test-audit [--dir .] [--strict]   Source-test coverage detection (Go/TS/JS/Python/Rust/Java; --untested-only)
  yagura alert-fix [--severity-min high]   Portfolio health sweep over registry sensor data (resolved/snoozed filtered)
  yagura alert-resolve <id> --action <resolve|snooze|reopen>  Manage alert lifecycle; --snooze-days N (default 7), --note TEXT
  yagura alert-snapshot [--status active|resolved|snoozed]   Show current lifecycle state of all tracked alerts
  yagura ast-check [--dir .]               Go AST structural audit (os.Exit/panic in library, bare goroutine, empty != nil branch, parse errors); --surface for capability profile; --min-severity high|medium|low
  yagura review-gate [--dir .] [--strict] [--gate block|review] Composite ② Review verdict (allow/review/block) over secretscan+aiverify+qualitycheck+astcheck
  yagura diff-scan [--file f] [--strict] [--min-severity critical|high|medium|low]  Delta scan of a unified diff: secrets in ADDED lines (--strict gate) + removed safety guards (review)
  yagura flow-risk [--file f] [--strict] [--min-severity high|medium]  Temporal scan of an op sequence (1 tool/op per line): exfiltration / injection-to-exec / untrusted-to-disk orderings
  yagura coverage [--dir .] [--min R] [--strict]      Scan blind-spot report: how much of the tree is in an analyzable language (covered vs uncovered-source)
  yagura assert-check [--dir .] [--max-hollow F] [--strict]  Test assertion density: detect hollow *_test.go files (zero assertions always pass, proving nothing)
  yagura err-policy [--dir .] [--min-wrap R] [--strict]      Error-context discipline: wrap ratio (fmt.Errorf %w vs naked return err) + blank-discard (_ = call()) detection
  yagura complexity [--dir .] [--max N] [--strict]  Cyclomatic complexity (McCabe, gocyclo-compatible): per-function score, flags functions over --max (default 10) = testability precondition
  yagura param-check [--dir .] [--max N] [--strict]  Long-parameter-list smell (Fowler): per-function param count, flags functions over --max (default 5) = complexity's horizontal pair
  yagura coupling [--dir .] [--module M] [--strict]  Package import coupling: fan-in/out + instability + Stable Dependencies Principle violations (module path auto-detected from go.mod)
  yagura api-doc [--dir .] [--min-doc R] [--strict]          Exported-API doc discipline: documented ratio + undocumented exported funcs/types/consts/vars/methods (godoc, golint-compatible)
  yagura dead-code [--dir .] [--strict]           Dead unexported declarations: package-level funcs/types/consts/vars never referenced within their own package
  yagura recv-check [--dir .] [--strict]          Method receiver consistency: inconsistent receiver names, mixed value/pointer receivers, un-idiomatic names (this/self)
  yagura code-health [--dir .] [--min-grade G]    Composite maintainability grade (A-F) per package from complexity/apidoc/deadcode/recv-check/assert-check/ast-check
  Add --json to any of the above for machine-readable output.
  ai-verify/quality-check/secretscan accept --rules-file (or auto-detect
    .yagura/{aiverify,quality,secretscan}.json) to load project-specific rules.
  pin-drift needs YAGURA_GITHUB_TOKEN (GitHub API SHA verification).

Set passphrase via YAGURA_SECRET_PASSPHRASE for set/get.
Configuration is via environment variables. See documentation.
`

// runSecret は `yagura secret <subcommand>` を処理する。
//
// Subcommands:
//
//	set <name>    — stdin から plaintext を読込み、暗号化保存
//	get <name>    — 復号して stdout に出力
//	list          — 名前一覧を改行区切りで出力
//	delete <name> — 削除
//
// パスフレーズは環境変数 YAGURA_SECRET_PASSPHRASE で渡す(CLI スクリプト向け)。
// インタラクティブ tty 入力は echo OFF が無依存で困難なため未対応(将来作業)。
//
// secret store の場所は YAGURA_STATE_DIR/secrets(config.SecretsPath()) を使う。
func runSecret(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, "Usage: yagura secret {set|get|list|delete} [name]\n\n"+
			"  Set passphrase via YAGURA_SECRET_PASSPHRASE env var.\n"+
			"  Plaintext for 'set' is read from stdin.\n")
		return nil
	}

	// secret store は state dir だけ必要(GitHub token は不要)。
	stateDir, err := config.ResolveStateDir()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	store, err := secrets.NewStore(config.SecretsDirFor(stateDir))
	if err != nil {
		return fmt.Errorf("init store: %w", err)
	}

	sub := args[0]
	rest := args[1:]

	// list は passphrase 不要
	if sub == "list" {
		names, err := store.List()
		if err != nil {
			return fmt.Errorf("list: %w", err)
		}
		for _, n := range names {
			fmt.Fprintln(stdout, n)
		}
		return nil
	}
	// delete も passphrase 不要(file removal だけ)
	if sub == "delete" {
		if len(rest) == 0 {
			return errors.New("delete requires <name>")
		}
		if err := store.Delete(rest[0]); err != nil {
			return fmt.Errorf("delete: %w", err)
		}
		return nil
	}

	// set / get は passphrase が必要
	passphrase := os.Getenv("YAGURA_SECRET_PASSPHRASE")
	if passphrase == "" {
		return errors.New("YAGURA_SECRET_PASSPHRASE must be set for set/get")
	}

	switch sub {
	case "set":
		if len(rest) == 0 {
			return errors.New("set requires <name>")
		}
		plaintext, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20)) // 1 MB max
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		plaintext = bytesTrimTrailingNewline(plaintext)
		if err := store.Set(rest[0], plaintext, passphrase); err != nil {
			return fmt.Errorf("set: %w", err)
		}
		return nil
	case "get":
		if len(rest) == 0 {
			return errors.New("get requires <name>")
		}
		plaintext, err := store.Get(rest[0], passphrase)
		if err != nil {
			return fmt.Errorf("get: %w", err)
		}
		stdout.Write(plaintext)
		stdout.Write([]byte("\n"))
		return nil
	}
	return fmt.Errorf("unknown subcommand %q (use set/get/list/delete)", sub)
}

func bytesTrimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// verifyAudit は config を読まずに YAGURA_STATE_DIR 直下の audit/ を検証する。
// 検証中に daemon が動いていても safe(read-only)。
//
// 戻り値:
//   - nil: 全ファイル OK
//   - error: 1 つ以上のファイルで integrity check 失敗
//
// out は人間向けの結果一覧の書込先(stdout を想定)。
// この関数は GITHUB_TOKEN を読まない — verify は通常運用とは独立して
// オフラインでも実行できる(disaster recovery を意識)。
func verifyAudit(out io.Writer) error {
	// daemon と同じ state dir 解決を使う(以前は ~/.yagura を見ており、daemon が
	// 書き込む ~/.yagura/state/audit と不一致だった)。
	stateDir, err := config.ResolveStateDir()
	if err != nil {
		return fmt.Errorf("could not resolve state dir: %w", err)
	}
	auditDir := config.AuditDirFor(stateDir)
	results, err := audit.Verify(auditDir)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if len(results) == 0 {
		fmt.Fprintln(out, "no audit files found in", auditDir)
		return nil
	}
	allOK := true
	for _, r := range results {
		status := "OK"
		if !r.OK {
			status = "FAILED"
			allOK = false
		}
		fmt.Fprintf(out, "%-8s %s  records=%d", status, r.File, r.TotalRecords)
		if !r.OK {
			fmt.Fprintf(out, "  failed_at_seq=%d  reason=%q", r.FailedAtSeq, r.Reason)
		}
		fmt.Fprintln(out)
	}
	if !allOK {
		return fmt.Errorf("audit log integrity check failed")
	}
	return nil
}

func run() error {
	// 1. Config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// 2. Logger
	logger := logging.New(cfg.LogLevel, serviceName, version, os.Stdout)
	logger.Info("yagura starting",
		"version", version,
		"go", runtime.Version(),
		"config", cfg.String())

	// 3. Metrics
	mreg := metrics.NewRegistry()
	scanCounter := mreg.NewCounter("yagura_scan_total", "Total number of project scans")
	scanFailedCounter := mreg.NewCounter("yagura_scan_failed_total", "Total number of failed scans")
	scanDurationMs := mreg.NewGauge("yagura_last_scan_duration_ms", "Duration of last scan cycle (ms)")
	scanLastAt := mreg.NewGauge("yagura_last_scan_unix", "Unix timestamp of last scan")

	// Portfolio metrics (auto-updated post-scan)
	projTotal := mreg.NewGauge("yagura_projects_total", "Total registered projects")
	projActive := mreg.NewGauge("yagura_projects_active", "Active stage projects")
	projFailingCI := mreg.NewGauge("yagura_projects_failing_ci", "Projects with failing CI")

	// Process metrics
	buildInfo := mreg.NewGauge("yagura_build_info", "Build info (always 1, version in label)")
	buildInfo.Set(1)
	startTime := mreg.NewGauge("yagura_start_time_unix", "Unix timestamp of process start")
	startTime.Set(time.Now().Unix())

	scannerMetrics := &scannerMetricsAdapter{
		scanned:  scanCounter,
		failed:   scanFailedCounter,
		duration: scanDurationMs,
		lastAt:   scanLastAt,
	}

	// 4. Registry
	reg, regErr := registry.New(cfg.ProjectsDir())
	if regErr != nil {
		// 部分ロードエラーは警告のみ
		logger.Warn("registry partial load", "error", regErr)
	}
	if reg == nil {
		return fmt.Errorf("registry: nil")
	}
	logger.Info("registry loaded",
		"dir", cfg.ProjectsDir(),
		"counts", reg.Count())

	// 5. GitHub client (S0.1: per-owner credential separation)
	tokenStore := github.NewTokenStore(cfg.GitHubToken)
	for owner, token := range cfg.GitHubTokens {
		tokenStore.AddOwnerToken(owner, token)
	}
	gh := github.NewClient(github.Config{
		Tokens:  tokenStore,
		BaseURL: cfg.GitHubBase,
		Timeout: cfg.ScanTimeout,
	})
	if tokenStore.HasPerOwner() {
		logger.Info("github tokens loaded",
			"per_owner_count", tokenStore.PerOwnerCount(),
			"note", "S0.1 per-owner credential separation active")
	}

	// S1.6: SHA pin drift checker(GitHub API 経由で SHA 検証)
	pinChecker := pindrift.New(gh)
	// v0.12.0: rate-limit aware execution
	// GitHub API authenticated rate limit (5000/h) に達する前に sleep して、
	// portfolio 全体の scan が部分失敗しないようにする。
	pinChecker.RateLimit = pindrift.NewRateLimitGuard(gh.LastRateLimit)

	// 6. Scanner(goroutine 起動)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// OSV + Scorecard + SecretScan クライアント(scanner と MCP tool で共有)
	osvClient := osv.New()             // OSV.dev、30s timeout
	scorecardClient := scorecard.New() // api.scorecard.dev、30s timeout
	secretScanner := secretscan.New()  // gitleaks 互換 regex + entropy 検出
	sbomGen := sbom.New()              // CycloneDX 1.5 SBOM 生成器(zero-dep)
	ghaAuditor := ghaaudit.New()       // GitHub Actions workflow 静的解析器(zero-dep)

	// v0.30.0 alert lifecycle store — created before the scanner so the periodic
	// health sweep can exclude resolved/snoozed alerts (same filter as alert_fix).
	// nil (load error) is tolerated: the sweep then reports every evaluated alert.
	var alertStore *alertfix.Store
	alertStatePath := filepath.Join(cfg.StateDir, "alert_state.jsonl")
	if st, err := alertfix.NewStore(alertStatePath); err != nil {
		logger.Warn("alert state load failed (continuing without lifecycle)",
			"path", alertStatePath, "err", err)
	} else {
		alertStore = st
	}

	health := &healthState{}
	scan := scanner.New(scanner.Config{
		Registry:    reg,
		GitHub:      gh,
		Logger:      logger,
		Metrics:     scannerMetrics,
		Interval:    cfg.ScanInterval,
		ScanTimeout: cfg.ScanTimeout,
		// Scanner ↔ alert_fix periodic loop (v0.35): after each scan cycle, run a
		// health sweep over the freshly-updated sensor data, stash the latest
		// result (for the dashboard banner) and log a structured summary so
		// operators see health drift without an on-demand alert_fix call.
		AfterScan: func(context.Context) {
			report := healthSweep(reg, alertStore)
			health.set(report)
			if report.Total > 0 {
				logger.Info("health sweep",
					"projects", report.ProjectsScanned,
					"alerts", report.Total,
					"critical", report.BySeverity[alertfix.SevCritical],
					"high", report.BySeverity[alertfix.SevHigh],
					"has_critical", report.HasCritical)
			} else {
				logger.Debug("health sweep clean", "projects", report.ProjectsScanned)
			}
		},
	})
	scan.Start(ctx)
	logger.Info("scanner started", "interval", cfg.ScanInterval)

	// SecurityScanner: 24h 周期で Scorecard + OSV を取得して registry に反映する。
	secScan := scan.NewSecurityScanner(osvClient, scorecardClient, cfg.SecurityScanInterval)
	secScan.Start(ctx)

	// Portfolio gauge updater: refresh stage/CI counts every 30s.
	// Cheap (in-memory iteration over ~100 projects max), independent of scanner.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		updateGauges := func() {
			projects := reg.List()
			var active, failing int
			for _, p := range projects {
				if p.Stage == "active" {
					active++
				}
				if p.CIStatus == "failing" {
					failing++
				}
			}
			projTotal.Set(int64(len(projects)))
			projActive.Set(int64(active))
			projFailingCI.Set(int64(failing))
		}
		updateGauges() // immediate
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				updateGauges()
			}
		}
	}()

	// 6.5. Audit logger(append-only JSONL with hash chain)
	auditLog, err := audit.New(cfg.AuditPath())
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	defer func() { auditLog.Close() }()
	if err := auditLog.Append(audit.Record{
		Kind:  "yagura_started",
		Actor: "yagura",
		Fields: map[string]any{
			"version":  version,
			"go":       runtime.Version(),
			"interval": cfg.ScanInterval.String(),
		},
	}); err != nil {
		logger.Warn("audit: initial record failed", "err", err)
	}
	logger.Info("audit logger initialized", "dir", cfg.AuditPath())

	// Audit retention: 起動時 1 回 + 24h 周期で古いファイルを削除する。
	// keep_days=0 のときは prune を完全停止(無制限保持)。
	if cfg.AuditKeepDays > 0 {
		runPrune := func() {
			n, err := audit.Prune(cfg.AuditPath(), cfg.AuditKeepDays)
			if err != nil {
				logger.Warn("audit prune failed", "err", err)
				return
			}
			if n > 0 {
				logger.Info("audit prune done", "deleted", n, "keep_days", cfg.AuditKeepDays)
				auditLog.Append(audit.Record{
					Kind:  "audit_pruned",
					Actor: "yagura",
					Fields: map[string]any{
						"deleted":   n,
						"keep_days": cfg.AuditKeepDays,
					},
				})
			}
		}
		runPrune()
		go func() {
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					runPrune()
				}
			}
		}()
	}

	// 7. MCP server
	mcpServer := mcp.New(cfg.MCPToken, logger)
	mcpServer.SetAudit(auditLog)

	// v0.35: persistent result cache (Roadmap #3) — write-through disk layer so
	// heavy sbom / ai_verify / quality_check results survive a daemon restart.
	// Best-effort: a mkdir failure just leaves the cache in-memory only.
	if cfg.StateDir != "" {
		cacheDir := filepath.Join(cfg.StateDir, "cache")
		if err := mcpServer.Cache().EnablePersistence(cacheDir); err != nil {
			logger.Warn("cache persistence disabled (continuing in-memory only)",
				"path", cacheDir, "err", err)
		} else {
			// Long-running daemon: reclaim expired on-disk entries that memory LRU
			// evicted, so {StateDir}/cache doesn't accumulate stale files over a run.
			go func() {
				ticker := time.NewTicker(time.Hour)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						mcpServer.Cache().PruneExpiredDisk()
					}
				}
			}()
		}
	}

	// v0.30.0: alert lifecycle store (resolved/snoozed の永続化)
	// 注: store は scanner より前に作成済み(health sweep が lifecycle filter に使う)。
	if alertStore != nil {
		mcpServer.SetAlertStore(alertStore)
	}

	// v0.31.0: Claude Code hook receiver (HTTP hooks Feb 2026 GA)
	// 受け取った hook events は {state_dir}/claude_hooks.jsonl に persist + 集計
	hookPath := filepath.Join(cfg.StateDir, "claude_hooks.jsonl")
	hookReceiver, err := hookreceiver.NewReceiver(hookPath, &registryLookup{reg: reg}, 10000)
	if err != nil {
		logger.Warn("hook receiver init failed (continuing without hooks)",
			"path", hookPath, "err", err)
		hookReceiver = nil
	}
	mcpServer.SetHookReceiver(hookReceiver)

	// v0.13.0: agent handoff layer (Claude Code ↔ Windsurf)
	quotaMon := quotamonitor.New()
	// v0.17.0: persist history across daemon restarts
	usageHistoryPath := filepath.Join(cfg.StateDir, "usage_history.jsonl")
	quotaMon.SetPersistPath(usageHistoryPath)
	if err := quotaMon.LoadHistory(usageHistoryPath); err != nil {
		logger.Warn("usage history load failed (continuing with empty history)",
			"path", usageHistoryPath, "err", err)
	} else {
		logger.Info("usage history loaded", "path", usageHistoryPath)
	}

	handoffStore, err := handoff.New(cfg.StateDir)
	if err != nil {
		logger.Error("handoff store init failed", "error", err)
		os.Exit(1)
	}
	launcher := agentlauncher.New()

	// v0.14.0: workspace auto-detection
	// 起動時 CWD から .git を上方向に探索し、handoff のデフォルト workspace に設定。
	// 検出失敗(.git 無し)時は state_dir に fallback(後方互換)。
	wsRoot, gitFound, wsErr := workspace.DetectCWD()
	if wsErr != nil || !gitFound {
		wsRoot = cfg.StateDir
		logger.Info("workspace auto-detect: using state_dir (no .git found in CWD ancestry)",
			"workspace", wsRoot)
	} else {
		logger.Info("workspace auto-detected from .git", "workspace", wsRoot)
	}

	logger.Info("agent handoff initialized",
		"handoff_state", handoffStore.Path(),
		"workspace_root", wsRoot)

	// v0.15.0: background watchdog goroutine
	// 30 秒毎に各 agent の heartbeat を check し、stale 化 / 復帰イベントを log。
	// daemon shutdown 時に ctx.Done で停止。
	go quotaMon.Watch(ctx, 30*time.Second, quotamonitor.DefaultIdleTimeout,
		func(e quotamonitor.StaleEvent) {
			if e.BecameStale {
				logger.Warn("agent went stale",
					"agent", string(e.Agent),
					"at", e.At.UTC().Format(time.RFC3339),
					"elapsed_since_heartbeat", e.Elapsed.String())
			} else {
				logger.Info("agent recovered from stale",
					"agent", string(e.Agent),
					"at", e.At.UTC().Format(time.RFC3339))
			}
		})

	mcp.SetVersion(version)
	mcp.RegisterDefaultTools(mcpServer, mcp.Deps{
		Registry:       reg,
		Now:            time.Now,
		OSV:            osvClient,
		Scorecard:      scorecardClient,
		SecretScanner:  secretScanner,
		Sbom:           sbomGen,
		Ghaaudit:       ghaAuditor,
		PinDrift:       pinChecker,
		MainModulePath: "github.com/shizukutanaka/yagura",
		MainVersion:    version,
		// v0.13.0: handoff
		QuotaMonitor:  quotaMon,
		HandoffStore:  handoffStore,
		AgentLauncher: launcher,
		WorkspaceRoot: wsRoot,       // v0.14.0: auto-detected from .git ancestry
		StateDir:      cfg.StateDir, // v0.102.0: yagura_self_improve_history
	})
	logger.Info("mcp server initialized",
		"tools", mcpServer.ToolNames(),
		"auth", cfg.MCPToken != "")

	// 8. Dashboard
	dash, err := dashboard.New(reg, logger)
	if err != nil {
		return fmt.Errorf("dashboard: %w", err)
	}
	// v0.14.0: agent handoff panel on dashboard
	dash.SetAgentStatusProvider(quotaMon)

	// v0.35: per-project agent activity column on dashboard (from recorded hooks),
	// with a structured drill-down (/dashboard/activity?slug=…) via session_summary.
	if hookReceiver != nil {
		dash.SetHookActivityProvider(hookActivityAdapter{hr: hookReceiver, srv: mcpServer})
	}
	// v0.35: portfolio health banner fed by the scanner's periodic alert_fix sweep.
	dash.SetPortfolioHealthProvider(health)

	// 9. HTTP server
	var ready atomic.Bool
	ready.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("draining"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		// 既存 metrics (scan counters 等) を最初に出力
		mreg.ServeHTTP(w, r)
		// v0.31.0: ToolStats + AlertFix + HookReceiver の label 付き metric を追加
		// 同一 endpoint で全 yagura metrics が揃う(Prometheus 慣行)
		extras := collectYaguraMetrics(mcpServer, hookReceiver, health)
		promexport.Render(w, extras)
	})
	mux.Handle("/mcp", mcpServer)
	mux.Handle("/dashboard", dash)
	mux.Handle("/dashboard/", dash) // trailing slash tolerated

	// v0.31.0: Claude Code HTTP hooks receiver (Feb 2026 GA)
	// PreToolUse / PostToolUse / Stop / SubagentStop 等を localhost に POST してくる
	// observation mode: 全 event を JSONL persist、空 response で agent 継続
	// v0.105.0: /mcp・HTTP API と同じ MCPToken Bearer 認証を適用(以前は無認証だった)。
	if hookReceiver != nil {
		mux.HandleFunc("/hooks/claude-code", requireBearerToken(cfg.MCPToken, hookReceiver.Handle))
		// v0.35: agent-agnostic alias. Any agent (Gemini CLI / Codex / OTel /
		// generic) can POST its lifecycle events here; the receiver normalizes
		// non-Claude-Code payloads via internal/agentevent.
		mux.HandleFunc("/hooks/agent", requireBearerToken(cfg.MCPToken, hookReceiver.Handle))
	}

	// v0.31.0: MCP 2026 spec .well-known metadata endpoint
	// (https://blog.modelcontextprotocol.io/posts/2026-mcp-roadmap/)
	// 接続前に server capabilities を発見可能にする
	mux.HandleFunc("/.well-known/mcp", func(w http.ResponseWriter, _ *http.Request) {
		meta := map[string]any{
			"name":      "yagura",
			"version":   version,
			"protocol":  "mcp/2025-11",
			"transport": []string{"http"},
			"endpoints": map[string]string{
				"mcp":               "/mcp",
				"hooks_claude_code": "/hooks/claude-code",
				"hooks_agent":       "/hooks/agent",
				"metrics":           "/metrics",
				"dashboard":         "/dashboard",
			},
			"capabilities": map[string]any{
				"tools":               len(mcpServer.ToolNames()),
				"audit_log":           true,
				"hook_receiver":       hookReceiver != nil,
				"alert_lifecycle":     true,
				"prometheus_metrics":  true,
				"reproducible_builds": true,
			},
			"description": "Portfolio orchestrator for the sovereign computing stack. Zero-dep Go MCP server with cortex flywheel ②③④ + Claude Code hooks receiver.",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(meta)
	})

	// v0.11.0: CI 統合用 HTTP endpoints(/sbom, /gha-audit, /pin-drift)
	// v0.12.0: per-route rate limiting で DoS / PAT 枯渇を防止
	sbomLimiter := httplimit.New(httplimit.Options{
		Capacity:        10, // burst 10
		RefillPerMinute: 60, // 1/sec sustained
		KeyFn:           httplimit.TokenKey,
	})
	ghaAuditLimiter := httplimit.New(httplimit.Options{
		Capacity:        5,  // CPU-bound, smaller burst
		RefillPerMinute: 30, // 1 per 2 sec
		KeyFn:           httplimit.TokenKey,
	})
	pinDriftLimiter := httplimit.New(httplimit.Options{
		Capacity:        3, // GitHub PAT 保護のため最も厳しく
		RefillPerMinute: 6, // 1 per 10 sec sustained
		KeyFn:           httplimit.TokenKey,
	})
	// 定期 GC で idle bucket を回収(メモリ leak 防止)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sbomLimiter.GC()
				ghaAuditLimiter.GC()
				pinDriftLimiter.GC()
			case <-ctx.Done():
				return
			}
		}
	}()

	registerHTTPAPI(mux, httpAPIDeps{
		Sbom:            sbomGen,
		Ghaaudit:        ghaAuditor,
		PinDrift:        pinChecker,
		MainModulePath:  "github.com/shizukutanaka/yagura",
		MainVersion:     version,
		AuthToken:       cfg.MCPToken,
		SbomLimiter:     sbomLimiter,
		GhaAuditLimiter: ghaAuditLimiter,
		PinDriftLimiter: pinDriftLimiter,
	})

	// root はダッシュボードへ
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/dashboard", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	if cfg.EnablePprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		logger.Info("pprof enabled at /debug/pprof/")
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           withRequestLog(logger, withSecurityHeaders(mux)),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          nil,
	}

	logger.Info("http server starting", "addr", cfg.Addr)
	httpErrCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErrCh <- err
		}
		close(httpErrCh)
	}()

	// 10. SIGTERM / SIGINT graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, shutdownSignals()...)

	select {
	case err := <-httpErrCh:
		return fmt.Errorf("http server: %w", err)
	case sig := <-sigCh:
		logger.Info("signal received, shutting down", "signal", sig.String())
	}

	// readiness を false にして 5 秒 drain
	ready.Store(false)
	logger.Info("readiness=false, draining", "grace", readyDrainGrace)
	time.Sleep(readyDrainGrace)

	// scanner 停止
	logger.Info("stopping scanner")
	scan.Stop()

	// HTTP server graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), httpShutdownGrace)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown", "error", err)
		return err
	}

	logger.Info("yagura stopped cleanly")
	auditLog.Append(audit.Record{
		Kind:  "yagura_stopped",
		Actor: "yagura",
	})
	return nil
}

// scannerMetricsAdapter は scanner.Metrics interface を metrics package の型に
// ブリッジする(アダプタパターン)。
type scannerMetricsAdapter struct {
	scanned  *metrics.Counter
	failed   *metrics.Counter
	duration *metrics.Gauge
	lastAt   *metrics.Gauge
}

func (a *scannerMetricsAdapter) IncScanned() { a.scanned.Inc() }
func (a *scannerMetricsAdapter) IncFailed()  { a.failed.Inc() }
func (a *scannerMetricsAdapter) SetLastScanDuration(d time.Duration) {
	a.duration.Set(d.Milliseconds())
}
func (a *scannerMetricsAdapter) SetLastScanAt(t time.Time) {
	a.lastAt.Set(t.Unix())
}

// withRequestLog は単純な HTTP request logger middleware。
func withRequestLog(logger interface {
	Info(msg string, args ...any)
}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		// /metrics や /healthz の高頻度 polling はログを抑える
		if isNoisyPath(r.URL.Path) {
			return
		}
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", clientAddr(r))
	})
}

func isNoisyPath(p string) bool {
	return p == "/healthz" || p == "/readyz" || p == "/metrics"
}

func clientAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush は SSE 等の streaming endpoint で必要。
// embedded ResponseWriter が Flusher を実装していれば forward する。
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ─── v0.31.0: Hook receiver + Prometheus extras ──────────────────

// registryLookup adapts *registry.Registry to hookreceiver.ProjectLookup.
//
// LocalPath prefix match で cwd → project slug を解決する。
type registryLookup struct {
	reg *registry.Registry
}

// healthSweep evaluates alert_fix over every registered project's current sensor
// data and returns the aggregate report. Used by the scanner's AfterScan hook to
// drive a periodic health sweep. Reuses mcp.ProjectToSnapshot (same extraction as
// yagura_alert_fix) and alertfix.EvaluateAll, so the deterministic rules match the
// on-demand tool. Plan.md enrichment is intentionally skipped here (sensor-only;
// no per-cycle disk I/O) — the on-demand tool still does the richer evaluation.
//
// When store is non-nil, resolved/snoozed alerts are excluded (same lifecycle
// filter as yagura_alert_fix) so the dashboard banner/list only nags about alerts
// that are actually still open.
func healthSweep(reg *registry.Registry, store *alertfix.Store) alertfix.Report {
	projects := reg.List()
	snaps := make([]alertfix.ProjectSnapshot, 0, len(projects))
	for _, p := range projects {
		snaps = append(snaps, mcp.ProjectToSnapshot(*p))
	}
	report := alertfix.EvaluateAll(snaps, alertfix.DefaultThresholds())
	if store != nil {
		report = store.FilterReport(report)
	}
	return report
}

// healthState holds the latest health-sweep report for read-only consumers
// (the dashboard banner). Updated by the scanner's AfterScan hook; concurrent
// reads from HTTP handlers are guarded by the mutex.
type healthState struct {
	mu     sync.RWMutex
	report alertfix.Report
	at     time.Time
	set_   bool
}

func (h *healthState) set(r alertfix.Report) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.report = r
	h.at = time.Now()
	h.set_ = true
}

// PortfolioHealth implements dashboard.PortfolioHealthProvider. ok is false until
// the first sweep has run.
func (h *healthState) PortfolioHealth() (dashboard.HealthSummary, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if !h.set_ {
		return dashboard.HealthSummary{}, false
	}
	r := h.report
	return dashboard.HealthSummary{
		Total:       r.Total,
		Critical:    r.BySeverity[alertfix.SevCritical],
		High:        r.BySeverity[alertfix.SevHigh],
		Medium:      r.BySeverity[alertfix.SevMedium],
		Low:         r.BySeverity[alertfix.SevLow],
		HasCritical: r.HasCritical,
		At:          h.at,
	}, true
}

// PortfolioAlerts implements dashboard.PortfolioHealthProvider for the alert
// drill-down. Alerts are already severity-ranked by alertfix.EvaluateAll.
func (h *healthState) PortfolioAlerts() ([]dashboard.AlertItem, time.Time, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if !h.set_ {
		return nil, time.Time{}, false
	}
	out := make([]dashboard.AlertItem, 0, len(h.report.Alerts))
	for _, a := range h.report.Alerts {
		out = append(out, dashboard.AlertItem{
			ID:             a.ID,
			Project:        a.Project,
			Source:         string(a.Source),
			Severity:       string(a.Severity),
			Title:          a.Title,
			Recommendation: a.Recommendation,
		})
	}
	return out, h.at, true
}

// hookActivityAdapter adapts the hook receiver's per-project stats to the
// dashboard's HookActivityProvider (read-only Activity column + drill-down).
type hookActivityAdapter struct {
	hr  *hookreceiver.Receiver
	srv *mcp.Server
}

func (a hookActivityAdapter) ProjectActivity(slug string) (dashboard.HookActivity, bool) {
	st := a.hr.ProjectStats(slug)
	if st.Total == 0 {
		return dashboard.HookActivity{}, false
	}
	// top tool, deterministic tie-break by name.
	var tools []string
	for t := range st.ByTool {
		tools = append(tools, t)
	}
	sort.Strings(tools)
	topTool, topCount := "", 0
	for _, t := range tools {
		if st.ByTool[t] > topCount {
			topTool, topCount = t, st.ByTool[t]
		}
	}
	return dashboard.HookActivity{
		Total:     st.Total,
		Errors:    st.ErrorCount,
		TopTool:   topTool,
		LastEvent: st.LastEvent,
	}, true
}

// ProjectActivityDetail builds the structured drill-down view for one project by
// summarizing its recorded hook timeline (same pipeline as yagura_session_summary).
func (a hookActivityAdapter) ProjectActivityDetail(slug string) (dashboard.ActivityDetail, bool) {
	if a.srv == nil {
		return dashboard.ActivityDetail{}, false
	}
	sum, ok := a.srv.RecordedSummary(slug, "", 0)
	if !ok || sum.Events == 0 {
		return dashboard.ActivityDetail{}, false
	}
	d := dashboard.ActivityDetail{
		Slug:            slug,
		Summary:         sum.Summary,
		ToolInvocations: sum.ToolInvocations,
		DistinctTools:   sum.DistinctTools,
		ErrorRate:       sum.ErrorRate,
		Agents:          sum.Agents,
		ByTool:          sortedLabelCounts(sum.ByTool),
		ByOperation:     sortedLabelCounts(sum.ByOperation),
		ToolSequence:    sum.ToolSequence,
		SequenceTrunc:   sum.SequenceTrunc,
		Anomalies:       sum.Anomalies,
	}
	for _, e := range sum.Errors {
		d.Errors = append(d.Errors, dashboard.ActivityError{
			Tool: e.Tool, ErrorType: e.ErrorType, Agent: e.Agent,
		})
	}
	return d, true
}

// sortedLabelCounts turns a label→count map into a deterministically ordered
// slice: count descending, then label ascending (stable for templates).
func sortedLabelCounts(m map[string]int) []dashboard.LabelCount {
	if len(m) == 0 {
		return nil
	}
	out := make([]dashboard.LabelCount, 0, len(m))
	for k, v := range m {
		out = append(out, dashboard.LabelCount{Label: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func (l *registryLookup) ResolveByPath(cwd string) (string, bool) {
	if l == nil || l.reg == nil || cwd == "" {
		return "", false
	}
	// LocalPath が cwd の prefix なら一致
	for _, p := range l.reg.List() {
		if p.LocalPath != "" && strings.HasPrefix(cwd, p.LocalPath) {
			return p.Slug, true
		}
	}
	return "", false
}

// collectYaguraMetrics は MCP server + Hook receiver の状態を Prometheus
// exposition format にまとめる。
//
// 既存 metrics.Registry が scan counters を担い、こちらが label 付き metrics
// (per-tool, per-project) を担当する分担。各 metrics family は独立(順序のみ固定)
// なので family 別に mcpToolMetrics 等へ委譲する。
func collectYaguraMetrics(srv *mcp.Server, hr *hookreceiver.Receiver, health *healthState) []promexport.Collection {
	var out []promexport.Collection
	out = append(out, mcpToolMetrics(srv)...)
	out = append(out, portfolioHealthMetrics(health)...)
	out = append(out, cacheMetrics(srv)...)
	out = append(out, hookMetrics(hr)...)
	out = append(out, alertLifecycleMetrics(srv)...)
	return out
}

// mcpToolMetrics は MCP tool 別の呼出数/リクエストバイト数/レスポンスバイト数/
// エラー数を Prometheus counter として組み立てる(per-tool labels)。
func mcpToolMetrics(srv *mcp.Server) []promexport.Collection {
	stats := srv.AllToolStats()
	calls := make([]promexport.Sample, 0, len(stats))
	reqBytes := make([]promexport.Sample, 0, len(stats))
	respBytes := make([]promexport.Sample, 0, len(stats))
	errs := make([]promexport.Sample, 0, len(stats))
	for _, ts := range stats {
		labels := map[string]string{"tool": ts.Name}
		calls = append(calls, promexport.Sample{Labels: labels, Value: float64(ts.Calls)})
		reqBytes = append(reqBytes, promexport.Sample{Labels: labels, Value: float64(ts.RequestBytes)})
		respBytes = append(respBytes, promexport.Sample{Labels: labels, Value: float64(ts.ResponseBytes)})
		errs = append(errs, promexport.Sample{Labels: labels, Value: float64(ts.ErrorCount)})
	}
	return []promexport.Collection{
		{Name: "yagura_mcp_tool_calls_total",
			Type: "counter", Help: "Total MCP tool invocations per tool",
			Samples: calls},
		{Name: "yagura_mcp_tool_request_bytes_total",
			Type: "counter", Help: "Cumulative request bytes per tool",
			Samples: reqBytes},
		{Name: "yagura_mcp_tool_response_bytes_total",
			Type: "counter", Help: "Cumulative response bytes per tool",
			Samples: respBytes},
		{Name: "yagura_mcp_tool_errors_total",
			Type: "counter", Help: "Total errors returned per tool",
			Samples: errs},
	}
}

// portfolioHealthMetrics は直近の alert_fix health sweep をゲージにする。外部監視が
// per-project sensor だけでなく portfolio 全体のアラート圧を見られるようにする
// (resolved/snoozed は除外済み)。sweep 未実施 / health 未設定なら空。
func portfolioHealthMetrics(health *healthState) []promexport.Collection {
	if health == nil {
		return nil
	}
	hs, ok := health.PortfolioHealth()
	if !ok {
		return nil
	}
	return []promexport.Collection{{
		Name: "yagura_portfolio_alerts",
		Type: "gauge",
		Help: "Open alerts from the latest health sweep by severity (resolved/snoozed excluded)",
		Samples: []promexport.Sample{
			{Labels: map[string]string{"severity": "critical"}, Value: float64(hs.Critical)},
			{Labels: map[string]string{"severity": "high"}, Value: float64(hs.High)},
			{Labels: map[string]string{"severity": "medium"}, Value: float64(hs.Medium)},
			{Labels: map[string]string{"severity": "low"}, Value: float64(hs.Low)},
		},
	}}
}

// cacheMetrics は dedupe cache のヒット/ミス累計を返す(観測 0 件なら空)。
func cacheMetrics(srv *mcp.Server) []promexport.Collection {
	cs := srv.CacheStats()
	if cs.Hits+cs.Misses == 0 {
		return nil
	}
	return []promexport.Collection{
		{Name: "yagura_cache_hits_total",
			Type: "counter", Help: "Cumulative dedupe cache hits",
			Samples: []promexport.Sample{{Value: float64(cs.Hits)}}},
		{Name: "yagura_cache_misses_total",
			Type: "counter", Help: "Cumulative dedupe cache misses",
			Samples: []promexport.Sample{{Value: float64(cs.Misses)}}},
	}
}

// hookMetrics は hook receiver の project 別イベント/エラー/ツール呼出を組み立てる。
// hr が nil なら空。
func hookMetrics(hr *hookreceiver.Receiver) []promexport.Collection {
	if hr == nil {
		return nil
	}
	allStats := hr.AllStats()
	var hookCalls, hookTools []promexport.Sample
	hookErrs := make([]promexport.Sample, 0, len(allStats))
	for slug, st := range allStats {
		for event, count := range st.ByEvent {
			hookCalls = append(hookCalls, promexport.Sample{
				Labels: map[string]string{"project": slug, "event": event},
				Value:  float64(count),
			})
		}
		// per-tool agent activity (any agent, via /hooks/agent). Maps to OTel
		// gen_ai.tool.name; exported as the `tool` label for Yagura consistency.
		for tool, count := range st.ByTool {
			hookTools = append(hookTools, promexport.Sample{
				Labels: map[string]string{"project": slug, "tool": tool},
				Value:  float64(count),
			})
		}
		hookErrs = append(hookErrs, promexport.Sample{
			Labels: map[string]string{"project": slug},
			Value:  float64(st.ErrorCount),
		})
	}
	var out []promexport.Collection
	if len(hookCalls) > 0 {
		out = append(out,
			promexport.Collection{Name: "yagura_hook_events_total",
				Type: "counter", Help: "Agent hook events received per project per event type (any agent)",
				Samples: hookCalls},
			promexport.Collection{Name: "yagura_hook_errors_total",
				Type: "counter", Help: "Agent tool errors observed via hooks per project (any agent)",
				Samples: hookErrs},
		)
	}
	// Tool-call family only when at least one tool call was observed — a project
	// with only non-tool events (SessionStart/Stop) would otherwise emit an empty
	// HELP/TYPE header with no samples.
	if len(hookTools) > 0 {
		out = append(out, promexport.Collection{Name: "yagura_hook_tool_calls_total",
			Type: "counter", Help: "Agent tool calls observed via hooks per project per tool (OTel gen_ai.tool.name)",
			Samples: hookTools})
	}
	return out
}

// alertLifecycleMetrics は alert store の lifecycle 別現在件数をゲージにする
// (store 未設定なら空)。
func alertLifecycleMetrics(srv *mcp.Server) []promexport.Collection {
	store := srv.AlertStore()
	if store == nil {
		return nil
	}
	stats := store.Stats()
	out := make([]promexport.Collection, 0, len(stats))
	for status, count := range stats {
		out = append(out, promexport.Collection{
			Name: "yagura_alert_lifecycle_current",
			Type: "gauge",
			Help: "Current count of alerts by lifecycle status",
			Samples: []promexport.Sample{{
				Labels: map[string]string{"status": string(status)},
				Value:  float64(count),
			}},
		})
	}
	return out
}
