// cli.go: yagura v0.35 "CLI direct mode".
//
// 動機 (v0.35.0):
//   v0.34 まで、registry 操作や local scan は MCP client(JSON-RPC over HTTP)
//   経由でしか叩けなかった(MCP server デメリット #7)。シェルスクリプトや CI
//   ステップから素早く `yagura list` / `yagura register` したいケースに応えられ
//   なかった。
//
// 本ファイルは MCP を介さず、registry / project / scanner パッケージの domain
// logic を直接呼ぶ top-level subcommand 群を提供する。ロジックは再実装せず、
// internal/mcp の各 tool handler と同じ呼び出しを行う(出力 shape も合わせる)。
//
// 設計:
//   - 各 verb は専用の flag.FlagSet(ContinueOnError, output=stderr)で parse。
//     ExitOnError を使わないのは os.Exit を避けてテスト/dispatch が制御を保つため。
//   - token 不要(registry CRUD / sbom / secretscan / gha-audit)は
//     config.ResolveStateDir() で state dir のみ解決(GitHub token を要求しない)。
//   - pin-drift だけ GitHub API が必要なので config.Load()(token 検証)を通す。
//   - register / update / unregister は daemon と同様に audit log へ best-effort
//     で追記する(trust base の append-only 監査証跡を CLI でも欠かさない)。

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shizukutanaka/yagura/internal/agentevent"
	"github.com/shizukutanaka/yagura/internal/agentmd"
	"github.com/shizukutanaka/yagura/internal/agentparallel"
	"github.com/shizukutanaka/yagura/internal/aiverify"
	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/apidoc"
	"github.com/shizukutanaka/yagura/internal/assertcheck"
	"github.com/shizukutanaka/yagura/internal/astcheck"
	"github.com/shizukutanaka/yagura/internal/audit"
	"github.com/shizukutanaka/yagura/internal/ccsecurity"
	"github.com/shizukutanaka/yagura/internal/codehealth"
	"github.com/shizukutanaka/yagura/internal/complexity"
	"github.com/shizukutanaka/yagura/internal/config"
	"github.com/shizukutanaka/yagura/internal/coupling"
	"github.com/shizukutanaka/yagura/internal/coverage"
	"github.com/shizukutanaka/yagura/internal/ctxcheck"
	"github.com/shizukutanaka/yagura/internal/deadcode"
	"github.com/shizukutanaka/yagura/internal/deprank"
	"github.com/shizukutanaka/yagura/internal/diffscan"
	"github.com/shizukutanaka/yagura/internal/errdiscard"
	"github.com/shizukutanaka/yagura/internal/errpolicy"
	"github.com/shizukutanaka/yagura/internal/errwrap"
	"github.com/shizukutanaka/yagura/internal/synccheck"
	"github.com/shizukutanaka/yagura/internal/featurelist"
	"github.com/shizukutanaka/yagura/internal/flagarg"
	"github.com/shizukutanaka/yagura/internal/flowrisk"
	"github.com/shizukutanaka/yagura/internal/ghaaudit"
	"github.com/shizukutanaka/yagura/internal/github"
	"github.com/shizukutanaka/yagura/internal/harness"
	"github.com/shizukutanaka/yagura/internal/hotspot"
	"github.com/shizukutanaka/yagura/internal/initps1"
	"github.com/shizukutanaka/yagura/internal/initsh"
	"github.com/shizukutanaka/yagura/internal/injectscan"
	"github.com/shizukutanaka/yagura/internal/mcp"
	"github.com/shizukutanaka/yagura/internal/namecheck"
	"github.com/shizukutanaka/yagura/internal/opsrisk"
	"github.com/shizukutanaka/yagura/internal/paramcheck"
	"github.com/shizukutanaka/yagura/internal/pathpolicy"
	"github.com/shizukutanaka/yagura/internal/pindrift"
	"github.com/shizukutanaka/yagura/internal/plantracker"
	"github.com/shizukutanaka/yagura/internal/progressfile"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/projectgraph"
	"github.com/shizukutanaka/yagura/internal/publicityscan"
	"github.com/shizukutanaka/yagura/internal/qualitycheck"
	"github.com/shizukutanaka/yagura/internal/recovery"
	"github.com/shizukutanaka/yagura/internal/recvcheck"
	"github.com/shizukutanaka/yagura/internal/registry"
	"github.com/shizukutanaka/yagura/internal/returncheck"
	"github.com/shizukutanaka/yagura/internal/reviewgate"
	"github.com/shizukutanaka/yagura/internal/riskreason"
	"github.com/shizukutanaka/yagura/internal/sbom"
	"github.com/shizukutanaka/yagura/internal/secretscan"
	"github.com/shizukutanaka/yagura/internal/sessionsummary"
	"github.com/shizukutanaka/yagura/internal/testcoverage"
	"github.com/shizukutanaka/yagura/internal/today"
	"github.com/shizukutanaka/yagura/internal/vex"
)

// mainModulePath は sbom 生成対象(yagura 自身)の module path。
// run() のハードコードと同じ値を CLI でも使う。
const mainModulePath = "github.com/shizukutanaka/yagura"

// errUsage は flag parse / 引数不足を示す番兵。runCLI で exit code 2 に変換する。
// FlagSet 側が既に詳細を stderr に出している。
var errUsage = errors.New("usage error")

// cliHandler は direct-mode subcommand の実装シグネチャ。
type cliHandler func(args []string, stdout, stderr io.Writer) error

// cliHandlers は verb → handler の単一の真実源。dispatch()(委譲可否判定)と
// runCLI(実行)が同じ map を参照するので、verb 追加は 1 行で済み、
// 「集合」と「switch」の二重メンテによる取りこぼしが構造的に起きない。
var cliHandlers = map[string]cliHandler{
	// registry CRUD
	"list": cliList, "get": cliGet, "search": cliSearch, "stats": cliStats, "today": cliToday,
	"register": cliRegister, "update": cliUpdate, "unregister": cliUnregister,
	"graph":       cliGraph,
	"plan-status": cliPlanStatus, "release-radar": cliReleaseRadar,
	"ops-risk": cliOpsRisk, "risk-triage": cliRiskTriage,
	"recovery-decide": cliRecoveryDecide, "agents-md": cliAgentsMd,
	"feature-list": cliFeatureList, "harness-coverage": cliHarnessCoverage,
	"agent-event": cliAgentEvent, "init-sh": cliInitSh, "progress-file": cliProgressFile,
	"harness-recommend": cliHarnessRecommend, "session-summary": cliSessionSummary,
	"parallel-plan":   cliParallelPlan,
	"graph-neighbors": cliGraphNeighbors, "graph-impact": cliGraphImpact, "graph-stats": cliGraphStats,
	// local scans
	"sbom": cliSbom, "secretscan": cliSecretScan, "gha-audit": cliGhaAudit, "pin-drift": cliPinDrift,
	// .claude/ + MCP artifact audits
	"skill-audit": cliSkillAudit, "workflow-audit": cliWorkflowAudit, "settings-audit": cliSettingsAudit,
	"agent-config-audit": cliAgentConfigAudit, "plugin-audit": cliPluginAudit, "mcp-audit": cliMCPAudit,
	"publicity-scan": cliPublicityScan, "vex-audit": cliVexAudit, "self-improve-history": cliSelfImproveHistory,
	"path-policy": cliPathPolicy, "inject-scan": cliInjectScan, "cc-security": cliCCSecurity,
	"claudemd-audit": cliClaudeMdAudit,
	// ② Review code-quality gates
	"ai-verify": cliAIVerify, "quality-check": cliQualityCheck, "test-audit": cliTestAudit,
	"alert-fix": cliAlertFix, "alert-resolve": cliAlertResolve, "alert-snapshot": cliAlertSnapshot, "ast-check": cliASTCheck, "review-gate": cliReviewGate,
	"diff-scan": cliDiffScan, "flow-risk": cliFlowRisk, "coverage": cliCoverage,
	"assert-check": cliAssertCheck, "err-policy": cliErrPolicy, "complexity": cliComplexity,
	"coupling": cliCoupling, "api-doc": cliAPIDoc, "dead-code": cliDeadCode,
	"recv-check": cliRecvCheck, "code-health": cliCodeHealth, "param-check": cliParamCheck,
	"flag-arg": cliFlagArg, "return-check": cliReturnCheck,
	"err-discard": cliErrDiscard,
	"dep-rank":    cliDepRank,
	"hotspot":     cliHotspot,
	"name-check":  cliNameCheck,
	"ctx-check":   cliCtxCheck,
	"err-wrap":    cliErrWrap,
	"sync-check":  cliSyncCheck,
	// shell completion
	"completion": cliCompletion,
}

// isCLIVerb は args[0] が direct-mode subcommand かを返す(dispatch 用)。
func isCLIVerb(verb string) bool {
	_, ok := cliHandlers[verb]
	return ok
}

// runCLI は direct-mode subcommand を実行し、プロセス exit code を返す。
func runCLI(verb string, args []string, stdout, stderr io.Writer) int {
	h, ok := cliHandlers[verb]
	if !ok {
		fmt.Fprintf(stderr, "yagura: unknown command %q\n", verb)
		return 2
	}
	if err := h(args, stdout, stderr); err != nil {
		if errors.Is(err, errUsage) {
			return 2
		}
		fmt.Fprintf(stderr, "yagura %s: %v\n", verb, err)
		return 1
	}
	return 0
}

// newFlagSet は ContinueOnError(os.Exit しない)で stderr 出力の FlagSet を返す。
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseArgs は flag が positional の前後どちらにあっても解釈する(GNU getopt 風)。
// 標準 flag は最初の非 flag で停止するため、`register breeze repo --lang go` の
// ように flag を後置すると無視されてしまう。これを permute して回避し、
// positional 引数を順序どおり返す。
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positionals = append(positionals, rest[0])
		args = rest[1:]
	}
	return positionals, nil
}

// openRegistry は token 不要の state-dir resolver で registry を開く。
// partial-load エラーは warning として表示し、致命扱いにしない(daemon と同じ)。
func openRegistry(stderr io.Writer) (*registry.Registry, error) {
	sd, err := config.ResolveStateDir()
	if err != nil {
		return nil, err
	}
	reg, err := registry.New(config.ProjectsDirFor(sd))
	if reg == nil {
		return nil, err
	}
	if err != nil {
		fmt.Fprintf(stderr, "yagura: warning: registry partial load: %v\n", err)
	}
	return reg, nil
}

// ─── registry read commands ──────────────────────────────────

func cliList(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("list", stderr)
	jsonOut := fs.Bool("json", false, "machine-readable JSON output")
	stage := fs.String("stage", "", "filter by stage (active/maintenance/paused/archived)")
	tag := fs.String("tag", "", "filter by tag (exact, case-insensitive)")
	lang := fs.String("language", "", "filter by language (exact, case-insensitive)")
	limit := fs.Int("limit", 0, "max rows to show (0 = all)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	projects, err := filterProjects(reg, *stage, *tag, *lang, "")
	if err != nil {
		return err
	}
	return emitProjects(stdout, projects, *limit, *jsonOut)
}

func cliSearch(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("search", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	tag := fs.String("tag", "", "filter by tag")
	lang := fs.String("language", "", "filter by language")
	stage := fs.String("stage", "", "filter by stage")
	query := fs.String("query", "", "substring over slug/name/notes/repo/tags")
	limit := fs.Int("limit", 0, "max rows to show (0 = all)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	// 先頭の positional も query として許容(`yagura search foo`)。
	if *query == "" && len(pos) > 0 {
		*query = pos[0]
	}
	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	projects, err := filterProjects(reg, *stage, *tag, *lang, *query)
	if err != nil {
		return err
	}
	return emitProjects(stdout, projects, *limit, *jsonOut)
}

// emitProjects は list/search の共通出力。limit>0 で先頭 limit 件に切り詰め、
// JSON では total/truncated を付与、human では末尾に注記する(MCP list と同じ流儀)。
func emitProjects(stdout io.Writer, projects []*project.Project, limit int, jsonOut bool) error {
	now := time.Now()
	total := len(projects)
	truncated := false
	if limit > 0 && total > limit {
		projects = projects[:limit]
		truncated = true
	}
	if jsonOut {
		payload := listPayload(projects, now)
		if truncated {
			payload["total"] = total
			payload["truncated"] = true
		}
		return emitJSON(stdout, payload)
	}
	humanList(stdout, projects, now)
	if truncated {
		fmt.Fprintf(stdout, "(showing %d of %d; --limit 0 for all)\n", len(projects), total)
	}
	return nil
}

func cliGet(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("get", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: yagura get <slug> [--json]")
	}
	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	p, err := reg.Get(pos[0])
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return fmt.Errorf("not found: %s", pos[0])
		}
		return err
	}
	if *jsonOut {
		return emitJSON(stdout, p)
	}
	humanProject(stdout, p)
	return nil
}

func cliStats(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("stats", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	st := computeStats(reg.List(), time.Now())
	if *jsonOut {
		return emitJSON(stdout, st)
	}
	humanStats(stdout, st)
	return nil
}

// cliToday は `yagura today` を処理する。portfolio の「今日注力すべき」プロジェクトを
// score 順に返す(MCP yagura_today と同一の internal/today.Rank を共有)。token 不要。
func cliToday(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("today", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	limit := fs.Int("limit", 5, "max projects to show (1-50)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *limit < 1 {
		*limit = 1
	}
	if *limit > 50 {
		*limit = 50
	}
	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	now := time.Now()
	items := today.Rank(reg.List(), now, *limit)
	if *jsonOut {
		return emitJSON(stdout, map[string]any{
			"date":  now.Format("2006-01-02"),
			"count": len(items),
			"items": items,
		})
	}
	humanToday(stdout, now, items)
	return nil
}

// cliGraph は `yagura graph <impact|neighbors|stats>` を処理する。registry の
// depends_on から依存グラフを構築して問い合わせる(MCP yagura_graph_* と同一の
// internal/projectgraph を共有)。token 不要、registry 読込のみ。
func cliGraph(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: yagura graph <impact|neighbors|stats> [slug] [--json] [--depth N]")
		return errUsage
	}
	sub := args[0]
	fs := newFlagSet("graph "+sub, stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	depth := fs.Int("depth", 2, "neighbor walk depth (neighbors only, 1-10)")
	rest, err := parseArgs(fs, args[1:])
	if err != nil {
		return errUsage
	}

	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	g := projectgraph.Build(toGraphProjects(reg.List()))

	switch sub {
	case "impact", "neighbors":
		if len(rest) < 1 {
			fmt.Fprintf(stderr, "yagura graph %s: slug required\n", sub)
			return errUsage
		}
		slug := rest[0]
		if sub == "impact" {
			res := g.Impact(slug)
			if *jsonOut {
				return emitJSON(stdout, res)
			}
			humanGraphImpact(stdout, res)
			return nil
		}
		if *depth < 1 {
			*depth = 1
		}
		if *depth > 10 {
			*depth = 10
		}
		res := g.Neighbors(slug, *depth)
		if *jsonOut {
			return emitJSON(stdout, res)
		}
		humanGraphNeighbors(stdout, res)
		return nil
	case "stats":
		out := map[string]any{"stats": g.Stats(), "dangling": g.Dangling()}
		if *jsonOut {
			return emitJSON(stdout, out)
		}
		humanGraphStats(stdout, g.Stats(), g.Dangling())
		return nil
	default:
		fmt.Fprintf(stderr, "yagura graph: unknown subcommand %q (impact|neighbors|stats)\n", sub)
		return errUsage
	}
}

// toGraphProjects は registry の Project を projectgraph の最小 view に畳む
// (internal/mcp の同名 helper と同じマッピング)。
func toGraphProjects(ps []*project.Project) []projectgraph.Project {
	out := make([]projectgraph.Project, 0, len(ps))
	for _, p := range ps {
		out = append(out, projectgraph.Project{Slug: p.Slug, DependsOn: p.DependsOn})
	}
	return out
}

// ─── registry mutation commands (audited) ────────────────────

func cliRegister(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("register", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	displayName := fs.String("display-name", "", "display name (default: slug)")
	lang := fs.String("language", "", "language")
	localPath := fs.String("local-path", "", "local filesystem path")
	tags := fs.String("tags", "", "comma-separated tags")
	dependsOn := fs.String("depends-on", "", "comma-separated dependency slugs")
	stage := fs.String("stage", "", "stage (default: active)")
	priority := fs.Int("priority", 0, "priority 0-5")
	notes := fs.String("notes", "", "free-text notes")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: yagura register <slug> <repository> [flags]")
	}
	slug, repo := pos[0], pos[1]
	dn := *displayName
	if dn == "" {
		dn = slug
	}
	st := project.Stage(strings.ToLower(strings.TrimSpace(*stage)))
	if st == "" {
		st = project.StageActive
	}
	p := &project.Project{
		Slug:        slug,
		DisplayName: dn,
		Repository:  repo,
		Language:    *lang,
		LocalPath:   *localPath,
		Tags:        splitCSV(*tags),
		DependsOn:   splitCSV(*dependsOn),
		Stage:       st,
		Priority:    *priority,
		Notes:       *notes,
	}
	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	if err := reg.Add(p); err != nil {
		if errors.Is(err, registry.ErrAlreadyExists) {
			return fmt.Errorf("already exists: %s", slug)
		}
		return err // project.Validate() のメッセージをそのまま見せる
	}
	auditMutation(stderr, "yagura_register", slug, map[string]any{"via": "cli", "repository": repo})
	if *jsonOut {
		return emitJSON(stdout, map[string]any{"slug": slug, "created": true})
	}
	fmt.Fprintf(stdout, "registered %s\n", slug)
	return nil
}

func cliUpdate(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("update", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	displayName := fs.String("display-name", "", "display name")
	lang := fs.String("language", "", "language")
	localPath := fs.String("local-path", "", "local path")
	tags := fs.String("tags", "", "comma-separated tags")
	dependsOn := fs.String("depends-on", "", "comma-separated dependency slugs")
	stage := fs.String("stage", "", "stage")
	priority := fs.Int("priority", 0, "priority 0-5")
	notes := fs.String("notes", "", "notes")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: yagura update <slug> [flags]")
	}
	slug := pos[0]
	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	cur, err := reg.Get(slug)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return fmt.Errorf("not found: %s", slug)
		}
		return err
	}
	// ユーザが明示的に指定した flag のみ適用(manual-metadata のみ。
	// sensor フィールドは scanner 専用なので CLI からは触れない)。
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["display-name"] {
		cur.DisplayName = *displayName
	}
	if set["language"] {
		cur.Language = *lang
	}
	if set["local-path"] {
		cur.LocalPath = *localPath
	}
	if set["tags"] {
		cur.Tags = splitCSV(*tags)
	}
	if set["depends-on"] {
		cur.DependsOn = splitCSV(*dependsOn)
	}
	if set["stage"] {
		st := project.Stage(strings.ToLower(strings.TrimSpace(*stage)))
		switch st {
		case project.StageActive, project.StageMaintenance,
			project.StagePaused, project.StageArchived:
			cur.Stage = st
		default:
			return fmt.Errorf("stage must be one of active/maintenance/paused/archived")
		}
	}
	if set["priority"] {
		if *priority < 0 || *priority > 5 {
			return fmt.Errorf("priority must be 0-5")
		}
		cur.Priority = *priority
	}
	if set["notes"] {
		cur.Notes = *notes
	}
	if err := reg.Update(cur); err != nil {
		return err
	}
	auditMutation(stderr, "yagura_update", slug, map[string]any{"via": "cli"})
	if *jsonOut {
		return emitJSON(stdout, map[string]any{"slug": slug, "updated": true})
	}
	fmt.Fprintf(stdout, "updated %s\n", slug)
	return nil
}

func cliUnregister(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("unregister", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: yagura unregister <slug>")
	}
	slug := pos[0]
	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	if err := reg.Delete(slug); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return fmt.Errorf("not found: %s", slug)
		}
		return err
	}
	auditMutation(stderr, "yagura_unregister", slug, map[string]any{"via": "cli"})
	if *jsonOut {
		return emitJSON(stdout, map[string]any{"slug": slug, "deleted": true})
	}
	fmt.Fprintf(stdout, "unregistered %s\n", slug)
	return nil
}

// ─── local scan commands ─────────────────────────────────────

func cliSbom(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("sbom", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	summary := fs.Bool("summary", false, "summary only")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	bom, err := sbom.New().Generate(mainModulePath, version)
	if err != nil {
		return err
	}
	if *summary {
		s := bom.Summarize()
		if *jsonOut {
			return emitJSON(stdout, s)
		}
		humanSbomSummary(stdout, s)
		return nil
	}
	if *jsonOut {
		return emitJSON(stdout, bom)
	}
	humanSbom(stdout, bom)
	return nil
}

func cliSecretScan(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("secretscan", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	slug := fs.String("slug", "", "scan only this project (default: all non-archived)")
	minSev := fs.String("min-severity", "", "LOW/MEDIUM/HIGH/CRITICAL")
	rulesFile := fs.String("rules-file", "", "path to custom rules JSON (default: auto-detect .yagura/secretscan.json)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	var toScan []*project.Project
	if *slug != "" {
		p, err := reg.Get(*slug)
		if err != nil {
			if errors.Is(err, registry.ErrNotFound) {
				return fmt.Errorf("not found: %s", *slug)
			}
			return err
		}
		toScan = []*project.Project{p}
	} else {
		for _, p := range reg.List() {
			if p.Stage != project.StageArchived {
				toScan = append(toScan, p)
			}
		}
	}
	var items []secretscan.ScanItem
	for _, p := range toScan {
		items = append(items, projectScanItems(p)...)
	}

	// Custom rules: explicit --rules-file or auto-detect ./.yagura/secretscan.json.
	scanner := secretscan.New()
	rf := *rulesFile
	if rf == "" {
		candidate := filepath.Join(".yagura", "secretscan.json")
		if _, statErr := os.Stat(candidate); statErr == nil {
			rf = candidate
		}
	}
	if rf != "" {
		cfg, loadErr := secretscan.LoadUserConfig(rf)
		if loadErr != nil {
			return fmt.Errorf("custom rules: %w", loadErr)
		}
		rules, applyErr := cfg.Apply(secretscan.DefaultRules())
		if applyErr != nil {
			return fmt.Errorf("apply custom rules: %w", applyErr)
		}
		scanner = secretscan.NewWithRules(rules)
	}

	result := scanner.ScanBatch(items)
	if *minSev != "" {
		m := strings.ToUpper(*minSev)
		if m != "LOW" && m != "MEDIUM" && m != "HIGH" && m != "CRITICAL" {
			return fmt.Errorf("min-severity must be LOW/MEDIUM/HIGH/CRITICAL")
		}
		result = filterBySeverity(result, secretscan.Severity(m))
	}
	if *jsonOut {
		return emitJSON(stdout, map[string]any{
			"scanned_projects": len(toScan),
			"sources_scanned":  len(items),
			"total_findings":   result.Total,
			"by_severity":      result.BySeverity,
			"by_source":        result.BySource,
			"source_order":     result.SourceOrder,
		})
	}
	humanSecretScan(stdout, result, len(toScan), len(items))
	return nil
}

func cliGhaAudit(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("gha-audit", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	dir := fs.String("dir", ".github/workflows", "directory containing workflow YAML files")
	summary := fs.Bool("summary", false, "summary only")
	minSev := fs.String("min-severity", "", "filter findings at or above this severity: LOW/MEDIUM/HIGH/CRITICAL")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	files, err := readWorkflowFiles(*dir)
	if err != nil {
		return err
	}
	results := ghaaudit.New().AuditDir(*dir, files)

	// min-severity filter
	if *minSev != "" {
		m := strings.ToUpper(*minSev)
		if m != "LOW" && m != "MEDIUM" && m != "HIGH" && m != "CRITICAL" {
			return fmt.Errorf("gha-audit: --min-severity must be LOW/MEDIUM/HIGH/CRITICAL, got %q", *minSev)
		}
		results = filterGhaFindings(results, ghaaudit.Severity(m))
	}

	if *summary {
		s := ghaaudit.Summarize(results)
		if *jsonOut {
			return emitJSON(stdout, s)
		}
		humanGhaSummary(stdout, s)
		return nil
	}
	if *jsonOut {
		return emitJSON(stdout, map[string]any{"results": results, "summary": ghaaudit.Summarize(results)})
	}
	humanGhaAudit(stdout, results)
	return nil
}

// filterGhaFindings は severity が min 以上の findings のみ残す。
// severity rank: CRITICAL(0) > HIGH(1) > MEDIUM(2) > LOW(3)。
func filterGhaFindings(results map[string][]ghaaudit.Finding, min ghaaudit.Severity) map[string][]ghaaudit.Finding {
	rank := map[ghaaudit.Severity]int{
		ghaaudit.SeverityCritical: 0,
		ghaaudit.SeverityHigh:     1,
		ghaaudit.SeverityMedium:   2,
		ghaaudit.SeverityLow:      3,
	}
	minRank := rank[min]
	out := make(map[string][]ghaaudit.Finding, len(results))
	for file, findings := range results {
		var kept []ghaaudit.Finding
		for _, f := range findings {
			if rank[f.Severity] <= minRank {
				kept = append(kept, f)
			}
		}
		if len(kept) > 0 {
			out[file] = kept
		}
	}
	return out
}

func cliPinDrift(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("pin-drift", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	dir := fs.String("dir", ".github/workflows", "directory containing workflow YAML files")
	concurrency := fs.Int("concurrency", 4, "parallel API checks")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	// pin-drift だけ GitHub API が必須 → config.Load() で token を検証する。
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pin-drift requires YAGURA_GITHUB_TOKEN (GitHub API SHA verification): %w", err)
	}
	files, err := readWorkflowFiles(*dir)
	if err != nil {
		return err
	}
	var pins []pindrift.Pin
	for path, content := range files {
		pins = append(pins, pindrift.ExtractPins(path, content)...)
	}
	if len(pins) == 0 {
		if *jsonOut {
			return emitJSON(stdout, map[string]any{
				"results": []pindrift.Result{},
				"summary": pindrift.Summary{ByStatus: map[string]int{}},
				"note":    "no SHA-pinned uses found (try gha-audit first)",
			})
		}
		fmt.Fprintln(stdout, "no SHA-pinned uses found (try gha-audit first)")
		return nil
	}
	gh := newGitHubClient(cfg)
	checker := pindrift.New(gh)
	checker.RateLimit = pindrift.NewRateLimitGuard(gh.LastRateLimit)
	if *concurrency < 1 {
		*concurrency = 1
	}
	results := checker.CheckPinsParallel(context.Background(), pins, *concurrency)
	if *jsonOut {
		return emitJSON(stdout, map[string]any{"results": results, "summary": pindrift.Summarize(results)})
	}
	humanPinDrift(stdout, results)
	return nil
}

// skillAuditEntry は 1 つの SKILL.md の監査結果(path 付き)。
type skillAuditEntry struct {
	Path string `json:"path"`
	harness.SkillAuditResult
}

// cliSkillAudit は dir 配下の SKILL.md を走査し、品質スコアと retire 候補を
// まとめて報告する(MUSE-Autoskill のライブラリ単位の self-cleaning を CLI で
// 実用化)。disk 走査なので MCP の単発 yagura_skill_audit ではなく CLI 側に置く。
// minScoreGate は --min-score 用の CI ゲート。score < min の item があれば error
// を返す(min<=0 で無効)。結果自体は呼び出し側で既に出力済み。決定論的な順序。
func minScoreGate(min int, scored map[string]int) error {
	if min <= 0 {
		return nil
	}
	var below []string
	for path, sc := range scored {
		if sc < min {
			below = append(below, fmt.Sprintf("%s (%d)", path, sc))
		}
	}
	if len(below) == 0 {
		return nil
	}
	sort.Strings(below)
	return fmt.Errorf("%d item(s) below --min-score %d: %s", len(below), min, strings.Join(below, ", "))
}

// auditResolveOpts は resolveSingleAuditTarget の入力を束ねる構造体。
type auditResolveOpts struct {
	FileFlag string
	Rest     []string
	JSONOut  bool
	ItemsKey string
}

// auditTargetResult は resolveSingleAuditTarget の出力を束ねる構造体。
type auditTargetResult struct {
	Path    string
	Content string
	Handled bool // true = 呼出側は return err で即抜け
}

// resolveSingleAuditTarget は単一ファイル audit(agent-config / plugin / mcp)で
// 共通の前段を担う: 対象パスを解決(FileFlag を Rest[0] で上書き)し、
//   - ファイル不在なら "graceful zero"(scanned:0 / flagged:0 / ItemsKey:[])を
//     出力して Handled=true を返す(未配置リポジトリでも素直に 0 件)。
//   - 在れば Content を読み出して返す(read 失敗は "read <p>:" で wrap)。
//
// 呼出側は `if tgt.Handled || err != nil { return err }` で分岐する。
func resolveSingleAuditTarget(stdout io.Writer, opts auditResolveOpts) (auditTargetResult, error) {
	path := opts.FileFlag
	if len(opts.Rest) > 0 {
		path = opts.Rest[0]
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if opts.JSONOut {
			return auditTargetResult{Path: path, Handled: true},
				emitJSON(stdout, map[string]any{"scanned": 0, "flagged": 0, opts.ItemsKey: []any{}})
		}
		fmt.Fprintln(stdout, "scanned: 0   flagged: 0")
		return auditTargetResult{Path: path, Handled: true}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return auditTargetResult{Path: path}, fmt.Errorf("read %s: %w", path, err)
	}
	return auditTargetResult{Path: path, Content: string(data)}, nil
}

func cliSkillAudit(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("skill-audit", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	dir := fs.String("dir", ".claude/skills", "directory to scan for SKILL.md files")
	retireOnly := fs.Bool("retire-only", false, "show only retire candidates")
	minScore := fs.Int("min-score", 0, "exit non-zero if any skill scores below N (CI gate; 0=off)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	paths, err := findSkillFiles(*dir)
	if err != nil {
		return err
	}
	entries := make([]skillAuditEntry, 0, len(paths))
	scored := make(map[string]int, len(paths))
	retireCount := 0
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		res := harness.AuditSkill(string(data))
		scored[p] = res.Score
		if res.RetireRecommended {
			retireCount++
		}
		if *retireOnly && !res.RetireRecommended {
			continue
		}
		entries = append(entries, skillAuditEntry{Path: p, SkillAuditResult: res})
	}
	if *jsonOut {
		if err := emitJSON(stdout, map[string]any{
			"scanned":           len(paths),
			"retire_candidates": retireCount,
			"skills":            entries,
		}); err != nil {
			return err
		}
	} else {
		humanSkillAudit(stdout, entries, len(paths), retireCount)
	}
	return minScoreGate(*minScore, scored)
}

// findSkillFiles は dir 配下(再帰)の "SKILL.md" を slug 昇順で返す。
// dir が存在しなければ空(エラーにしない — 未導入リポジトリでも素直に 0 件)。
// walkFiles は dir 配下を再帰走査し、match した file path を昇順で返す。
//
// find*Files 系(skill/workflow/vex/publicity scan)が個別に持っていた
// 「Stat→WalkDir→sort」の同型ループの一本化。挙動は従来と同一:
// dir 不在は (nil, nil)(未配置リポジトリでも audit を 0 件で通すため)、
// 走査エラーは "walk <dir>: " で wrap、結果は決定論的に sort 済み。
func walkFiles(dir string, match func(path string, d fs.DirEntry) bool) ([]string, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && match(p, d) {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	sort.Strings(paths)
	return paths, nil
}

func findSkillFiles(dir string) ([]string, error) {
	return walkFiles(dir, func(_ string, d fs.DirEntry) bool {
		return d.Name() == "SKILL.md"
	})
}

// workflowAuditEntry は 1 つの workflow ファイルの監査結果(path 付き)。
type workflowAuditEntry struct {
	Path string `json:"path"`
	harness.WorkflowAuditResult
}

// cliWorkflowAudit は dir 配下の Dynamic Workflow(*.js / *.mjs)を走査し、
// Anthropic launch 記事の "token を浪費する mistakes" を構造ベースで検出する。
// skill-audit と同じく disk 走査なので MCP tool は増やさず CLI 側に置く。
func cliWorkflowAudit(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("workflow-audit", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	dir := fs.String("dir", ".claude/workflows", "directory to scan for workflow .js/.mjs files")
	flaggedOnly := fs.Bool("flagged-only", false, "show only workflows with at least one issue")
	minScore := fs.Int("min-score", 0, "exit non-zero if any workflow scores below N (CI gate; 0=off)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	paths, err := findWorkflowFiles(*dir)
	if err != nil {
		return err
	}
	entries := make([]workflowAuditEntry, 0, len(paths))
	scored := make(map[string]int, len(paths))
	flaggedCount := 0
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		res := harness.AuditWorkflow(string(data))
		scored[p] = res.Score
		if len(res.Issues) > 0 {
			flaggedCount++
		}
		if *flaggedOnly && len(res.Issues) == 0 {
			continue
		}
		entries = append(entries, workflowAuditEntry{Path: p, WorkflowAuditResult: res})
	}
	if *jsonOut {
		if err := emitJSON(stdout, map[string]any{
			"scanned":   len(paths),
			"flagged":   flaggedCount,
			"workflows": entries,
		}); err != nil {
			return err
		}
	} else {
		humanWorkflowAudit(stdout, entries, len(paths), flaggedCount)
	}
	return minScoreGate(*minScore, scored)
}

// findWorkflowFiles は dir 配下(再帰)の *.js / *.mjs を path 昇順で返す。
// dir が存在しなければ空(エラーにしない — 未導入リポジトリでも素直に 0 件)。
func findWorkflowFiles(dir string) ([]string, error) {
	return walkFiles(dir, func(p string, _ fs.DirEntry) bool {
		ext := filepath.Ext(p)
		return ext == ".js" || ext == ".mjs"
	})
}

// settingsAuditEntry は 1 つの settings.json の監査結果(path 付き)。
type settingsAuditEntry struct {
	Path string `json:"path"`
	harness.SettingsAuditResult
}

// cliSettingsAudit は dir 配下の settings.json / settings.local.json を走査し、
// Boris の customization ガイドが説く security/sharing best practice(permissions
// を check-in、破壊的コマンドを deny、無制限 allow を避ける)を構造ベースで検査する。
// skill-audit / workflow-audit と同じく disk 走査なので CLI 側に置き、MCP tool は
// 増やさない。
func cliSettingsAudit(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("settings-audit", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	dir := fs.String("dir", ".claude", "directory to scan for settings.json / settings.local.json")
	minScore := fs.Int("min-score", 0, "exit non-zero if any settings file scores below N (CI gate; 0=off)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	paths := findSettingsFiles(*dir)
	entries := make([]settingsAuditEntry, 0, len(paths))
	scored := make(map[string]int, len(paths))
	flaggedCount := 0
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		res := harness.AuditSettings(string(data))
		scored[p] = res.Score
		if len(res.Issues) > 0 {
			flaggedCount++
		}
		entries = append(entries, settingsAuditEntry{Path: p, SettingsAuditResult: res})
	}
	if *jsonOut {
		if err := emitJSON(stdout, map[string]any{
			"scanned":  len(paths),
			"flagged":  flaggedCount,
			"settings": entries,
		}); err != nil {
			return err
		}
	} else {
		humanSettingsAudit(stdout, entries, len(paths), flaggedCount)
	}
	return minScoreGate(*minScore, scored)
}

// findSettingsFiles は dir 直下の settings.json / settings.local.json のうち実在
// するものを path 昇順で返す(再帰しない — settings は scope ごとに dir 直下に置く)。
// dir が無ければ空(エラーにしない)。
func findSettingsFiles(dir string) []string {
	var paths []string
	for _, name := range []string{"settings.json", "settings.local.json"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

// cliAgentConfigAudit は OpenClaw 系 multi-provider エージェント設定(openclaw.json)を
// 監査する。OS を触る自律エージェントを安全に運用するための security/reliability foot-gun
// (LAN 公開認証 / sandbox / secret 直書き / context・compaction・モデル参照不整合)を検出。
// 単一ファイル監査なので dir 走査ではなく --file(or 位置引数)で対象を取る。
func cliAgentConfigAudit(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("agent-config-audit", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	file := fs.String("file", "openclaw.json", "path to the OpenClaw-style agent config JSON")
	minScore := fs.Int("min-score", 0, "exit non-zero if the config scores below N (CI gate; 0=off)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	tgt, err := resolveSingleAuditTarget(stdout, auditResolveOpts{FileFlag: *file, Rest: rest, JSONOut: *jsonOut, ItemsKey: "configs"})
	if tgt.Handled || err != nil {
		return err
	}
	path, content := tgt.Path, tgt.Content
	res := harness.AuditAgentConfig(content)
	flagged := 0
	if len(res.Issues) > 0 {
		flagged = 1
	}
	entry := agentConfigAuditEntry{Path: path, AgentConfigAuditResult: res}
	if *jsonOut {
		if err := emitJSON(stdout, map[string]any{
			"scanned": 1,
			"flagged": flagged,
			"configs": []agentConfigAuditEntry{entry},
		}); err != nil {
			return err
		}
	} else {
		humanAgentConfigAudit(stdout, entry, flagged)
	}
	return minScoreGate(*minScore, map[string]int{path: res.Score})
}

// agentConfigAuditEntry は 1 つの openclaw.json の監査結果(path 付き)。
type agentConfigAuditEntry struct {
	Path string `json:"path"`
	harness.AgentConfigAuditResult
}

// cliPluginAudit は Claude Code プラグイン / マーケットプレイス manifest を監査する。
// content から plugin / marketplace を自動判定。単一ファイルなので --file(or 位置引数)。
func cliPluginAudit(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("plugin-audit", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	file := fs.String("file", ".claude-plugin/plugin.json", "path to plugin.json or marketplace.json")
	minScore := fs.Int("min-score", 0, "exit non-zero if the manifest scores below N (CI gate; 0=off)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	tgt, err := resolveSingleAuditTarget(stdout, auditResolveOpts{FileFlag: *file, Rest: rest, JSONOut: *jsonOut, ItemsKey: "manifests"})
	if tgt.Handled || err != nil {
		return err
	}
	path, content := tgt.Path, tgt.Content
	res := harness.AuditPluginManifest(content)
	flagged := 0
	if len(res.Issues) > 0 {
		flagged = 1
	}
	entry := pluginAuditEntry{Path: path, PluginAuditResult: res}
	if *jsonOut {
		if err := emitJSON(stdout, map[string]any{
			"scanned":   1,
			"flagged":   flagged,
			"manifests": []pluginAuditEntry{entry},
		}); err != nil {
			return err
		}
	} else {
		humanPluginAudit(stdout, entry, flagged)
	}
	return minScoreGate(*minScore, map[string]int{path: res.Score})
}

// pluginAuditEntry は 1 つの plugin/marketplace manifest の監査結果(path 付き)。
type pluginAuditEntry struct {
	Path string `json:"path"`
	harness.PluginAuditResult
}

// mcpAuditEntry は 1 つの .mcp.json / tools 定義の監査結果(path 付き)。
type mcpAuditEntry struct {
	Path string `json:"path"`
	harness.MCPAuditResult
}

// cliMCPAudit は .mcp.json / tools 定義を tool-poisoning & 設定リスクで監査する。
// content から server/tools を自動判定。単一ファイルなので --file(or 位置引数)。
func cliMCPAudit(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("mcp-audit", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	file := fs.String("file", ".mcp.json", "path to .mcp.json or a tools/list JSON")
	minScore := fs.Int("min-score", 0, "exit non-zero if it scores below N (CI gate; 0=off)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	tgt, err := resolveSingleAuditTarget(stdout, auditResolveOpts{FileFlag: *file, Rest: rest, JSONOut: *jsonOut, ItemsKey: "configs"})
	if tgt.Handled || err != nil {
		return err
	}
	path, content := tgt.Path, tgt.Content
	res := harness.AuditMCPConfig(content)
	flagged := 0
	if len(res.Issues) > 0 {
		flagged = 1
	}
	entry := mcpAuditEntry{Path: path, MCPAuditResult: res}
	if *jsonOut {
		if err := emitJSON(stdout, map[string]any{
			"scanned": 1,
			"flagged": flagged,
			"configs": []mcpAuditEntry{entry},
		}); err != nil {
			return err
		}
	} else {
		humanMCPAudit(stdout, entry, flagged)
	}
	return minScoreGate(*minScore, map[string]int{path: res.Score})
}

// publicityFinding は path 付きの publicity leak 1 件。
type publicityFinding struct {
	Path string `json:"path"`
	publicityscan.Finding
}

// publicityTextExts は publicity-scan が走査するテキスト拡張子。
var publicityTextExts = map[string]bool{
	".md": true, ".mdx": true, ".txt": true, ".json": true, ".yaml": true,
	".yml": true, ".toml": true, ".sh": true, ".env": true, ".example": true,
}

// cliPublicityScan は公開前 leak チェック(absolute home パス / 内部 hostname /
// private IP / email)を行う。SKILL.md 等を public repo へ出す前の publicity-review
// ゲートを CLI 化(secretscan の credential 検出を補完)。path はファイル or dir。
func cliPublicityScan(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("publicity-scan", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	dir := fs.String("dir", ".claude", "file or directory to scan (text files only)")
	strict := fs.Bool("strict", false, "exit non-zero if any finding is reported (for CI gates)")
	minSev := fs.String("min-severity", "low", "minimum severity to report: high|medium|low")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	target := *dir
	if len(rest) > 0 {
		target = rest[0]
	}
	pubRank := map[publicityscan.Severity]int{
		publicityscan.SevHigh: 0, publicityscan.SevMedium: 1, publicityscan.SevLow: 2,
	}
	minPubSev, err := parsePubSeverity(*minSev)
	if err != nil {
		fmt.Fprintf(stderr, "publicity-scan: --min-severity must be high|medium|low, got %q\n", *minSev)
		return errUsage
	}
	paths, err := findScanFiles(target)
	if err != nil {
		return err
	}
	var findings []publicityFinding
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		for _, f := range publicityscan.Scan(string(data)) {
			if pubRank[f.Severity] <= pubRank[minPubSev] {
				findings = append(findings, publicityFinding{Path: p, Finding: f})
			}
		}
	}
	if *jsonOut {
		bare := make([]publicityscan.Finding, len(findings))
		for i, f := range findings {
			bare[i] = f.Finding
		}
		if err := emitJSON(stdout, map[string]any{
			"scanned":  len(paths),
			"findings": findings,
			"summary":  publicityscan.Summarize(bare),
		}); err != nil {
			return err
		}
	} else {
		humanPublicityScan(stdout, findings, len(paths))
	}
	// --strict: leak が 1 件でもあれば CI を落とす(findings は既に出力済み)。
	if *strict && len(findings) > 0 {
		return fmt.Errorf("%d publicity finding(s) — failing because --strict is set", len(findings))
	}
	return nil
}

// parsePubSeverity は publicity-scan の --min-severity 値を publicityscan.Severity へ変換する。
func parsePubSeverity(s string) (publicityscan.Severity, error) {
	switch strings.ToLower(s) {
	case "high":
		return publicityscan.SevHigh, nil
	case "medium":
		return publicityscan.SevMedium, nil
	case "low":
		return publicityscan.SevLow, nil
	default:
		return "", fmt.Errorf("unknown severity %q", s)
	}
}

// findScanFiles は target がファイルならそれ 1 件、dir なら配下のテキストファイルを
// path 昇順で返す。存在しなければ空(エラーにしない — 他 audit と同じ graceful 挙動)。
func findScanFiles(target string) ([]string, error) {
	info, err := os.Stat(target)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{target}, nil
	}
	return walkFiles(target, func(p string, _ fs.DirEntry) bool {
		return publicityTextExts[strings.ToLower(filepath.Ext(p))]
	})
}

// vexAuditEntry は 1 つの OpenVEX JSON ファイルの検証結果(path 付き)。
type vexAuditEntry struct {
	Path       string   `json:"path"`
	OK         bool     `json:"ok"`
	Statements int      `json:"statements"`
	Issues     []string `json:"issues,omitempty"`
	Error      string   `json:"error,omitempty"` // JSON parse 失敗時のみ
}

// cliVexAudit は dir 配下の OpenVEX *.json(security-spec.md が mandate する
// docs/vex/vex-CVE-*.json)を構造検証する。spec が公開を義務付ける VEX 文書が
// rot(不正 status / justification 欠落 / 壊れた JSON)しないよう CI で守るための
// gate。disk 走査なので skill-audit 等と同じく CLI 側に置く(MCP tool は増やさない)。
// OSPS-VM-04.02(非該当脆弱性を VEX 形式で文書化)の検証点。
func cliVexAudit(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("vex-audit", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	dir := fs.String("dir", "docs/vex", "directory to scan for OpenVEX *.json files")
	strict := fs.Bool("strict", false, "exit non-zero if any file fails validation (CI gate)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	target := *dir
	if len(rest) > 0 {
		target = rest[0]
	}
	paths, err := findVexFiles(target)
	if err != nil {
		return err
	}
	entries := make([]vexAuditEntry, 0, len(paths))
	flagged := 0
	for _, p := range paths {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", p, rerr)
		}
		doc, issues, perr := vex.ParseAndValidate(data)
		e := vexAuditEntry{Path: p, Statements: len(doc.Statements), Issues: issues}
		if perr != nil {
			e.Error = perr.Error()
			e.OK = false
		} else {
			e.OK = len(issues) == 0
		}
		if !e.OK {
			flagged++
		}
		entries = append(entries, e)
	}
	if *jsonOut {
		if err := emitJSON(stdout, map[string]any{
			"scanned": len(paths),
			"flagged": flagged,
			"files":   entries,
		}); err != nil {
			return err
		}
	} else {
		humanVexAudit(stdout, entries, len(paths), flagged)
	}
	if *strict && flagged > 0 {
		return fmt.Errorf("%d VEX file(s) failed validation — failing because --strict is set", flagged)
	}
	return nil
}

// findVexFiles は dir 配下(再帰)の *.json を path 昇順で返す。
// dir が無ければ空(エラーにしない — 他 audit と同じ graceful 挙動)。
func findVexFiles(dir string) ([]string, error) {
	return walkFiles(dir, func(p string, _ fs.DirEntry) bool {
		return strings.EqualFold(filepath.Ext(p), ".json")
	})
}

// cliSelfImproveHistory は append-only 監査ログから `self_improve` record を時系列で
// 読み出し、自己改善の軌跡(severity 別件数・提案数・self_collected)を表示する。
// record=true で yagura_self_improve が刻んだ「auditable memory」を読み戻して、
// 連続する評価を diff し収束(非 misevolution)を確認するための窓口(CI/script 向け)。
// disk 走査 + token 不要なので CLI direct mode に置く。
func cliSelfImproveHistory(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("self-improve-history", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	limit := fs.Int("limit", 0, "show only the last N assessments (0 = all)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	sd, err := config.ResolveStateDir()
	if err != nil {
		return err
	}
	recs, err := audit.Read(config.AuditDirFor(sd), "self_improve")
	if err != nil {
		return err
	}
	if *limit > 0 && len(recs) > *limit {
		recs = recs[len(recs)-*limit:]
	}
	if *jsonOut {
		return emitJSON(stdout, map[string]any{"count": len(recs), "assessments": recs})
	}
	humanSelfImproveHistory(stdout, recs)
	return nil
}

// cliPathPolicy は変更パス集合を glob policy で deny/review/allow に判定する。
// policy は JSON ファイル(--policy、既定 .yagura/paths.json)。変更パスは位置引数 /
// --changed CSV / stdin(空白区切り行、`git diff --name-only | yagura path-policy` 用)。
// --strict は deny が 1 件でもあれば非ゼロ終了(CI gate)。disk(policy)読み + token 不要。
func cliPathPolicy(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("path-policy", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	policyPath := fs.String("policy", ".yagura/paths.json", "path to the JSON policy file")
	changedCSV := fs.String("changed", "", "comma-separated changed paths (else positional args or stdin)")
	strict := fs.Bool("strict", false, "exit non-zero if any path is denied (CI gate)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	data, err := os.ReadFile(*policyPath)
	if err != nil {
		return fmt.Errorf("read policy %s: %w", *policyPath, err)
	}
	var pol pathpolicy.Policy
	if err := json.Unmarshal(data, &pol); err != nil {
		return fmt.Errorf("parse policy %s: %w", *policyPath, err)
	}
	if err := pol.Validate(); err != nil {
		return err
	}

	changed := splitCSV(*changedCSV)
	changed = append(changed, pos...)
	if len(changed) == 0 {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				changed = append(changed, line)
			}
		}
	}
	if len(changed) == 0 {
		return fmt.Errorf("no changed paths (pass as args, --changed, or via stdin)")
	}

	res := pathpolicy.Evaluate(pol, changed)
	if *jsonOut {
		if err := emitJSON(stdout, res); err != nil {
			return err
		}
	} else {
		humanPathPolicy(stdout, res)
	}
	if *strict && res.Worst == pathpolicy.ActionDeny {
		return fmt.Errorf("%d path(s) denied by policy — failing because --strict is set", len(res.Denied))
	}
	return nil
}

// injectFinding は path 付きの間接インジェクション検出 1 件。
type injectFinding struct {
	Path string `json:"path"`
	injectscan.Finding
}

// cliInjectScan は untrusted content(fetch/取り込んだファイル)に潜む間接プロンプト
// インジェクションのシグナルを検出する。path はファイル or dir(テキストのみ)。
// --strict で 1 件でも検出があれば非ゼロ終了(信頼境界 gate)。LLM 非依存のサーバ層検査。
func cliInjectScan(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("inject-scan", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	dir := fs.String("dir", ".", "file or directory to scan (text files only)")
	strict := fs.Bool("strict", false, "exit non-zero if any injection signal is found (CI gate)")
	minScore := fs.Int("min-score", 0, "exit non-zero if any file scores below N (0=off)")
	minSev := fs.String("min-severity", "low", "minimum severity to report: critical|high|medium|low")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	injectRank := map[injectscan.Severity]int{
		injectscan.SevCritical: 0, injectscan.SevHigh: 1,
		injectscan.SevMedium: 2, injectscan.SevLow: 3,
	}
	minInjectSev, err := parseInjectSeverity(*minSev)
	if err != nil {
		fmt.Fprintf(stderr, "inject-scan: --min-severity must be critical|high|medium|low, got %q\n", *minSev)
		return errUsage
	}
	target := *dir
	if len(rest) > 0 {
		target = rest[0]
	}
	paths, err := findScanFiles(target)
	if err != nil {
		return err
	}
	var findings []injectFinding
	scored := make(map[string]int, len(paths))
	for _, p := range paths {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", p, rerr)
		}
		res := injectscan.Scan(string(data))
		scored[p] = res.Score
		for _, f := range res.Findings {
			if injectRank[f.Severity] <= injectRank[minInjectSev] {
				findings = append(findings, injectFinding{Path: p, Finding: f})
			}
		}
	}
	if *jsonOut {
		if err := emitJSON(stdout, map[string]any{
			"scanned":  len(paths),
			"findings": findings,
		}); err != nil {
			return err
		}
	} else {
		humanInjectScan(stdout, findings, len(paths))
	}
	if *strict && len(findings) > 0 {
		return fmt.Errorf("%d prompt-injection signal(s) — failing because --strict is set", len(findings))
	}
	return minScoreGate(*minScore, scored)
}

// parseInjectSeverity は inject-scan の --min-severity 値を injectscan.Severity へ変換する。
func parseInjectSeverity(s string) (injectscan.Severity, error) {
	switch strings.ToLower(s) {
	case "critical":
		return injectscan.SevCritical, nil
	case "high":
		return injectscan.SevHigh, nil
	case "medium":
		return injectscan.SevMedium, nil
	case "low":
		return injectscan.SevLow, nil
	default:
		return "", fmt.Errorf("unknown severity %q", s)
	}
}

// cliCCSecurity は Claude Code を使うプロジェクトの「最低限のセキュリティ対策」
// 姿勢を監査する。dir 配下のファイル構成・設定を集めて ccsecurity.Audit に渡す。
// 機械判定できない人手プロセス項目はガイダンスとして併記する。
func cliCCSecurity(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("cc-security", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	dir := fs.String("dir", ".", "project directory to audit")
	minScore := fs.Int("min-score", 0, "exit non-zero if score < N (0=off, for CI gates)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	target := *dir
	if len(rest) > 0 {
		target = rest[0]
	}
	in, err := gatherCCSecurityInput(target)
	if err != nil {
		return err
	}
	report := ccsecurity.Audit(in)
	if *jsonOut {
		if err := emitJSON(stdout, report); err != nil {
			return err
		}
	} else {
		humanCCSecurity(stdout, report)
	}
	if *minScore > 0 && report.Score < *minScore {
		return fmt.Errorf("cc-security score %d is below --min-score %d", report.Score, *minScore)
	}
	return nil
}

// gatherCCSecurityInput はプロジェクト dir からセキュリティ監査に必要な事実を集める。
// 存在しないファイルはエラーにせず欠落として扱う(他 audit と同じ graceful 挙動)。
func gatherCCSecurityInput(dir string) (ccsecurity.Input, error) {
	in := ccsecurity.Input{}

	// .env 系ファイルの検出(dir 直下)。
	entries, err := os.ReadDir(dir)
	if err != nil {
		return in, fmt.Errorf("read dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == ".env" || strings.HasPrefix(name, ".env.") || strings.HasSuffix(name, ".env") {
			in.EnvFiles = append(in.EnvFiles, name)
		}
	}
	sort.Strings(in.EnvFiles)

	// 主要ファイルの読み込み(best-effort)。
	readIfExists := func(rel string) (string, bool) {
		data, rerr := os.ReadFile(filepath.Join(dir, rel))
		if rerr != nil {
			return "", false
		}
		return string(data), true
	}
	if c, ok := readIfExists(".gitignore"); ok {
		in.HasGitignore, in.Gitignore = true, c
	}
	if c, ok := readIfExists("CLAUDE.md"); ok {
		in.HasClaudeMd, in.ClaudeMd = true, c
	}
	if c, ok := readIfExists(filepath.Join(".claude", "settings.json")); ok {
		in.HasSettings, in.SettingsJSON = true, c
		in.MCPServerCount = countMCPServers(c)
	}
	if _, ok := readIfExists("WORKLOG.md"); ok {
		in.HasWorklog = true
	}
	if fi, serr := os.Stat(filepath.Join(dir, ".git")); serr == nil && fi.IsDir() {
		in.HasGitDir = true
	}

	// --dangerously-skip-permissions 走査用に、shell / Makefile / md を集める。
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".sh" || ext == ".bash" || name == "Makefile" || ext == ".md" {
			if data, rerr := os.ReadFile(filepath.Join(dir, name)); rerr == nil {
				in.ExtraText = append(in.ExtraText, ccsecurity.NamedText{Name: name, Text: string(data)})
			}
		}
	}
	return in, nil
}

// cliClaudeMdAudit は CLAUDE.md の構造(4 セクション / 命令数 / Lost in the Middle)
// を監査する。引数省略時は ./CLAUDE.md を対象にする。
func cliClaudeMdAudit(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("claudemd-audit", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	file := fs.String("file", "CLAUDE.md", "path to the CLAUDE.md to audit")
	minScore := fs.Int("min-score", 0, "exit non-zero if score < N (0=off, for CI gates)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	target := *file
	if len(rest) > 0 {
		target = rest[0]
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("read %s: %w", target, err)
	}
	res := harness.AuditClaudeMd(string(data))
	if *jsonOut {
		if err := emitJSON(stdout, res); err != nil {
			return err
		}
	} else {
		humanClaudeMdAudit(stdout, target, res)
	}
	if *minScore > 0 && res.Score < *minScore {
		return fmt.Errorf("claudemd-audit score %d is below --min-score %d", res.Score, *minScore)
	}
	return nil
}

// countMCPServers は settings.json の mcpServers エントリ数を返す(失敗時 0)。
func countMCPServers(settingsJSON string) int {
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(settingsJSON), &doc); err != nil {
		return 0
	}
	return len(doc.MCPServers)
}

// ─── shared helpers ──────────────────────────────────────────

// filterProjects は stage/tag/language/query で registry を絞り込む。
// すべて空なら List()(全件)を返す。internal/mcp の search/list と同じ条件。
func filterProjects(reg *registry.Registry, stage, tag, lang, query string) ([]*project.Project, error) {
	var st project.Stage
	if stage != "" {
		st = project.Stage(strings.ToLower(strings.TrimSpace(stage)))
		switch st {
		case project.StageActive, project.StageMaintenance,
			project.StagePaused, project.StageArchived:
		default:
			return nil, fmt.Errorf("stage must be one of active/maintenance/paused/archived")
		}
	}
	if stage == "" && tag == "" && lang == "" && query == "" {
		return reg.List(), nil
	}
	return reg.Filter(func(p *project.Project) bool {
		if tag != "" && !p.HasTag(tag) {
			return false
		}
		if lang != "" && !strings.EqualFold(p.Language, lang) {
			return false
		}
		if stage != "" && p.Stage != st {
			return false
		}
		if query != "" && !p.MatchesQuery(query) {
			return false
		}
		return true
	}), nil
}

// splitCSV は "a, b ,c" → ["a","b","c"]。空文字は nil。
func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// projectScanItems は internal/mcp.projectFieldsAsScanItems と同じ変換
// (Project のテキストフィールド → ScanItem)。
func projectScanItems(p *project.Project) []secretscan.ScanItem {
	var items []secretscan.ScanItem
	add := func(field, text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		items = append(items, secretscan.ScanItem{Source: p.Slug + ":" + field, Text: text})
	}
	add("display_name", p.DisplayName)
	add("notes", p.Notes)
	add("tags", strings.Join(p.Tags, " "))
	if p.Sprint != nil {
		add("sprint.goal", p.Sprint.Goal)
		for i, m := range p.Sprint.Milestones {
			add(fmt.Sprintf("sprint.milestone[%d]", i), m.Title)
		}
	}
	return items
}

// filterBySeverity は min 以上の severity だけを残した BatchResult を返す。
// internal/mcp.filterFindingsBatch と同じロジック。
func filterBySeverity(r secretscan.BatchResult, min secretscan.Severity) secretscan.BatchResult {
	minRank := severityRank(min)
	out := secretscan.BatchResult{
		BySource:    map[string][]secretscan.Finding{},
		SourceOrder: []string{},
		BySeverity:  map[string]int{},
	}
	for _, src := range r.SourceOrder {
		var keep []secretscan.Finding
		for _, f := range r.BySource[src] {
			if severityRank(f.Severity) >= minRank {
				keep = append(keep, f)
				out.BySeverity[string(f.Severity)]++
				out.Total++
			}
		}
		if len(keep) > 0 {
			out.BySource[src] = keep
			out.SourceOrder = append(out.SourceOrder, src)
		}
	}
	return out
}

func severityRank(s secretscan.Severity) int {
	switch s {
	case secretscan.SeverityCritical:
		return 4
	case secretscan.SeverityHigh:
		return 3
	case secretscan.SeverityMedium:
		return 2
	case secretscan.SeverityLow:
		return 1
	default:
		return 0
	}
}

// ─── ai-verify (v0.36.0) ─────────────────────────────────────

// cliAIVerify は `yagura ai-verify [dir]` を処理する。
// デフォルト rule set に加え、dir/.yagura/aiverify.json があれば自動的に
// カスタムルールをマージする。--rules-file で明示的にファイルを指定することも可能。
func cliAIVerify(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("ai-verify", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	summaryOnly := fset.Bool("summary-only", false, "summary only (no per-finding list)")
	dir := fset.String("dir", ".", "directory to scan recursively")
	rulesFile := fset.String("rules-file", "", "path to custom rules JSON (default: auto-detect <dir>/.yagura/aiverify.json)")
	minRisk := fset.String("min-risk", "", "filter findings at or above this risk: LOW/MEDIUM/HIGH/CRITICAL")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if *minRisk != "" {
		m := strings.ToUpper(*minRisk)
		if m != "LOW" && m != "MEDIUM" && m != "HIGH" && m != "CRITICAL" {
			return fmt.Errorf("ai-verify: --min-risk must be LOW/MEDIUM/HIGH/CRITICAL, got %q", *minRisk)
		}
		*minRisk = m
	}

	sr, err := readSourceFiles(*dir)
	if err != nil {
		return fmt.Errorf("read source files: %w", err)
	}
	warnIncompleteScan(stderr, sr, *dir)
	files := sr.Files

	rules := aiverify.DefaultRules()

	// Resolve custom rules file: explicit flag or auto-detect.
	rf := *rulesFile
	if rf == "" {
		candidate := filepath.Join(*dir, ".yagura", "aiverify.json")
		if _, statErr := os.Stat(candidate); statErr == nil {
			rf = candidate
		}
	}
	if rf != "" {
		cfg, loadErr := aiverify.LoadUserConfig(rf)
		if loadErr != nil {
			return fmt.Errorf("custom rules: %w", loadErr)
		}
		rules, err = cfg.Apply(rules)
		if err != nil {
			return fmt.Errorf("apply custom rules: %w", err)
		}
	}

	res := aiverify.ScanWithRules(files, rules)

	// min-risk filter: keep only findings at or above threshold.
	if *minRisk != "" {
		res = filterAIVerifyByRisk(res, aiverify.RiskLevel(*minRisk))
	}

	if *jsonOut {
		return emitJSON(stdout, res)
	}
	if *summaryOnly {
		humanAIVerifySummary(stdout, res)
	} else {
		humanAIVerify(stdout, res)
	}
	return nil
}

// filterAIVerifyByRisk は risk が min 以上の findings のみ残し集計を再計算する。
func filterAIVerifyByRisk(r aiverify.Result, min aiverify.RiskLevel) aiverify.Result {
	rank := map[aiverify.RiskLevel]int{
		aiverify.RiskCritical: 0,
		aiverify.RiskHigh:     1,
		aiverify.RiskMedium:   2,
		aiverify.RiskLow:      3,
	}
	minRank := rank[min]
	kept := r.Findings[:0]
	for _, f := range r.Findings {
		if rank[f.Risk] <= minRank {
			kept = append(kept, f)
		}
	}
	r.Findings = kept
	// recalculate BySeverity, HasCritical
	r.BySeverity = make(map[aiverify.RiskLevel]int)
	r.HasCritical = false
	for _, f := range kept {
		r.BySeverity[f.Risk]++
		if f.Risk == aiverify.RiskCritical {
			r.HasCritical = true
		}
	}
	return r
}

// ─── quality-check (v0.36.0) ─────────────────────────────────

// cliQualityCheck は `yagura quality-check [dir]` を処理する。
// dir/.yagura/quality.json があれば自動的にカスタムルールをマージする。
func cliQualityCheck(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("quality-check", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	summaryOnly := fset.Bool("summary-only", false, "summary only (no per-finding list)")
	dir := fset.String("dir", ".", "directory to scan recursively")
	rulesFile := fset.String("rules-file", "", "path to custom rules JSON [{id,pattern,severity,languages?,description?,suggestion?}]")
	minSev := fset.String("min-severity", "", "filter findings at or above this severity: info/warning/prohibited")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if *minSev != "" {
		switch strings.ToLower(*minSev) {
		case "info", "warning", "prohibited":
			*minSev = strings.ToLower(*minSev)
		default:
			return fmt.Errorf("quality-check: --min-severity must be info/warning/prohibited, got %q", *minSev)
		}
	}

	sr, err := readSourceFiles(*dir)
	if err != nil {
		return fmt.Errorf("read source files: %w", err)
	}
	warnIncompleteScan(stderr, sr, *dir)
	files := sr.Files

	rules := qualitycheck.DefaultRules()

	// Resolve custom rules file: explicit flag or auto-detect.
	rf := *rulesFile
	if rf == "" {
		candidate := filepath.Join(*dir, ".yagura", "quality.json")
		if _, statErr := os.Stat(candidate); statErr == nil {
			rf = candidate
		}
	}
	if rf != "" {
		data, readErr := os.ReadFile(rf)
		if readErr != nil {
			return fmt.Errorf("read rules file: %w", readErr)
		}
		var specs []qualitycheck.RuleSpec
		if jsonErr := json.Unmarshal(data, &specs); jsonErr != nil {
			return fmt.Errorf("parse rules file: %w", jsonErr)
		}
		custom, compErr := qualitycheck.CompileRules(specs)
		if compErr != nil {
			return fmt.Errorf("compile custom rules: %w", compErr)
		}
		rules = append(rules, custom...)
	}

	res := qualitycheck.ScanFiles(files, rules)

	if *minSev != "" {
		res = filterQualityBySeverity(res, qualitycheck.Severity(*minSev))
	}

	if *jsonOut {
		return emitJSON(stdout, res)
	}
	if *summaryOnly {
		humanQualityCheckSummary(stdout, res)
	} else {
		humanQualityCheck(stdout, res)
	}
	return nil
}

// filterQualityBySeverity は severity が min 以上の findings のみ残す。
// rank: prohibited(0) > warning(1) > info(2) — prohibited が最も厳しい。
func filterQualityBySeverity(r qualitycheck.Result, min qualitycheck.Severity) qualitycheck.Result {
	rank := map[qualitycheck.Severity]int{
		qualitycheck.SevProhibited: 0,
		qualitycheck.SevWarning:    1,
		qualitycheck.SevInfo:       2,
	}
	minRank := rank[min]
	kept := r.Findings[:0]
	for _, f := range r.Findings {
		if rank[f.Severity] <= minRank {
			kept = append(kept, f)
		}
	}
	r.Findings = kept
	r.BySeverity = make(map[qualitycheck.Severity]int)
	for _, f := range kept {
		r.BySeverity[f.Severity]++
	}
	return r
}

// ─── test-audit (v0.36.0) ────────────────────────────────────

// cliTestAudit は `yagura test-audit --dir .` を処理する。
// dir を再帰 walk して source-test 対応を検出し、coverage ratio を返す。
// testcoverage.Audit は純関数(I/O / token 不要)。
func cliTestAudit(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("test-audit", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively")
	untestedOnly := fset.Bool("untested-only", false, "list only sources without a matching test")
	strict := fset.Bool("strict", false, "exit non-zero if any source file lacks a matching test (CI gate)")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	sr, err := readSourceFiles(*dir)
	if err != nil {
		return fmt.Errorf("read source files: %w", err)
	}
	warnIncompleteScan(stderr, sr, *dir)
	files := sr.Files

	res := testcoverage.Audit(files)
	if *jsonOut {
		return emitJSON(stdout, res)
	}
	if *untestedOnly {
		humanTestAuditUntestedOnly(stdout, res)
	} else {
		humanTestAudit(stdout, res)
	}
	if *strict && len(res.UntestedFiles) > 0 {
		return fmt.Errorf("%d source file(s) lack a matching test — failing because --strict is set", len(res.UntestedFiles))
	}
	return nil
}

// ─── ast-check (v0.36.0, Roadmap #6) ─────────────────────────

// cliASTCheck は `yagura ast-check` を処理する。--dir 配下の Go ソースを go/ast で
// 構造解析し、行 regex では検出できないパターン(os.Exit in library / 空 nil 分岐 /
// parse error)を flag する。
func cliASTCheck(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("ast-check", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively")
	surface := fset.Bool("surface", false, "report capability surface (exec/network/unsafe/reflect/crypto) instead of defect findings")
	minSev := fset.String("min-severity", "low", "minimum severity to report: high|medium|low (ignored with --surface)")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	sr, err := readSourceFiles(*dir)
	if err != nil {
		return fmt.Errorf("read source files: %w", err)
	}
	warnIncompleteScan(stderr, sr, *dir)

	if *surface {
		res := astcheck.Surface(sr.Files)
		if *jsonOut {
			return emitJSON(stdout, res)
		}
		humanASTSurface(stdout, res)
		return nil
	}

	astRank := map[string]int{"high": 0, "medium": 1, "low": 2}
	minASTSev := strings.ToLower(*minSev)
	if _, ok := astRank[minASTSev]; !ok {
		fmt.Fprintf(stderr, "ast-check: --min-severity must be high|medium|low, got %q\n", *minSev)
		return errUsage
	}

	res := astcheck.ScanFiles(sr.Files)
	res = filterASTFindings(res, minASTSev, astRank)
	if *jsonOut {
		return emitJSON(stdout, res)
	}
	humanASTCheck(stdout, res)
	return nil
}

// filterASTFindings は astcheck.Result の Findings を severity >= min でフィルタし
// BySeverity/ByRule を再集計する。
func filterASTFindings(r astcheck.Result, minSev string, rank map[string]int) astcheck.Result {
	minRank := rank[minSev]
	kept := r.Findings[:0]
	for _, f := range r.Findings {
		if rank[strings.ToLower(f.Severity)] <= minRank {
			kept = append(kept, f)
		}
	}
	r.Findings = kept
	r.BySeverity = make(map[string]int)
	r.ByRule = make(map[string]int)
	for _, f := range kept {
		r.BySeverity[f.Severity]++
		r.ByRule[f.Rule]++
	}
	return r
}

// ─── review-gate (v0.36.0, 新視点) ────────────────────────────

// cliReviewGate は cortex flywheel ② Review の scanner 群(secretscan / aiverify /
// qualitycheck / astcheck)を --dir に対して走らせ、reviewgate で 1 つの合成判定
// (allow / review / block)へ束ねる。--strict で block 時に exit 非ゼロ(CI gate)。
func cliReviewGate(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("review-gate", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively")
	strict := fset.Bool("strict", false, "exit non-zero if the gate verdict is block (CI gate)")
	gate := fset.String("gate", "block", "minimum verdict tier to trigger exit non-zero: block|review (review includes block)")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if *gate != "block" && *gate != "review" {
		fmt.Fprintf(stderr, "review-gate: --gate must be block|review, got %q\n", *gate)
		return errUsage
	}

	sr, err := readSourceFiles(*dir)
	if err != nil {
		return fmt.Errorf("read source files: %w", err)
	}
	warnIncompleteScan(stderr, sr, *dir)
	files := sr.Files

	ai := aiverify.Scan(files)
	ql := qualitycheck.ScanFiles(files, qualitycheck.DefaultRules())
	ast := astcheck.ScanFiles(files)
	sc := secretscan.New()
	secretTotal := 0
	for path, content := range files {
		secretTotal += len(sc.Scan(content, path))
	}

	sig := reviewgate.Signals{
		SecretFindings: secretTotal,
		AIRiskScore:    ai.RiskScore,
		AICritical:     ai.BySeverity[aiverify.RiskCritical],
		LintProhibited: ql.BySeverity[qualitycheck.SevProhibited],
		ASTHigh:        ast.BySeverity["high"],
	}
	dec := reviewgate.Evaluate(sig)

	if *jsonOut {
		if err := emitJSON(stdout, map[string]any{"signals": sig, "decision": dec}); err != nil {
			return err
		}
	} else {
		humanReviewGate(stdout, sig, dec)
	}
	if *strict && dec.Tier == reviewgate.TierBlock {
		return fmt.Errorf("review gate: block — %s", strings.Join(dec.Blockers, "; "))
	}
	if *gate == "review" && (dec.Tier == reviewgate.TierReview || dec.Tier == reviewgate.TierBlock) {
		return fmt.Errorf("review gate: %s — %s", dec.Tier, strings.Join(dec.Blockers, "; "))
	}
	return nil
}

// ─── diff-scan (v0.36.0, delta 視点) ──────────────────────────

// diffSecretHit は diff の追加行に混入した secret 検出 1 件(diff の file:line 付き)。
type diffSecretHit struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	RuleID   string `json:"rule_id"`
	Severity string `json:"severity"`
}

// cliDiffScan は unified diff(stdin か --file)の *追加行のみ* に secret 混入が
// ないか検査する。スナップショット全体ではなく「この変更が新たに持ち込んだもの」
// を採点する delta 視点(既存負債で PR を落とさない)。
func cliDiffScan(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("diff-scan", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	file := fset.String("file", "", "read unified diff from this file (default: stdin)")
	strict := fset.Bool("strict", false, "exit non-zero if the diff introduces any secret (CI gate)")
	minSev := fset.String("min-severity", "low", "minimum severity to report: critical|high|medium|low")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	diffSecRank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	minDiffSev := strings.ToLower(*minSev)
	if _, ok := diffSecRank[minDiffSev]; !ok {
		fmt.Fprintf(stderr, "diff-scan: --min-severity must be critical|high|medium|low, got %q\n", *minSev)
		return errUsage
	}

	var data []byte
	var err error
	if *file != "" {
		data, err = os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("read diff %s: %w", *file, err)
		}
	} else {
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	}

	added := diffscan.AddedLines(string(data))
	sc := secretscan.New()
	var hits []diffSecretHit
	for _, al := range added {
		for _, f := range sc.Scan(al.Text, al.Path) {
			sev := strings.ToLower(string(f.Severity))
			if diffSecRank[sev] <= diffSecRank[minDiffSev] {
				hits = append(hits, diffSecretHit{Path: al.Path, Line: al.Line, RuleID: f.RuleID, Severity: string(f.Severity)})
			}
		}
	}
	guards := diffscan.RemovedGuards(string(data))

	if *jsonOut {
		if err := emitJSON(stdout, map[string]any{"added_lines": len(added), "findings": hits, "guards_removed": guards}); err != nil {
			return err
		}
	} else {
		humanDiffScan(stdout, len(added), hits, guards)
	}
	if *strict && len(hits) > 0 {
		return fmt.Errorf("diff introduced %d secret(s) — failing because --strict is set", len(hits))
	}
	return nil
}

// ─── flow-risk (v0.36.0, temporal/flow 視点) ──────────────────

// cliFlowRisk は操作シーケンス(stdin か --file、1 行 1 ツール/操作名)を読み、
// 各行を capability に正規化して危険な順序(exfiltration / injection-to-exec /
// untrusted-to-disk)を検出する。--strict で high flow 検出時 exit 非ゼロ。
func cliFlowRisk(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("flow-risk", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	file := fset.String("file", "", "read the op sequence from this file (default: stdin); one tool/op name per line")
	strict := fset.Bool("strict", false, "exit non-zero if a flow at or above --min-severity is detected (CI gate)")
	minSev := fset.String("min-severity", "high", "minimum severity to report and gate on: high|medium")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	flowRank := map[string]int{"high": 0, "medium": 1}
	minFlowSev := strings.ToLower(*minSev)
	if _, ok := flowRank[minFlowSev]; !ok {
		fmt.Fprintf(stderr, "flow-risk: --min-severity must be high|medium, got %q\n", *minSev)
		return errUsage
	}

	var data []byte
	var err error
	if *file != "" {
		data, err = os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("read sequence %s: %w", *file, err)
		}
	} else {
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	}

	var steps []flowrisk.Step
	for _, line := range strings.Split(string(data), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		steps = append(steps, flowrisk.Step{Name: name, Capability: flowrisk.ClassifyTool(name)})
	}
	allRisks := flowrisk.Analyze(steps)

	// Filter by --min-severity.
	var risks []flowrisk.FlowRisk
	for _, r := range allRisks {
		if flowRank[strings.ToLower(r.Severity)] <= flowRank[minFlowSev] {
			risks = append(risks, r)
		}
	}

	if *jsonOut {
		if err := emitJSON(stdout, map[string]any{"steps": len(steps), "flows": risks}); err != nil {
			return err
		}
	} else {
		humanFlowRisk(stdout, len(steps), risks)
	}
	if *strict && len(risks) > 0 {
		return fmt.Errorf("%d flow(s) at or above %s detected — failing because --strict is set", len(risks), *minSev)
	}
	return nil
}

// ─── coverage (v0.36.0, blind-spot meta 視点) ─────────────────

// cliCoverage は --dir の全ファイルを拡張子分類し、yagura の scanner が解析できる
// 割合(coverage)と盲点(未対応言語のソース)を報告する。clean 判定が「どれだけの
// コードを実際に見たか」を可視化する。
func cliCoverage(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("coverage", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively")
	minRatio := fset.Float64("min", 0, "exit non-zero if coverage ratio is below this (0 = no gate)")
	strict := fset.Bool("strict", false, "exit non-zero if any source file is in the scanner blind spot (CI gate)")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	var paths []string
	walkErr := filepath.WalkDir(*dir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == "node_modules" || name == ".git" || name == ".yagura" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(*dir, path)
		if relErr != nil {
			rel = path
		}
		paths = append(paths, rel)
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("walk %s: %w", *dir, walkErr)
	}

	rep := coverage.Classify(paths)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanCoverage(stdout, rep)
	}
	if *strict && rep.UncoveredSource > 0 {
		return fmt.Errorf("%d source file(s) in scanner blind spot — failing because --strict is set", rep.UncoveredSource)
	}
	if *minRatio > 0 && rep.CoverageRatio < *minRatio {
		return fmt.Errorf("coverage %.2f below --min %.2f (%d uncovered source file(s))", rep.CoverageRatio, *minRatio, rep.UncoveredSource)
	}
	return nil
}

// ─── assert-check (v0.36.0) ──────────────────────────────────

// cliAssertCheck は `yagura assert-check` を処理する。
// テストのアサーション密度を計測し、hollow (assertion なし) テストファイルを検出する。
// ソクラテス的動機: テスト存在(testcoverage)と異なり、テストが "何かを主張して
// いるか" を計測する。assertion 密度が 0 のテストは常に緑になるが証明にはならない。
func cliAssertCheck(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("assert-check", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for *_test.go files")
	maxDensity := fset.Float64("max-hollow", 0, "exit non-zero if hollow file count exceeds this fraction of test files (0 = no gate)")
	strict := fset.Bool("strict", false, "exit non-zero if any hollow test file exists (CI gate)")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	sr, err := readGoTestFiles(*dir)
	if err != nil {
		return err
	}
	warnIncompleteScan(stderr, sr, *dir)

	rep := assertcheck.Scan(sr.Files)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanAssertCheck(stdout, rep)
	}
	if *strict && rep.HollowFiles > 0 {
		return fmt.Errorf("%d hollow test file(s) (no assertions) — failing because --strict is set", rep.HollowFiles)
	}
	if *maxDensity > 0 && rep.TestFiles > 0 {
		hollowFrac := float64(rep.HollowFiles) / float64(rep.TestFiles)
		if hollowFrac > *maxDensity {
			return fmt.Errorf("hollow fraction %.2f exceeds --max-hollow %.2f (%d/%d test files)", hollowFrac, *maxDensity, rep.HollowFiles, rep.TestFiles)
		}
	}
	return nil
}

// ─── err-policy (v0.36.0) ────────────────────────────────────

// cliErrPolicy は `yagura err-policy` を処理する。Go ソースのエラー診断可能性を計測:
// naked `return err`(context 喪失)vs wrapped `fmt.Errorf(...%w...)` の wrap 率 +
// `_ = call()` の blank-discard 検出。token 不要(ローカル静的解析)。
func cliErrPolicy(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("err-policy", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	minRatio := fset.Float64("min-wrap", 0, "exit non-zero if wrap ratio is below this (0 = no gate)")
	strict := fset.Bool("strict", false, "exit non-zero if any blank-discard (_ = call()) is found (CI gate)")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	sr, err := readGoFiles(*dir)
	if err != nil {
		return err
	}
	warnIncompleteScan(stderr, sr, *dir)

	rep := errpolicy.Scan(sr.Files)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanErrPolicy(stdout, rep)
	}
	if *strict && rep.BlankDiscards > 0 {
		return fmt.Errorf("%d blank-discard (_ = call()) found — failing because --strict is set", rep.BlankDiscards)
	}
	if *minRatio > 0 {
		d := rep.WrappedReturns + rep.NakedReturns
		if d > 0 && rep.WrapRatio < *minRatio {
			return fmt.Errorf("wrap ratio %.2f below --min-wrap %.2f (%d naked of %d error returns)", rep.WrapRatio, *minRatio, rep.NakedReturns, d)
		}
	}
	return nil
}

// ─── complexity (v0.36.0) ────────────────────────────────────

// cliComplexity は `yagura complexity` を処理する。Go 関数の循環的複雑度を計測し、
// しきい値(--max、既定 10)超過の関数を flag する。--strict で超過時に exit 1。
// ソクラテス的動機: テストできる前提条件(全パス網羅に要するテスト数の下限)を数値化。
// token 不要(ローカル静的解析)。capped+warned walker を共有(fail-open 防止)。
func cliComplexity(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("complexity", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	max := fset.Int("max", 10, "complexity threshold; functions above this are flagged")
	strict := fset.Bool("strict", false, "exit non-zero if any function exceeds --max")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	sr, err := readGoFiles(*dir)
	if err != nil {
		return err
	}
	warnIncompleteScan(stderr, sr, *dir)

	rep := complexity.Scan(sr.Files, *max)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanComplexity(stdout, rep)
	}
	if *strict && rep.OverThreshold > 0 {
		return fmt.Errorf("%d function(s) exceed complexity threshold %d (max observed %d)", rep.OverThreshold, rep.Threshold, rep.MaxComplexity)
	}
	return nil
}

// cliParamCheck は `yagura param-check` を処理する。Go 関数のパラメータ数を計測し、
// しきい値(--max、既定 5)超過の関数を flag する。--strict で超過時に exit 1。
// ソクラテス的動機: complexity が関数内部の縦の複雑さなら、これは入口の横幅。
// Fowler の "Long Parameter List" smell を検出し、複雑度リファクタが引数列に
// ツケを回す退行を可視化する(complexity の水平方向の対)。token 不要。
func cliParamCheck(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("param-check", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	max := fset.Int("max", 5, "parameter-count threshold; functions above this are flagged")
	strict := fset.Bool("strict", false, "exit non-zero if any function exceeds --max")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	sr, err := readGoFiles(*dir)
	if err != nil {
		return err
	}
	warnIncompleteScan(stderr, sr, *dir)

	rep := paramcheck.Scan(sr.Files, *max)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanParamCheck(stdout, rep)
	}
	if *strict && rep.OverThreshold > 0 {
		return fmt.Errorf("%d function(s) exceed parameter threshold %d (max observed %d)", rep.OverThreshold, rep.Threshold, rep.MaxParams)
	}
	return nil
}

// ─── flag-arg (v0.66.0) ──────────────────────────────────────

// cliFlagArg は `yagura flag-arg` を処理する。Go 関数の bool パラメータ(Fowler
// "Flag Argument" smell)を ast で検出する。--min-bools=2 で単一 bool をスキップ。
// ソクラテス的動機: complexity は分岐数、paramcheck は引数総数を測るが、bool 1 個でも
// `process(data, true)` の呼び出し元で意味が不透明になる制御結合の臭いを補完する。
func cliFlagArg(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("flag-arg", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	minBools := fset.Int("min-bools", 1, "minimum number of bool params to flag (2 to skip single-bool cases)")
	strict := fset.Bool("strict", false, "exit non-zero if any flag arguments found")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	sr, err := readGoFiles(*dir)
	if err != nil {
		return err
	}
	warnIncompleteScan(stderr, sr, *dir)

	rep := flagarg.Scan(sr.Files, *minBools)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanFlagArg(stdout, rep)
	}
	if *strict && rep.FlagsFound > 0 {
		return fmt.Errorf("%d flag-argument(s) found (bool params ≥ %d)", rep.FlagsFound, rep.Threshold)
	}
	return nil
}

// ─── return-check (v0.67.0) ──────────────────────────────────

// cliReturnCheck は `yagura return-check` を処理する。Go 関数の戻り値の数を計測し、
// しきい値(--max、既定 3)超過の関数を flag する。
// ソクラテス的動機: paramcheck が引数の入口を測り、flagarg が引数の意味的制御結合を測る。
// return-check はその「出口の対」として関数シグネチャの全体像(入力幅+出力幅+意味)を補完する。
func cliReturnCheck(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("return-check", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	max := fset.Int("max", 3, "return-value count threshold; functions above this are flagged (default 3)")
	strict := fset.Bool("strict", false, "exit non-zero if any function exceeds --max")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	sr, err := readGoFiles(*dir)
	if err != nil {
		return err
	}
	warnIncompleteScan(stderr, sr, *dir)

	rep := returncheck.Scan(sr.Files, *max)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanReturnCheck(stdout, rep)
	}
	if *strict && rep.TooManyReturns > 0 {
		return fmt.Errorf("%d function(s) exceed return-value threshold %d (max observed %d)", rep.TooManyReturns, rep.Threshold, rep.MaxReturns)
	}
	return nil
}

// ─── err-discard (v0.68.0) ──────────────────────────────────

// cliErrDiscard は `yagura err-discard` を処理する。Go のコールサイトで
// error を返す関数が ExprStmt として呼ばれている箇所(= error が暗黙的に捨てられている)
// を二パス AST 走査で検出する。
// ソクラテス的動機: paramcheck + flagarg + returncheck は関数の定義側をプロファイルする
// 三軸を提供したが、呼び出し側の規律は見ていなかった。errdiscard はその盲点を塞ぐ。
func cliErrDiscard(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("err-discard", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	strict := fset.Bool("strict", false, "exit non-zero if any discarded errors found")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	sr, err := readGoFiles(*dir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", *dir, err)
	}
	warnIncompleteScan(stderr, sr, *dir)
	rep := errdiscard.Scan(sr.Files)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanErrDiscard(stdout, rep)
	}
	if *strict && rep.ErrorsDiscarded > 0 {
		return fmt.Errorf("%d call site(s) discard a returned error", rep.ErrorsDiscarded)
	}
	return nil
}

// ─── dep-rank (v0.69.0) ─────────────────────────────────────

// cliDepRank は `yagura dep-rank` を処理する。Go の import グラフから内部パッケージの
// in-degree(何個の内部パッケージに参照されているか)を計算し、blast radius が大きい
// パッケージを特定する。
// ソクラテス的動機: 全先行レンズは関数レベル(complexity/paramcheck/flagarg/returncheck)か
// コールサイトレベル(errdiscard)で動作しており、パッケージグラフ構造を見ていなかった。
func cliDepRank(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("dep-rank", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	module := fset.String("module", "github.com/shizukutanaka/yagura", "Go module prefix")
	threshold := fset.Int("threshold", 5, "minimum in-degree to flag (default 5)")
	topN := fset.Int("top", 10, "show top N packages by in-degree in human output")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	sr, err := readGoFiles(*dir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", *dir, err)
	}
	warnIncompleteScan(stderr, sr, *dir)
	rep := deprank.Scan(sr.Files, *module, *threshold)
	if *jsonOut {
		return emitJSON(stdout, rep)
	}
	humanDepRank(stdout, rep, *topN)
	return nil
}

// ─── hotspot (v0.70.0) ───────────────────────────────────────

// cliHotspot は `yagura hotspot` を処理する。4 つのシグネチャ系レンズ
// (complexity / paramcheck / flagarg / returncheck)を同じ file set に適用し、
// 複数レンズが独立に指摘した関数(= 収束シグナル)を高信頼リファクタ対象として報告。
// ソクラテス的動機: 個々のレンズは偽陽性を持つが、独立シグナルの収束は高信頼。
// --min-lenses で収束数の下限(default 2)、--strict で hotspot 検出時 exit 1。
func cliHotspot(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("hotspot", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	minLenses := fset.Int("min-lenses", 2, "minimum lenses that must converge to report a hotspot (default 2)")
	strict := fset.Bool("strict", false, "exit non-zero if any hotspot is found")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	sr, err := readGoFiles(*dir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", *dir, err)
	}
	warnIncompleteScan(stderr, sr, *dir)
	rep := hotspot.Scan(sr.Files, *minLenses)
	if *jsonOut {
		return emitJSON(stdout, rep)
	}
	humanHotspot(stdout, rep)
	if *strict && len(rep.Hotspots) > 0 {
		return fmt.Errorf("%d convergent-signal hotspot(s)", len(rep.Hotspots))
	}
	return nil
}

// ─── name-check (v0.73.0) ────────────────────────────────────

// cliNameCheck は `yagura name-check` を処理する。関数名がシグネチャの約束を
// 守っているかを検査する意味軸のレンズ: is/has 述語は bool を、Get/New 接頭辞は
// 戻り値を返すべき。型情報不要・決定論的。--strict で指摘時 exit 1。
func cliNameCheck(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("name-check", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	strict := fset.Bool("strict", false, "exit non-zero if any inconsistency is found")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	sr, err := readGoFiles(*dir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", *dir, err)
	}
	warnIncompleteScan(stderr, sr, *dir)
	rep := namecheck.Scan(sr.Files)
	if *jsonOut {
		return emitJSON(stdout, rep)
	}
	humanNameCheck(stdout, rep)
	if *strict && rep.Flagged > 0 {
		return fmt.Errorf("%d name↔signature inconsistency(ies)", rep.Flagged)
	}
	return nil
}

// ─── ctx-check (v0.75.0) ─────────────────────────────────────

// cliCtxCheck は `yagura ctx-check` を処理する。context.Context の取り扱い規律
// (第一引数規約 + struct field 非格納)を検査する並行性軸のレンズ。型情報不要・
// 決定論的。--strict で違反時 exit 1。
func cliCtxCheck(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("ctx-check", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	strict := fset.Bool("strict", false, "exit non-zero if any violation is found")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	sr, err := readGoFiles(*dir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", *dir, err)
	}
	warnIncompleteScan(stderr, sr, *dir)
	rep := ctxcheck.Scan(sr.Files)
	if *jsonOut {
		return emitJSON(stdout, rep)
	}
	humanCtxCheck(stdout, rep)
	if *strict && rep.Flagged > 0 {
		return fmt.Errorf("%d context.Context discipline violation(s)", rep.Flagged)
	}
	return nil
}

// ─── err-wrap (v0.76.0) ──────────────────────────────────────

// cliErrWrap は `yagura err-wrap` を処理する。Go 1.13 エラーラッピング規約
// (%w / errors.Is / errors.As)の違反を検査する error-chain 健全性軸のレンズ。
// 型情報不要・決定論的。--strict で違反時 exit 1。
func cliErrWrap(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("err-wrap", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	strict := fset.Bool("strict", false, "exit non-zero if any violation is found")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	sr, err := readGoFiles(*dir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", *dir, err)
	}
	warnIncompleteScan(stderr, sr, *dir)
	rep := errwrap.Scan(sr.Files)
	if *jsonOut {
		return emitJSON(stdout, rep)
	}
	humanErrWrap(stdout, rep)
	if *strict && rep.Flagged > 0 {
		return fmt.Errorf("%d error-wrapping violation(s)", rep.Flagged)
	}
	return nil
}

// ─── sync-check (v0.77.0) ────────────────────────────────────

// cliSyncCheck は `yagura sync-check` を処理する。sync.Mutex 等ロック型を含む
// 型の値コピー誤用を検査する concurrency safety 軸のレンズ(go vet copylocks 同等)。
// 型情報不要・決定論的。--strict で違反時 exit 1。
func cliSyncCheck(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("sync-check", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	strict := fset.Bool("strict", false, "exit non-zero if any violation is found")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	sr, err := readGoFiles(*dir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", *dir, err)
	}
	warnIncompleteScan(stderr, sr, *dir)
	rep := synccheck.Scan(sr.Files)
	if *jsonOut {
		return emitJSON(stdout, rep)
	}
	humanSyncCheck(stdout, rep)
	if *strict && rep.Flagged > 0 {
		return fmt.Errorf("%d sync-lock copy violation(s)", rep.Flagged)
	}
	return nil
}

// ─── coupling (v0.36.0) ──────────────────────────────────────

// cliCoupling は `yagura coupling` を処理する。Go の import グラフから package 間の
// fan-in/fan-out/instability を算出し、Stable Dependencies Principle 違反を flag する。
// module path は --module、無指定なら <dir>/go.mod から自動検出。--strict で違反時 exit 1。
// ソクラテス的動機: complexity が関数内部の絡まりなら、これは package 同士(アーキ
// テクチャ)の絡まり。capped+warned walker(readGoFiles)を共有し fail-open を防止。
func cliCoupling(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("coupling", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "module root to scan recursively for .go files")
	module := fset.String("module", "", "go.mod module path (default: auto-detect from <dir>/go.mod)")
	strict := fset.Bool("strict", false, "exit non-zero if any SDP violation is found")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	mp := *module
	if mp == "" {
		detected, derr := readModulePath(*dir)
		if derr != nil {
			return fmt.Errorf("module path: %w (pass --module to override)", derr)
		}
		mp = detected
	}

	sr, err := readGoFiles(*dir)
	if err != nil {
		return err
	}
	warnIncompleteScan(stderr, sr, *dir)

	rep := coupling.Scan(sr.Files, mp)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanCoupling(stdout, rep)
	}
	if *strict {
		var violations int
		for _, f := range rep.Findings {
			if f.Rule == "sdp-violation" {
				violations++
			}
		}
		if violations > 0 {
			return fmt.Errorf("%d Stable Dependencies Principle violation(s)", violations)
		}
	}
	return nil
}

// readModulePath は <dir>/go.mod の `module <path>` 行を読む。
func readModulePath(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", fmt.Errorf("no module directive in %s/go.mod", dir)
}

// ─── api-doc (v0.36.0) ───────────────────────────────────────

// cliAPIDoc は `yagura api-doc` を処理する。exported API のドキュメント率を計測し、
// doc コメントの無い exported シンボル(undocumented public contract)を flag する。
// --min-doc R で documented 率が R 未満なら exit 1。ソクラテス的動機: package が
// 依存側に約束する公開契約とその文書化。capped+warned walker を共有(fail-open 防止)。
func cliAPIDoc(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("api-doc", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	minDoc := fset.Float64("min-doc", 0, "exit non-zero if documented ratio is below this (0 = no gate)")
	strict := fset.Bool("strict", false, "exit non-zero if any exported symbol lacks a doc comment (CI gate)")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	sr, err := readGoFiles(*dir)
	if err != nil {
		return err
	}
	warnIncompleteScan(stderr, sr, *dir)

	rep := apidoc.Scan(sr.Files)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanAPIDoc(stdout, rep)
	}
	if *strict && rep.ExportedTotal > rep.Documented {
		return fmt.Errorf("%d exported symbol(s) lack doc comments — failing because --strict is set", rep.ExportedTotal-rep.Documented)
	}
	if *minDoc > 0 && rep.ExportedTotal > 0 && rep.DocumentedRatio < *minDoc {
		return fmt.Errorf("documented ratio %.2f below --min-doc %.2f (%d of %d exported symbols undocumented)",
			rep.DocumentedRatio, *minDoc, rep.ExportedTotal-rep.Documented, rep.ExportedTotal)
	}
	return nil
}

// ─── dead-code (v0.36.0) ─────────────────────────────────────

// cliDeadCode は `yagura dead-code` を処理する。自 package 内で参照されない unexported
// 宣言を検出する。--strict で dead が 1 件でもあれば exit 1。ソクラテス的動機: apidoc の
// 双対(非公開側)。capped+warned walker を共有(fail-open 防止)。
func cliDeadCode(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("dead-code", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	strict := fset.Bool("strict", false, "exit non-zero if any dead unexported declaration is found")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	sr, err := readGoFiles(*dir)
	if err != nil {
		return err
	}
	warnIncompleteScan(stderr, sr, *dir)

	rep := deadcode.Scan(sr.Files)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanDeadCode(stdout, rep)
	}
	if *strict && rep.Dead > 0 {
		return fmt.Errorf("%d dead unexported declaration(s)", rep.Dead)
	}
	return nil
}

// ─── recv-check (v0.36.0) ────────────────────────────────────

// cliRecvCheck は `yagura recv-check` を処理する。型のメソッドレシーバ一貫性を検査
// (名前不揃い / 値・ポインタ混在 / this・self)。--strict で finding が 1 件でも exit 1。
// ソクラテス的動機: unit を自分自身の他の部分と照らす自己一貫性。capped+warned walker 共有。
func cliRecvCheck(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("recv-check", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	strict := fset.Bool("strict", false, "exit non-zero if any receiver-consistency issue is found")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}

	sr, err := readGoFiles(*dir)
	if err != nil {
		return err
	}
	warnIncompleteScan(stderr, sr, *dir)

	rep := recvcheck.Scan(sr.Files)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanRecvCheck(stdout, rep)
	}
	if *strict && len(rep.Findings) > 0 {
		return fmt.Errorf("%d receiver-consistency issue(s)", len(rep.Findings))
	}
	return nil
}

// ─── code-health (v0.36.0) ───────────────────────────────────

// cliCodeHealth は `yagura code-health` を処理する。保守性レンズ群を package 別
// grade(A-F)へ合成する。--min-grade で総合 grade がそれ未満なら exit 1。
// ソクラテス的動機: 8 個の別々の問いではなく「総合的に健全か」を 1 つの grade で。
func cliCodeHealth(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("code-health", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	dir := fset.String("dir", ".", "directory to scan recursively for .go files")
	minGrade := fset.String("min-grade", "", "exit non-zero if overall grade is below this (A-F)")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	if *minGrade != "" && !validGrade(*minGrade) {
		return fmt.Errorf("invalid --min-grade %q (A/B/C/D/F)", *minGrade)
	}

	sr, err := readGoFiles(*dir)
	if err != nil {
		return err
	}
	warnIncompleteScan(stderr, sr, *dir)

	rep := codehealth.Analyze(sr.Files)
	if *jsonOut {
		if err := emitJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		humanCodeHealth(stdout, rep)
	}
	if *minGrade != "" && gradeRank(rep.OverallGrade) < gradeRank(*minGrade) {
		return fmt.Errorf("overall grade %s below --min-grade %s (score %d)", rep.OverallGrade, *minGrade, rep.OverallScore)
	}
	return nil
}

// validGrade / gradeRank は --min-grade ゲート用。A が最上位。
func validGrade(g string) bool {
	switch strings.ToUpper(g) {
	case "A", "B", "C", "D", "F":
		return true
	}
	return false
}

func gradeRank(g string) int {
	switch strings.ToUpper(g) {
	case "A":
		return 5
	case "B":
		return 4
	case "C":
		return 3
	case "D":
		return 2
	case "F":
		return 1
	}
	return 0
}

// ─── alert-fix (v0.36.0) ─────────────────────────────────────

// cliAlertFix は `yagura alert-fix` を処理する。registry の sensor data に対して
// portfolio 全体の health sweep を実行し、actionable alert を返す。token 不要
// (registry 読込のみ)。daemon の AfterScan health sweep と同じ rule を使う。
//
// 注: daemon の sweep 同様、Plan.md enrichment は行わない(sensor-only、disk I/O なし)。
// Plan.md を含む richer な評価は MCP `yagura_alert_fix` を使う。
func cliAlertFix(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("alert-fix", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	slug := fset.String("slug", "", "evaluate a single project (default: whole portfolio)")
	severityMin := fset.String("severity-min", "", "drop alerts below this severity (critical/high/medium/low)")
	staleDays := fset.Int("stale-days", 0, "override stale threshold in days (0 = default 30)")
	scorecardMin := fset.Float64("scorecard-min", 0, "override Scorecard alert threshold (0 = default 5.0)")
	openIssuesHigh := fset.Int("open-issues-high", 0, "override open-issues alert threshold (0 = default 20)")
	includeInactive := fset.Bool("include-inactive", false, "include resolved/snoozed alerts (default: filtered out)")
	if err := fset.Parse(args); err != nil {
		return errUsage
	}
	// Validate the closed-enum flag up front: a typo'd --severity-min must
	// error rather than silently pass through unfiltered.
	if *severityMin != "" && !validAlertSeverity(*severityMin) {
		return fmt.Errorf("invalid --severity-min %q (critical/high/medium/low)", *severityMin)
	}

	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}

	var projects []*project.Project
	if *slug != "" {
		p, gerr := reg.Get(*slug)
		if gerr != nil {
			return fmt.Errorf("project %q not registered", *slug)
		}
		projects = []*project.Project{p}
	} else {
		projects = reg.List()
	}

	th := alertfix.DefaultThresholds()
	if *staleDays > 0 {
		th.StaleDays = *staleDays
	}
	if *scorecardMin > 0 {
		th.ScorecardMin = *scorecardMin
	}
	if *openIssuesHigh > 0 {
		th.OpenIssuesHigh = *openIssuesHigh
	}

	snaps := make([]alertfix.ProjectSnapshot, 0, len(projects))
	for _, p := range projects {
		snaps = append(snaps, mcp.ProjectToSnapshot(*p))
	}
	report := alertfix.EvaluateAll(snaps, th)

	if *severityMin != "" {
		report = filterReportBySeverity(report, *severityMin)
	}

	// Lifecycle filter: exclude resolved/snoozed alerts (same store the daemon and
	// yagura_alert_fix use) unless --include-inactive. Best-effort: a load failure
	// just means no alerts are filtered.
	if !*includeInactive {
		if sd, sderr := config.ResolveStateDir(); sderr == nil {
			statePath := filepath.Join(sd, "alert_state.jsonl")
			if store, serr := alertfix.NewStore(statePath); serr == nil {
				report = store.FilterReport(report)
			} else {
				fmt.Fprintf(stderr, "yagura: warning: alert lifecycle unavailable: %v\n", serr)
			}
		}
	}

	if *jsonOut {
		return emitJSON(stdout, map[string]any{
			"alerts":           report.Alerts,
			"total":            report.Total,
			"by_severity":      report.BySeverity,
			"by_source":        report.BySource,
			"by_project":       report.ByProject,
			"projects_scanned": report.ProjectsScanned,
			"has_critical":     report.HasCritical,
			"summary":          report.Summary(),
		})
	}
	humanAlertFix(stdout, report)
	return nil
}

// validAlertSeverity は --severity-min が既知の閾値かを返す。
func validAlertSeverity(s string) bool {
	switch strings.ToLower(s) {
	case "critical", "high", "medium", "low":
		return true
	}
	return false
}

// filterReportBySeverity は severity_min 以上の alert のみ残し集計を再計算する。
// mcp.filterBySeverity と同じ rank だが、Total だけでなく by_* も再計算する。
// 未知の min は呼出側(cliAlertFix)が事前検証する前提(ここに来ない)。
func filterReportBySeverity(r alertfix.Report, min string) alertfix.Report {
	rank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	maxRank, ok := rank[strings.ToLower(min)]
	if !ok {
		return r
	}
	kept := make([]alertfix.Alert, 0, len(r.Alerts))
	for _, a := range r.Alerts {
		if rank[string(a.Severity)] <= maxRank {
			kept = append(kept, a)
		}
	}
	r.Alerts = kept
	r.Total = len(kept)
	r.BySeverity = map[alertfix.Severity]int{}
	r.BySource = map[alertfix.Source]int{}
	r.ByProject = map[string]int{}
	r.HasCritical = false
	for _, a := range kept {
		r.BySeverity[a.Severity]++
		r.BySource[a.Source]++
		r.ByProject[a.Project]++
		if a.Severity == alertfix.SevCritical {
			r.HasCritical = true
		}
	}
	return r
}

// scanResult は readSourceFiles の結果。スキャンが不完全になった理由を
// 区別して持つ(remediation が異なるため):Truncated = 上限到達、
// Unreadable = 存在するが読めなかったソース。どちらも非空なら呼出側が警告する。
type scanResult struct {
	Files      map[string]string
	Truncated  bool     // 1000 件 / 50 MB 上限に達して打ち切った
	Unreadable []string // ツリー内に在るが os.ReadFile が失敗したソース
}

// readSourceFiles は dir を再帰的に走査し、Go/TS/JS/Python/Rust/Java の
// ソースファイルを {relpath: content} として返す。
// vendor/, node_modules/, .git/ はスキップ。上限 1000 件 / 50 MB。
// スキャンが不完全(上限到達 or 読取失敗)なら scanResult のフラグで通知する。
// これらを無視して「findings なし」を報告すると部分スキャンを完全スキャンと
// 取り違える fail-open になるため、呼出側は必ず警告すること。
func readSourceFiles(dir string) (scanResult, error) {
	const maxFiles = 1000
	const maxTotalBytes = 50 * 1024 * 1024
	return readSourceFilesLimited(dir, maxFiles, maxTotalBytes)
}

// readSourceFilesLimited は readSourceFiles の本体(上限を引数化してテスト可能に)。
// Go/TS/JS/Python/Rust/Java のソースを対象にする。
func readSourceFilesLimited(dir string, maxFiles int, maxTotalBytes int64) (scanResult, error) {
	return readFilesLimited(dir, maxFiles, maxTotalBytes, isSourceFile)
}

// readGoFiles は dir 配下の *.go を capped+warned walker で読む(err-policy 用)。
// readSourceFilesLimited と同じ上限・同じ不完全スキャン通知を継承する。
func readGoFiles(dir string) (scanResult, error) {
	const maxFiles = 1000
	const maxTotalBytes = 50 * 1024 * 1024
	return readFilesLimited(dir, maxFiles, maxTotalBytes, func(name string) bool {
		return strings.HasSuffix(name, ".go")
	})
}

// readGoTestFiles は dir 配下の *_test.go を capped+warned walker で読む(assert-check 用)。
func readGoTestFiles(dir string) (scanResult, error) {
	const maxFiles = 1000
	const maxTotalBytes = 50 * 1024 * 1024
	return readFilesLimited(dir, maxFiles, maxTotalBytes, func(name string) bool {
		return strings.HasSuffix(name, "_test.go")
	})
}

// readFilesLimited は dir を再帰的に走査し、accept(filename)==true のファイルを
// {relpath: content} で読む共通 walker。vendor/node_modules/.git/.yagura を skip し、
// maxFiles / maxTotalBytes の上限と、不完全スキャン(truncated / unreadable)の
// シグナルを scanResult に記録する。新しいスキャナはこの 1 本を predicate 付きで
// 再利用すること(独自 WalkDir を書くと caps と fail-open 警告が失われる)。
func readFilesLimited(dir string, maxFiles int, maxTotalBytes int64, accept func(name string) bool) (scanResult, error) {
	sr := scanResult{Files: make(map[string]string)}
	var totalBytes int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		name := d.Name()
		if d.IsDir() {
			if name == "vendor" || name == "node_modules" || name == ".git" || name == ".yagura" {
				return filepath.SkipDir
			}
			return nil
		}
		if !accept(name) {
			return nil
		}
		if len(sr.Files) >= maxFiles {
			sr.Truncated = true
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			// 存在するソースが読めない = 検出から漏れる。fail-open を避けるため
			// 黙殺せず記録する(readWorkflowFiles は同条件で hard-fail するが、
			// 深いツリー walk を 1 ファイルで止めるのは過剰なので skip+report)。
			rel, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				rel = path
			}
			sr.Unreadable = append(sr.Unreadable, rel)
			return nil
		}
		if totalBytes+int64(len(data)) > maxTotalBytes {
			sr.Truncated = true
			return nil
		}
		totalBytes += int64(len(data))
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			rel = path
		}
		sr.Files[rel] = string(data)
		return nil
	})
	return sr, err
}

// warnIncompleteScan は scan が不完全な場合に stderr へ目立つ警告を出す。
// 部分スキャンを「クリーン」と誤読させない(fail-open 防止)ための共通処理。
func warnIncompleteScan(stderr io.Writer, sr scanResult, dir string) {
	if sr.Truncated {
		fmt.Fprintf(stderr, "warning: scan of %s truncated at %d files / 50MB cap — "+
			"results cover only part of the tree; narrow --dir or split the scan before trusting a clean verdict\n",
			dir, len(sr.Files))
	}
	if n := len(sr.Unreadable); n > 0 {
		shown := sr.Unreadable
		if len(shown) > 5 {
			shown = shown[:5]
		}
		fmt.Fprintf(stderr, "warning: %d source file(s) in %s could not be read and were skipped "+
			"(not scanned): %s — fix permissions/symlinks before trusting a clean verdict\n",
			n, dir, strings.Join(shown, ", "))
	}
}

// isSourceFile は name がサポート言語のソースファイルかを返す。
func isSourceFile(name string) bool {
	for _, ext := range []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// readWorkflowFiles は dir 直下の *.yml / *.yaml を {filename: content} で読む。
func readWorkflowFiles(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	files := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		files[name] = string(data)
	}
	return files, nil
}

// newGitHubClient は run() と同じ構成で GitHub client を組み立てる(pin-drift 用)。
func newGitHubClient(cfg *config.Config) *github.Client {
	ts := github.NewTokenStore(cfg.GitHubToken)
	for owner, token := range cfg.GitHubTokens {
		ts.AddOwnerToken(owner, token)
	}
	return github.NewClient(github.Config{
		Tokens:  ts,
		BaseURL: cfg.GitHubBase,
		Timeout: cfg.ScanTimeout,
	})
}

// ─── alert-resolve (v0.51.0) ─────────────────────────────────

// cliAlertResolve は `yagura alert-resolve <alert-id> --action <resolve|snooze|reopen>`
// を処理する。MCP yagura_alert_resolve と同じ alertfix.Store API を使う。
// state は {state_dir}/alert_state.jsonl に JSONL 永続化される。
func cliAlertResolve(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("alert-resolve", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	action := fset.String("action", "", "lifecycle action: resolve|snooze|reopen")
	note := fset.String("note", "", "optional note to attach")
	snoozeDays := fset.Int("snooze-days", 7, "snooze duration in days (default 7)")
	pos, err := parseArgs(fset, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "usage: yagura alert-resolve <alert-id> --action <resolve|snooze|reopen> [--note TEXT] [--snooze-days N]")
		return fmt.Errorf("alert-resolve: alert-id required")
	}
	alertID := pos[0]
	if *action == "" {
		return fmt.Errorf("alert-resolve: --action is required (resolve|snooze|reopen)")
	}
	switch *action {
	case "resolve", "snooze", "reopen":
	default:
		return fmt.Errorf("alert-resolve: unknown action %q (must be resolve|snooze|reopen)", *action)
	}

	sd, err := config.ResolveStateDir()
	if err != nil {
		return fmt.Errorf("alert-resolve: %w", err)
	}
	statePath := filepath.Join(sd, "alert_state.jsonl")
	store, err := alertfix.NewStore(statePath)
	if err != nil {
		return fmt.Errorf("alert-resolve: open store: %w", err)
	}

	switch *action {
	case "resolve":
		err = store.Resolve(alertID, *note)
	case "snooze":
		days := *snoozeDays
		if days <= 0 {
			days = 7
		}
		until := time.Now().Add(time.Duration(days) * 24 * time.Hour)
		err = store.Snooze(alertID, until, *note)
	case "reopen":
		err = store.Reopen(alertID, *note)
	}
	if err != nil {
		return fmt.Errorf("alert-resolve: %w", err)
	}

	st, _ := store.Get(alertID)
	stats := store.Stats()

	if *jsonOut {
		return emitJSON(stdout, map[string]any{
			"alert_id":        alertID,
			"action":          *action,
			"current_state":   st,
			"lifecycle_stats": stats,
		})
	}
	humanAlertResolve(stdout, alertID, *action, st, stats)
	return nil
}

// cliAlertSnapshot は `yagura alert-snapshot` を処理する。
// alertfix.Store から全 alert の現在の lifecycle 状態を表示する(read-only)。
// 表示対象は全ステータス(active/resolved/snoozed)で、--status でフィルタ可能。
func cliAlertSnapshot(args []string, stdout, stderr io.Writer) error {
	fset := newFlagSet("alert-snapshot", stderr)
	jsonOut := fset.Bool("json", false, "JSON output")
	status := fset.String("status", "", "filter by status: active|resolved|snoozed (default: all)")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *status != "" {
		switch *status {
		case "active", "resolved", "snoozed":
		default:
			return fmt.Errorf("alert-snapshot: --status must be active|resolved|snoozed, got %q", *status)
		}
	}

	sd, err := config.ResolveStateDir()
	if err != nil {
		return fmt.Errorf("alert-snapshot: %w", err)
	}
	statePath := filepath.Join(sd, "alert_state.jsonl")
	store, err := alertfix.NewStore(statePath)
	if err != nil {
		return fmt.Errorf("alert-snapshot: open store: %w", err)
	}

	snap := store.Snapshot()
	stats := store.Stats()

	// optional filter
	if *status != "" {
		want := alertfix.LifecycleStatus(*status)
		kept := snap[:0]
		for _, s := range snap {
			if s.Status == want {
				kept = append(kept, s)
			}
		}
		snap = kept
	}

	if *jsonOut {
		return emitJSON(stdout, map[string]any{
			"states":          snap,
			"lifecycle_stats": stats,
		})
	}
	humanAlertSnapshot(stdout, snap, stats)
	return nil
}

// ─── plan-status (v0.38.0) ───────────────────────────────────

// cliPlanStatus は `yagura plan-status <slug>` を処理する。
// MCP yagura_plan_status と同じ domain logic(plantracker.Parse)を呼ぶ。
// LocalPath 配下から Plan.md を探し、checkboxes + required sections を集計する。
// token 不要。
func cliPlanStatus(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("plan-status", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: yagura plan-status <slug> [--json]")
	}
	slug := pos[0]
	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	p, err := reg.Get(slug)
	if err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return fmt.Errorf("not found: %s", slug)
		}
		return err
	}
	content, path, loadErr := loadPlanMdLocal(p.LocalPath)
	if loadErr != nil {
		if *jsonOut {
			return emitJSON(stdout, map[string]any{
				"slug":    slug,
				"plan_md": "",
				"error":   loadErr.Error(),
			})
		}
		return loadErr
	}
	state := plantracker.Parse(content)
	if *jsonOut {
		return emitJSON(stdout, map[string]any{
			"slug":    slug,
			"plan_md": path,
			"state":   state,
			"summary": state.Summary(),
		})
	}
	humanPlanStatus(stdout, slug, path, state)
	return nil
}

// ─── release-radar (v0.38.0) ─────────────────────────────────

// cliReleaseRadar は `yagura release-radar` を処理する。
// MCP yagura_release_radar と同じ domain logic(plantracker.ReleaseReadinessExt /
// plantracker.Rank)を使い、LocalPath がある全 project の Plan.md を読んで
// release 準備度を 0-100 でランク付けする。token 不要。
// --scan-code を付けると aiverify で AI risk factor を追加集計する。
func cliReleaseRadar(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("release-radar", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	limit := fs.Int("limit", 10, "max projects to show (0 = all)")
	scanCode := fs.Bool("scan-code", false, "scan project source with ai-verify for AI risk (slower)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *limit < 0 {
		*limit = 0
	}
	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	projects := reg.List()
	items := make([]plantracker.RankedProject, 0, len(projects))
	for _, p := range projects {
		if p.LocalPath == "" {
			continue
		}
		content, _, loadErr := loadPlanMdLocal(p.LocalPath)
		if loadErr != nil {
			continue
		}
		plan := plantracker.Parse(content)
		ciStatus := string(p.CIStatus)
		if ciStatus == "" {
			ciStatus = "unknown"
		}
		openCrit := p.VulnCritical
		var aiResult aiverify.Result
		if *scanCode {
			aiResult = scanProjectAICodeCLI(p.LocalPath)
		}
		readiness := plantracker.ReleaseReadinessExt(plantracker.ReadinessInput{
			Plan: plan, CIStatus: ciStatus, OpenCriticalIssues: openCrit,
			AIRiskScore: aiResult.RiskScore, AIHasCritical: aiResult.HasCritical,
		})
		reason := pickReleaseReason(plan, ciStatus, openCrit, aiResult.HasCritical, aiResult.RiskScore)
		items = append(items, plantracker.RankedProject{
			Slug:               p.Slug,
			Readiness:          readiness,
			PlanProgressPct:    plan.ProgressPct,
			CurrentPhase:       plan.CurrentPhase,
			CIStatus:           ciStatus,
			OpenIssuesCritical: openCrit,
			AIRiskScore:        aiResult.RiskScore,
			AIHasCritical:      aiResult.HasCritical,
			AIGenLineCount:     aiResult.AIGenLines,
			Reason:             reason,
		})
	}
	ranked := plantracker.Rank(items)
	if *limit > 0 && len(ranked) > *limit {
		ranked = ranked[:*limit]
	}
	if *jsonOut {
		return emitJSON(stdout, map[string]any{
			"ranked":          ranked,
			"total_projects":  len(projects),
			"projects_scored": len(items),
			"scan_code":       *scanCode,
		})
	}
	humanReleaseRadar(stdout, ranked, len(projects), len(items), *scanCode)
	return nil
}

// loadPlanMdLocal は LocalPath 配下から Plan.md / PLAN.md / plan.md を探す。
// internal/mcp.loadPlanMd と同じロジック(循環 import を避けるため複製)。
func loadPlanMdLocal(localPath string) (string, string, error) {
	if localPath == "" {
		return "", "", fmt.Errorf("project has no local_path")
	}
	candidates := []string{"Plan.md", "PLAN.md", "plan.md"}
	for _, name := range candidates {
		full := filepath.Join(localPath, name)
		if data, err := os.ReadFile(full); err == nil {
			return string(data), full, nil
		}
	}
	return "", "", fmt.Errorf("no Plan.md / PLAN.md / plan.md found in %s", localPath)
}

// pickReleaseReason は readiness 阻害の最大要因を 1 文で返す。
// internal/mcp.pickReason と同じロジック(循環 import を避けるため複製)。
func pickReleaseReason(plan plantracker.PlanState, ciStatus string, openCrit int,
	aiCritical bool, aiRisk int) string {
	_ = aiRisk // unused in the textual reason (AI critical already captured)
	if aiCritical {
		return "AI-generated critical risk (review required)"
	}
	if openCrit > 0 {
		return fmt.Sprintf("%d critical issues blocking", openCrit)
	}
	if strings.EqualFold(ciStatus, "failing") {
		return "CI failing"
	}
	if !plan.IsHealthy {
		return "Plan.md missing required sections"
	}
	if plan.ProgressPct < 100 {
		return fmt.Sprintf("plan %d%% remaining", 100-plan.ProgressPct)
	}
	return "ready to release"
}

// scanProjectAICodeCLI は LocalPath 配下の主要 source file を aiverify で scan する。
// internal/mcp.scanProjectAICode と同じロジック(循環 import を避けるため複製)。
// 上位 64 file / 256KB 制限(暴走防止)。
func scanProjectAICodeCLI(localPath string) aiverify.Result {
	const maxFiles = 64
	const maxFileSize = 256 * 1024

	files := map[string]string{}
	walked := 0
	filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if walked >= maxFiles {
			return filepath.SkipDir
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
			return nil
		}
		ext := filepath.Ext(path)
		switch ext {
		case ".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".rs":
		default:
			return nil
		}
		if info.Size() > maxFileSize {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(localPath, path)
		files[rel] = string(data)
		walked++
		return nil
	})
	if len(files) == 0 {
		return aiverify.Result{}
	}
	return aiverify.Scan(files)
}

// ─── ops-risk (v0.39.0) ───────────────────────────────────────

// cliOpsRisk は `yagura ops-risk [--file <path>] [--json]` を処理する。
// Operations を JSON で受け取り、自律 tier を決定論的に分類して返す。
func cliOpsRisk(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("ops-risk", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	filePath := fs.String("file", "", "path to JSON file (default: stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	data, err := readInputData(*filePath, os.Stdin)
	if err != nil {
		return fmt.Errorf("ops-risk: %w", err)
	}

	var ops []opsrisk.Op
	// accept array directly OR {"operations":[...]} wrapper
	if err := tryUnmarshalOpsArray(data, &ops); err != nil {
		return fmt.Errorf("ops-risk: invalid JSON: %w", err)
	}
	if len(ops) == 0 {
		return fmt.Errorf("no operations provided (pass JSON array via --file or stdin)")
	}

	result := opsrisk.ClassifyAll(ops)
	if *jsonOut {
		return emitJSON(stdout, result)
	}
	humanOpsRisk(stdout, result)
	return nil
}

// tryUnmarshalOpsArray は JSON を []opsrisk.Op として解釈する。
// 直接配列か {"operations":[...]} ラッパー形式の両方を受け付ける。
func tryUnmarshalOpsArray(data []byte, ops *[]opsrisk.Op) error {
	if err := json.Unmarshal(data, ops); err == nil {
		return nil
	}
	var wrapper struct {
		Operations []opsrisk.Op `json:"operations"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}
	*ops = wrapper.Operations
	return nil
}

// readInputData は --file フラグが指定されていれば当該ファイルを、
// そうでなければ fallback(通常 os.Stdin)を読み込む。
func readInputData(filePath string, fallback io.Reader) ([]byte, error) {
	if filePath != "" {
		return os.ReadFile(filePath)
	}
	return io.ReadAll(fallback)
}

// ─── risk-triage (v0.39.0) ───────────────────────────────────

// cliRiskTriage は `yagura risk-triage [--file <path>] [--slug <s>] [--json]` を処理する。
// CVE findings の複合リスクスコアを計算して優先度付きで返す。
func cliRiskTriage(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("risk-triage", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	filePath := fs.String("file", "", "path to JSON file (default: stdin)")
	slug := fs.String("slug", "", "project slug to enrich findings with registry metadata")
	if err := fs.Parse(args); err != nil {
		return err
	}

	data, err := readInputData(*filePath, os.Stdin)
	if err != nil {
		return fmt.Errorf("risk-triage: %w", err)
	}

	var inputs []riskreason.Input
	if err := tryUnmarshalRiskArray(data, &inputs); err != nil {
		return fmt.Errorf("risk-triage: invalid JSON: %w", err)
	}
	if len(inputs) == 0 {
		return fmt.Errorf("no findings provided (pass JSON array via --file or stdin)")
	}

	// optional registry enrichment when --slug is given
	if *slug != "" {
		if reg, err := openRegistry(stderr); err == nil {
			if p, err := reg.Get(*slug); err == nil {
				deps := toRiskGraphDependents(reg, *slug)
				for i := range inputs {
					inputs[i].AssetPriority = p.Priority
					inputs[i].Stage = string(p.Stage)
					inputs[i].Tags = append(inputs[i].Tags, p.Tags...)
					inputs[i].Dependents = deps
				}
			} else {
				fmt.Fprintf(stderr, "yagura: warning: project %q not found in registry\n", *slug)
			}
		}
	}

	results := riskreason.ScoreAll(inputs)
	// sort by Score descending (highest risk first)
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	if *jsonOut {
		return emitJSON(stdout, results)
	}
	humanRiskTriage(stdout, results)
	return nil
}

// tryUnmarshalRiskArray は JSON を []riskreason.Input として解釈する。
// 直接配列か {"findings":[...]} ラッパー形式の両方を受け付ける。
func tryUnmarshalRiskArray(data []byte, inputs *[]riskreason.Input) error {
	if err := json.Unmarshal(data, inputs); err == nil {
		return nil
	}
	var wrapper struct {
		Findings []riskreason.Input `json:"findings"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}
	*inputs = wrapper.Findings
	return nil
}

// toRiskGraphDependents は projectgraph から slug の依存元数(transitive impact 件数)を返す。
// graph が取得できない場合は 0 を返す(best-effort)。
func toRiskGraphDependents(reg *registry.Registry, slug string) int {
	g := projectgraph.Build(toGraphProjects(reg.List()))
	impact := g.Impact(slug)
	return impact.ImpactCount
}

// ─── recovery-decide (v0.40.0) ───────────────────────────────

// cliRecoveryDecide は `yagura recovery-decide --class <cls> [options]` を処理する。
// 失敗 class + 試行回数 + budget から次の recovery action を決定論的に返す。
func cliRecoveryDecide(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("recovery-decide", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	class := fs.String("class", "", "failure class (timeout/rate_limit/bad_args/tool_error/auth/quota/context_overflow/wrong_result/unknown)")
	attempt := fs.Int("attempt", 1, "1-based attempt count")
	maxAttempts := fs.Int("max-attempts", 3, "recovery budget")
	agent := fs.String("agent", "", "current agent identifier")
	severity := fs.String("severity", "", "severity (low = degrade instead of escalate when budget exhausted)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *class == "" {
		return fmt.Errorf("--class is required (e.g. timeout, rate_limit, bad_args, tool_error, auth, quota, context_overflow, wrong_result, unknown)")
	}
	d := recovery.Decide(recovery.Event{
		Class:       recovery.FailureClass(*class),
		Attempt:     *attempt,
		MaxAttempts: *maxAttempts,
		Agent:       *agent,
		Severity:    *severity,
	})
	if *jsonOut {
		return emitJSON(stdout, d)
	}
	humanRecoveryDecide(stdout, d)
	return nil
}

// ─── agents-md (v0.40.0) ─────────────────────────────────────

// cliAgentsMd は `yagura agents-md <slug> [--write] [--json]` を処理する。
// Plan.md + registry facts から AGENTS.md を生成する。
func cliAgentsMd(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("agents-md", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	write := fs.Bool("write", false, "write AGENTS.md to project local_path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: yagura agents-md <slug> [--write] [--json]")
	}
	slug := fs.Arg(0)

	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	p, err := reg.Get(slug)
	if err != nil {
		return fmt.Errorf("project %q not found", slug)
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
		GeneratedBy:  "yagura",
	}
	if p.LocalPath != "" {
		if content, _, err2 := loadPlanMdLocal(p.LocalPath); err2 == nil {
			state := plantracker.Parse(content)
			for _, ph := range state.Phases {
				lower := strings.ToLower(ph.Name)
				if !strings.Contains(lower, "phase") && !strings.Contains(ph.Name, "フェーズ") {
					continue
				}
				facts.Phases = append(facts.Phases,
					fmt.Sprintf("%s (%d/%d)", ph.Name, ph.CompletedTasks, ph.TotalTasks))
			}
			facts.Description = cliExtractSection(content, []string{"目的", "Purpose"})
			facts.Scope = cliExtractSection(content, []string{"スコープ", "Scope"})
			facts.DoD = cliExtractDoDItems(content)
		}
	}

	body := agentmd.Generate(facts)
	result := map[string]any{
		"slug":     slug,
		"body":     body,
		"length":   len(body),
		"filename": "AGENTS.md",
	}
	if *write {
		if p.LocalPath == "" {
			return fmt.Errorf("project %q has no local_path; cannot write AGENTS.md", slug)
		}
		path := filepath.Join(p.LocalPath, "AGENTS.md")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return fmt.Errorf("write AGENTS.md: %w", err)
		}
		result["written_to"] = path
		fmt.Fprintf(stderr, "yagura: wrote %s\n", path)
	}
	if *jsonOut {
		return emitJSON(stdout, result)
	}
	humanAgentsMd(stdout, body)
	return nil
}

// ─── feature-list (v0.40.0) ──────────────────────────────────

// cliFeatureList は `yagura feature-list <slug> [--write] [--json]` を処理する。
// Plan.md を Anthropic-style feature-list.json に変換する。
func cliFeatureList(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("feature-list", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	write := fs.Bool("write", false, "write feature-list.json to project local_path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: yagura feature-list <slug> [--write] [--json]")
	}
	slug := fs.Arg(0)

	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	p, err := reg.Get(slug)
	if err != nil {
		return fmt.Errorf("project %q not found", slug)
	}
	if p.LocalPath == "" {
		return fmt.Errorf("project %q has no local_path; cannot read Plan.md", slug)
	}
	content, _, err := loadPlanMdLocal(p.LocalPath)
	if err != nil {
		return fmt.Errorf("Plan.md not found for %q: %w", slug, err)
	}

	state := plantracker.Parse(content)
	pin := cliPlanStateToFeatureInput(slug, content, state)
	fl := featurelist.Build(pin, nil)

	if *write {
		raw, merr := json.MarshalIndent(fl, "", "  ")
		if merr != nil {
			return fmt.Errorf("marshal feature-list: %w", merr)
		}
		path := filepath.Join(p.LocalPath, "feature-list.json")
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			return fmt.Errorf("write feature-list.json: %w", err)
		}
		fmt.Fprintf(stderr, "yagura: wrote %s\n", path)
	}
	if *jsonOut {
		return emitJSON(stdout, fl)
	}
	humanFeatureList(stdout, fl)
	return nil
}

// ─── harness-coverage (v0.40.0) ──────────────────────────────

// cliHarnessCoverage は `yagura harness-coverage [--json]` を処理する。
// Fowler taxonomy(Computational × Inferential × Guide × Sensor)に対して
// yagura が提供する coverage を返す。
func cliHarnessCoverage(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("harness-coverage", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	matrix := map[string]map[string][]string{
		"guide": {
			"computational": {
				"yagura feature-list (Plan.md → feature-list.json scaffold)",
				"yagura path-policy (change-path gate against .yagura/paths.json)",
				"yagura ops-risk (operation autonomy tier: auto/log/review/human)",
				"yagura recovery-decide (failure recovery: retry/replan/escalate)",
				"yagura risk-triage (CVE compound prioritization: CVSS+asset+reachability)",
				"yagura release-radar (cross-project release readiness ranking)",
				"yagura harness-recommend (Claude Code scaffold by language)",
				"yagura vex-audit (OpenVEX compliance: validate docs/vex/*.json)",
			},
			"inferential": {
				"yagura agents-md (AGENTS.md scaffold for Claude Code/Codex/Cursor)",
				"yagura harness-coverage (self-audit: which quadrants are covered?)",
				"yagura skill-audit / subagent-audit (skill scaffolding)",
				"yagura parallel-plan (LPT fan-out plan across AI agents)",
			},
		},
		"sensor": {
			"computational": {
				"yagura quality-check (static code analysis: as-any/TODO/ts-ignore)",
				"yagura secretscan (secret detection: API keys/credentials)",
				"yagura gha-audit (workflow audit: pinning/permissions)",
				"yagura pin-drift (dep pin drift: SHA-pinned uses)",
				"yagura ai-verify (AI-generated code patterns: eval/unsafe/network)",
				"yagura inject-scan (prompt injection signals in untrusted content)",
				"yagura publicity-scan (pre-publish leak: home paths/IPs/emails)",
				"yagura test-audit (source-test counterpart coverage)",
				"yagura assert-check (test assertion density: hollow test detection)",
				"yagura err-policy (error diagnostics: wrap ratio + blank-discard)",
				"yagura diff-scan (diff delta: secrets in added lines + guard removal)",
				"yagura flow-risk (temporal flow: exfiltration/injection/untrusted-disk)",
				"yagura sbom (CycloneDX SBOM generation)",
				"yagura ast-check (Go AST: os.Exit-library/empty-nil-branch/panic)",
				"yagura complexity (cyclomatic complexity per function)",
				"yagura coupling (package coupling: fan-in/fan-out/SDP violations)",
				"yagura dead-code (unreachable unexported declarations)",
				"yagura recv-check (method receiver consistency)",
				"yagura api-doc (exported-API doc ratio)",
				"yagura coverage (scanner blind-spot report)",
				"yagura code-health (composite maintainability grade A-F per package)",
				"yagura review-gate (composite ② Review gate: allow/review/block)",
				"yagura alert-fix (portfolio health sweep: sensor-based alert recommendations)",
				"yagura cc-security (Claude Code security posture audit)",
			},
			"inferential": {
				"(intentionally none — ADR-0001 zero-dep precludes LLM-as-judge in-process)",
			},
		},
	}
	counts := map[string]int{}
	for axis, ci := range matrix {
		for class, items := range ci {
			key := axis + "." + class
			counts[key] = len(items)
		}
	}
	result := map[string]any{
		"matrix": matrix,
		"counts": counts,
	}
	if *jsonOut {
		return emitJSON(stdout, result)
	}
	humanHarnessCoverage(stdout, matrix, counts)
	return nil
}

// ─── agent-event (v0.41.0) ───────────────────────────────────

// cliAgentEvent は `yagura agent-event [--file <path>] [--json]` を処理する。
// 任意形式の agent lifecycle イベントを OTel GenAI semconv に正規化する。
func cliAgentEvent(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("agent-event", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	filePath := fs.String("file", "", "path to JSON file (default: stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := readInputData(*filePath, os.Stdin)
	if err != nil {
		return fmt.Errorf("agent-event: %w", err)
	}
	e, err := agentevent.NormalizeJSON(data)
	if err != nil {
		return fmt.Errorf("agent-event: invalid JSON: %w", err)
	}
	result := map[string]any{
		"normalized":    e,
		"otel":          e.OTel(),
		"source_format": e.SourceFormat,
	}
	if *jsonOut {
		return emitJSON(stdout, result)
	}
	humanAgentEvent(stdout, e)
	return nil
}

// ─── init-sh (v0.41.0) ───────────────────────────────────────

// cliInitSh は `yagura init-sh <slug> [--target posix|powershell] [--write] [--json]`
// を処理する。long-running agent session 用の init script を生成する。
func cliInitSh(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("init-sh", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	write := fs.Bool("write", false, "write init.{sh,ps1} to project local_path")
	target := fs.String("target", "posix", "target: posix (default) or powershell/windows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: yagura init-sh <slug> [--target posix|powershell] [--write] [--json]")
	}
	slug := fs.Arg(0)

	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	p, err := reg.Get(slug)
	if err != nil {
		return fmt.Errorf("project %q not found", slug)
	}

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

	tgt := strings.ToLower(strings.TrimSpace(*target))
	var body, filename string
	var fileMode os.FileMode
	switch tgt {
	case "powershell", "ps1", "windows", "win":
		spec := initps1.BootSpec{
			Project:       slug,
			GeneratedBy:   "yagura",
			WorkDir:       p.LocalPath,
			Language:      p.Language,
			RequiredTools: tools,
			RequiredFiles: files,
			HandoffFiles:  []string{"claude-progress.txt", "AGENTS.md"},
		}
		body = initps1.Generate(spec)
		filename = "init.ps1"
		fileMode = 0o644
	case "", "posix", "sh", "bash", "unix", "linux", "macos", "darwin":
		spec := initsh.BootSpec{
			Project:       slug,
			GeneratedBy:   "yagura",
			WorkDir:       p.LocalPath,
			Language:      p.Language,
			RequiredTools: tools,
			RequiredFiles: files,
			HandoffFiles:  []string{"claude-progress.txt", "AGENTS.md"},
		}
		body = initsh.Generate(spec)
		filename = "init.sh"
		fileMode = 0o755
	default:
		return fmt.Errorf("unknown --target %q (use 'posix' or 'powershell')", *target)
	}

	result := map[string]any{
		"slug":     slug,
		"target":   tgt,
		"body":     body,
		"length":   len(body),
		"filename": filename,
	}
	if *write {
		if p.LocalPath == "" {
			return fmt.Errorf("project %q has no local_path; cannot write %s", slug, filename)
		}
		path := filepath.Join(p.LocalPath, filename)
		if err := os.WriteFile(path, []byte(body), fileMode); err != nil {
			return fmt.Errorf("write %s: %w", filename, err)
		}
		result["written_to"] = path
		fmt.Fprintf(stderr, "yagura: wrote %s\n", path)
	}
	if *jsonOut {
		return emitJSON(stdout, result)
	}
	humanInitSh(stdout, body, filename)
	return nil
}

// ─── progress-file (v0.41.0) ─────────────────────────────────

// cliProgressFile は `yagura progress-file <slug> [--note txt] [--write] [--json]`
// を処理する。Plan.md + registry から claude-progress.txt を生成する。
// hook/alert 状態は CLI からアクセスできないため degraded mode(Plan.md のみ)で動作。
func cliProgressFile(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("progress-file", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	write := fs.Bool("write", false, "write claude-progress.txt to project local_path")
	note := fs.String("note", "", "optional free-form intent / state note")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: yagura progress-file <slug> [--note txt] [--write] [--json]")
	}
	slug := fs.Arg(0)

	reg, err := openRegistry(stderr)
	if err != nil {
		return err
	}
	p, err := reg.Get(slug)
	if err != nil {
		return fmt.Errorf("project %q not found", slug)
	}

	snap := progressfile.Snapshot{
		Project:     slug,
		GeneratedBy: "yagura",
		Note:        *note,
	}
	if p.LocalPath != "" {
		if content, _, err2 := loadPlanMdLocal(p.LocalPath); err2 == nil {
			state := plantracker.Parse(content)
			snap.PlanProgressPct = state.ProgressPct
			snap.CurrentPhase = state.CurrentPhase
			pin := cliPlanStateToFeatureInput(slug, content, state)
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

	body := progressfile.Generate(snap)
	result := map[string]any{
		"slug":     slug,
		"body":     body,
		"length":   len(body),
		"filename": "claude-progress.txt",
	}
	if *write {
		if p.LocalPath == "" {
			return fmt.Errorf("project %q has no local_path; cannot write claude-progress.txt", slug)
		}
		path := filepath.Join(p.LocalPath, "claude-progress.txt")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return fmt.Errorf("write claude-progress.txt: %w", err)
		}
		result["written_to"] = path
		fmt.Fprintf(stderr, "yagura: wrote %s\n", path)
	}
	if *jsonOut {
		return emitJSON(stdout, result)
	}
	humanProgressFile(stdout, body)
	return nil
}

// ─── Plan.md helpers for CLI (mirrors internal/mcp/tools_guides.go) ──────────
// These replicate private helpers from the mcp package to avoid circular imports.

func cliExtractSection(content string, headers []string) string {
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

func cliExtractDoDItems(content string) []string {
	body := cliExtractSection(content, []string{"完了定義", "Definition of Done", "DoD"})
	if body == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(body, "\n") {
		ts := strings.TrimSpace(line)
		if !strings.HasPrefix(ts, "- ") && !strings.HasPrefix(ts, "* ") {
			continue
		}
		item := strings.TrimSpace(ts[2:])
		if strings.HasPrefix(item, "[ ]") || strings.HasPrefix(item, "[x]") || strings.HasPrefix(item, "[X]") {
			item = strings.TrimSpace(item[3:])
		}
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func cliPlanStateToFeatureInput(project, content string, state plantracker.PlanState) featurelist.PlanInput {
	pin := featurelist.PlanInput{
		Project: project,
		DoD:     cliExtractDoDItems(content),
	}
	lines := strings.Split(content, "\n")
	for i, ph := range state.Phases {
		lower := strings.ToLower(ph.Name)
		if !strings.Contains(lower, "phase") && !strings.Contains(ph.Name, "フェーズ") {
			continue
		}
		startLine := ph.LineStart
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
			var done bool
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

// ─── harness-recommend (v0.42.0) ──────────────────────────────

// cliHarnessRecommend は `yagura harness-recommend [--slug s|--language l] [--json]`
// を処理する。MCP yagura_harness_recommend と同一の harness.RecommendForLanguage を呼ぶ。
func cliHarnessRecommend(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("harness-recommend", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	slug := fs.String("slug", "", "project slug (looks up language from registry)")
	lang := fs.String("language", "", "language override (go/typescript/python/rust/...)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	language := strings.TrimSpace(*lang)
	if language == "" && *slug != "" {
		reg, err := openRegistry(stderr)
		if err != nil {
			return err
		}
		p, err := reg.Get(*slug)
		if err != nil {
			return fmt.Errorf("project %q not found", *slug)
		}
		language = p.Language
	}
	if language == "" {
		return fmt.Errorf("either --slug (with registered language) or --language must be provided")
	}

	rec := harness.RecommendForLanguage(language)
	if *jsonOut {
		return emitJSON(stdout, rec)
	}
	humanHarnessRecommend(stdout, rec)
	return nil
}

// ─── session-summary (v0.42.0) ────────────────────────────────

// cliSessionSummary は `yagura session-summary [--file f] [--json]` を処理する。
// JSON 配列形式のエージェントイベントを読み込み、sessionsummary.Summarize で集約する。
// daemon の hook timeline は CLI 非アクセスのため、--file/stdin でイベントを渡す。
func cliSessionSummary(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("session-summary", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	filePath := fs.String("file", "", "path to JSON array of events (default: stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	data, err := readInputData(*filePath, os.Stdin)
	if err != nil {
		return fmt.Errorf("session-summary: %w", err)
	}

	var rawEvents []map[string]any
	if err := json.Unmarshal(data, &rawEvents); err != nil {
		return fmt.Errorf("session-summary: expected JSON array of events: %w", err)
	}

	norm := make([]agentevent.Event, 0, len(rawEvents))
	for _, raw := range rawEvents {
		norm = append(norm, agentevent.Normalize(raw))
	}

	sum := sessionsummary.Summarize(norm)
	if *jsonOut {
		return emitJSON(stdout, sum)
	}
	humanSessionSummary(stdout, sum)
	return nil
}

// ─── parallel-plan (v0.44.0) ──────────────────────────────────

// cliParallelPlan は `yagura parallel-plan [--file f] [--json]` を処理する。
// MCP yagura_parallel_plan と同一の agentparallel.PlanDataParallel を呼ぶ。
// Input JSON: {"tasks":[{id,weight?,min_tier?}...], "agents":[{name,tier?,capacity_percent?,max_concurrency?}...], "task_count"?:N, "global_concurrency"?:N}
func cliParallelPlan(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("parallel-plan", stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	filePath := fs.String("file", "", "path to JSON input file (default: stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	data, err := readInputData(*filePath, os.Stdin)
	if err != nil {
		return fmt.Errorf("parallel-plan: %w", err)
	}

	var in struct {
		Tasks []struct {
			ID      string  `json:"id"`
			Weight  float64 `json:"weight"`
			MinTier string  `json:"min_tier"`
		} `json:"tasks"`
		TaskCount         int `json:"task_count"`
		GlobalConcurrency int `json:"global_concurrency"`
		Agents            []struct {
			Name            string `json:"name"`
			Tier            string `json:"tier"`
			CapacityPercent *int   `json:"capacity_percent"`
			MaxConcurrency  int    `json:"max_concurrency"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("parallel-plan: invalid JSON: %w", err)
	}
	if len(in.Agents) == 0 {
		return fmt.Errorf("parallel-plan: at least one agent is required")
	}

	var tasks []agentparallel.Task
	if len(in.Tasks) > 0 {
		for i, t := range in.Tasks {
			id := strings.TrimSpace(t.ID)
			if id == "" {
				id = fmt.Sprintf("task-%d", i+1)
			}
			tier, ok := cliParseTier(t.MinTier)
			if !ok {
				return fmt.Errorf("task %q: unknown min_tier %q (use any/cheap/mid/strong)", id, t.MinTier)
			}
			tasks = append(tasks, agentparallel.Task{ID: id, Weight: t.Weight, MinTier: tier})
		}
	} else if in.TaskCount > 0 {
		for i := 0; i < in.TaskCount; i++ {
			tasks = append(tasks, agentparallel.Task{ID: fmt.Sprintf("task-%d", i+1), Weight: 1})
		}
	} else {
		return fmt.Errorf("parallel-plan: provide 'tasks' or a positive 'task_count'")
	}

	agents := make([]agentparallel.Agent, 0, len(in.Agents))
	for _, a := range in.Agents {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			return fmt.Errorf("parallel-plan: agent name must not be empty")
		}
		tier, ok := cliParseTier(a.Tier)
		if !ok {
			return fmt.Errorf("agent %q: unknown tier %q (use any/cheap/mid/strong)", name, a.Tier)
		}
		capPct := 100
		if a.CapacityPercent != nil {
			capPct = *a.CapacityPercent
		}
		agents = append(agents, agentparallel.Agent{
			Name:           name,
			Tier:           tier,
			CapacityPct:    capPct,
			MaxConcurrency: a.MaxConcurrency,
		})
	}

	plan := agentparallel.PlanDataParallel(tasks, agents, in.GlobalConcurrency)
	if *jsonOut {
		return emitJSON(stdout, plan)
	}
	humanParallelPlan(stdout, plan)
	return nil
}

// cliGraphNeighbors は registry の depends_on graph を BFS で探索し近傍を返す。
func cliGraphNeighbors(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("graph-neighbors", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	depth := fs.Int("depth", 2, "max hops (1-10)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	slug := fs.Arg(0)
	if slug == "" {
		fmt.Fprintln(stderr, "usage: yagura graph-neighbors [--json] [--depth N] <slug>")
		return fmt.Errorf("graph-neighbors: slug required")
	}
	if *depth < 1 {
		*depth = 1
	}
	if *depth > 10 {
		*depth = 10
	}
	reg, err := openRegistry(stderr)
	if err != nil {
		return fmt.Errorf("graph-neighbors: %w", err)
	}
	g := projectgraph.Build(cliToGraphProjects(reg.List()))
	result := g.Neighbors(slug, *depth)
	if *jsonOut {
		return emitJSON(stdout, result)
	}
	humanGraphNeighbors(stdout, result)
	return nil
}

// cliGraphImpact は slug を変更した場合の影響範囲(transitive reverse deps)を返す。
func cliGraphImpact(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("graph-impact", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	slug := fs.Arg(0)
	if slug == "" {
		fmt.Fprintln(stderr, "usage: yagura graph-impact [--json] <slug>")
		return fmt.Errorf("graph-impact: slug required")
	}
	reg, err := openRegistry(stderr)
	if err != nil {
		return fmt.Errorf("graph-impact: %w", err)
	}
	g := projectgraph.Build(cliToGraphProjects(reg.List()))
	result := g.Impact(slug)
	if *jsonOut {
		return emitJSON(stdout, result)
	}
	humanGraphImpact(stdout, result)
	return nil
}

// cliGraphStats は registry の depends_on graph の統計を返す。
func cliGraphStats(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("graph-stats", stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	reg, err := openRegistry(stderr)
	if err != nil {
		return fmt.Errorf("graph-stats: %w", err)
	}
	g := projectgraph.Build(cliToGraphProjects(reg.List()))
	stats := g.Stats()
	dangling := g.Dangling()
	if *jsonOut {
		return emitJSON(stdout, map[string]any{"stats": stats, "dangling": dangling})
	}
	humanGraphStats(stdout, stats, dangling)
	return nil
}

// cliToGraphProjects は registry.Project → projectgraph.Project へ変換する
// (mcp.toGraphProjects の複製 — 循環 import 回避)。
func cliToGraphProjects(ps []*project.Project) []projectgraph.Project {
	out := make([]projectgraph.Project, 0, len(ps))
	for _, p := range ps {
		out = append(out, projectgraph.Project{Slug: p.Slug, DependsOn: p.DependsOn})
	}
	return out
}

// cliParseTier は tier 文字列を agentparallel.Tier に変換する
// (mcp.parseTier の複製 — 循環 import 回避)。
func cliParseTier(s string) (agentparallel.Tier, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "any":
		return agentparallel.TierAny, true
	case "cheap", "haiku":
		return agentparallel.TierCheap, true
	case "mid", "sonnet", "medium":
		return agentparallel.TierMid, true
	case "strong", "opus", "high":
		return agentparallel.TierStrong, true
	}
	return agentparallel.TierAny, false
}

// auditMutation は registry 変更を audit log に best-effort で追記する。
// CLAUDE.md の方針どおり、audit 失敗は warning に留めコマンド自体は失敗させない。
func auditMutation(stderr io.Writer, kind, target string, fields map[string]any) {
	sd, err := config.ResolveStateDir()
	if err != nil {
		fmt.Fprintf(stderr, "yagura: warning: audit skipped: %v\n", err)
		return
	}
	log, err := audit.New(config.AuditDirFor(sd))
	if err != nil {
		fmt.Fprintf(stderr, "yagura: warning: audit unavailable: %v\n", err)
		return
	}
	defer func() { log.Close() }()
	if err := log.Append(audit.Record{Kind: kind, Actor: "cli", Target: target, Fields: fields}); err != nil {
		fmt.Fprintf(stderr, "yagura: warning: audit append failed: %v\n", err)
	}
}

// ─── Shell completion ──────────────────────────────────────────────────────

// yaguraVerbs is the canonical sorted list of all CLI verbs (cliHandlers + main dispatch).
// Used by cliCompletion to generate shell completion scripts.
var yaguraVerbs = []string{
	"agent-config-audit", "agent-event", "agents-md", "ai-verify",
	"alert-fix", "alert-resolve", "alert-snapshot", "api-doc",
	"assert-check", "ast-check", "cc-security", "claudemd-audit",
	"code-health", "complexity", "completion", "coupling", "coverage", "ctx-check",
	"dead-code", "dep-rank", "diff-scan", "err-discard", "err-policy", "err-wrap", "feature-list", "flag-arg", "flow-risk",
	"gha-audit", "get", "graph", "graph-impact", "graph-neighbors", "graph-stats",
	"harness-coverage", "harness-recommend", "help", "hotspot", "init-sh", "inject-scan",
	"list", "mcp-audit", "name-check", "ops-risk", "parallel-plan", "param-check", "path-policy",
	"pin-drift", "plan-status", "plugin-audit", "progress-file", "publicity-scan",
	"quality-check", "recv-check", "recovery-decide", "register", "release-radar",
	"return-check", "review-gate", "risk-triage", "sbom", "search", "secret", "secretscan",
	"self-improve-history", "session-summary", "settings-audit", "skill-audit",
	"stats", "sync-check", "test-audit", "today", "unregister", "update", "vex-audit",
	"verify", "version", "workflow-audit",
}

// cliCompletion は bash/zsh/fish のシェル補完スクリプトを stdout に出力する。
// Usage: source <(yagura completion bash)
//
//	yagura completion zsh  > ~/.zfunc/_yagura
//	yagura completion fish > ~/.config/fish/completions/yagura.fish
func cliCompletion(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("completion", stderr)
	positionals, err := parseArgs(fs, args)
	if err != nil {
		return errUsage
	}
	shell := "bash"
	if len(positionals) > 0 {
		shell = positionals[0]
	}

	verbList := strings.Join(yaguraVerbs, " ")

	switch shell {
	case "bash":
		fmt.Fprintf(stdout, bashCompletionTmpl, verbList)
	case "zsh":
		fmt.Fprintf(stdout, zshCompletionTmpl, buildZshVerbLines())
	case "fish":
		fmt.Fprintf(stdout, fishCompletionTmpl, verbList, verbList)
	default:
		fmt.Fprintf(stderr, "completion: unknown shell %q — use bash, zsh, or fish\n", shell)
		return errUsage
	}
	return nil
}

const bashCompletionTmpl = `# yagura bash completion
# Add to ~/.bashrc: source <(yagura completion bash)
_yagura_completion() {
    local cur="${COMP_WORDS[COMP_CWORD]}"
    local verbs="%s"
    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=($(compgen -W "$verbs" -- "$cur"))
    fi
}
complete -F _yagura_completion yagura
`

const zshCompletionTmpl = `# yagura zsh completion
# Add to ~/.zshrc: source <(yagura completion zsh)
# Or: yagura completion zsh > "${fpath[1]}/_yagura"
_yagura() {
    local -a verbs
    verbs=(
%s    )
    _describe 'command' verbs
}
compdef _yagura yagura
`

const fishCompletionTmpl = `# yagura fish completion
# Usage: yagura completion fish | source
#        or: yagura completion fish > ~/.config/fish/completions/yagura.fish
complete -c yagura -f
complete -c yagura -n "not __fish_seen_subcommand_from %s" -a "%s"
`

// buildZshVerbLines builds the zsh _describe array entries with short descriptions.
func buildZshVerbLines() string {
	descriptions := map[string]string{
		"agent-config-audit":   "audit OpenClaw-style openclaw.json (security/reliability)",
		"agent-event":          "normalize agent lifecycle event to OTel GenAI semconv",
		"agents-md":            "generate AGENTS.md from Plan.md + registry facts",
		"ai-verify":            "AI code risk audit (auth/billing/data/crypto/secret)",
		"alert-fix":            "portfolio health sweep over sensor data",
		"alert-resolve":        "manage alert lifecycle (resolve/snooze/reopen)",
		"alert-snapshot":       "show current lifecycle state of all tracked alerts",
		"api-doc":              "exported-API doc discipline: documented ratio",
		"assert-check":         "test assertion density: detect hollow test files",
		"ast-check":            "Go AST structural audit (os.Exit/panic in library)",
		"cc-security":          "audit a project's Claude Code security posture",
		"claudemd-audit":       "audit CLAUDE.md structure (4 sections, instruction budget)",
		"code-health":          "composite maintainability grade (A-F) per package",
		"complexity":           "cyclomatic complexity (McCabe, gocyclo-compatible)",
		"completion":           "generate shell completion script (bash|zsh|fish)",
		"coupling":             "package import coupling: fan-in/out + instability",
		"coverage":             "scan blind-spot report: covered vs uncovered-source",
		"dead-code":            "dead unexported declarations within their own package",
		"dep-rank":             "package import in-degree rank (blast radius when changed)",
		"diff-scan":            "delta scan of unified diff: secrets in added lines",
		"err-discard":          "error-discard smell: call sites silently ignoring returned errors",
		"err-policy":           "error-context discipline: wrap ratio + blank-discard",
		"err-wrap":             "error-wrapping discipline: %w over %v, errors.Is/As over ==/type-assert",
		"sync-check":           "sync-lock copy discipline: methods/params/returns must not copy types containing sync.Mutex",
		"feature-list":         "convert Plan.md to Anthropic-style feature-list.json",
		"flag-arg":             "boolean flag-argument smell (Fowler); bool params encoding hidden branches",
		"flow-risk":            "temporal scan of op sequence: exfiltration / injection",
		"return-check":         "many-return-values smell; output width — pair to param-check (input width)",
		"gha-audit":            "GitHub Actions workflow audit (pinning/permissions)",
		"get":                  "get project by slug",
		"graph":                "dependency graph queries over registry depends_on",
		"graph-impact":         "transitive reverse deps (change impact)",
		"graph-neighbors":      "BFS: direct+transitive deps/dependents",
		"graph-stats":          "graph summary: nodes/edges/roots/hubs",
		"harness-coverage":     "Fowler taxonomy self-audit (4 quadrants)",
		"harness-recommend":    "Claude Code .claude/ scaffold by language",
		"help":                 "print help message",
		"hotspot":              "convergent-signal hotspots: functions flagged by 2+ signature lenses",
		"name-check":           "name↔signature consistency: predicates return bool, getters/constructors return a value",
		"ctx-check":            "context.Context discipline: must be first param, not stored in struct fields",
		"init-sh":              "generate init.sh or init.ps1 for agent sessions",
		"inject-scan":          "scan untrusted content for indirect prompt injection",
		"list":                 "list registry projects",
		"mcp-audit":            "audit .mcp.json / tools for poisoning & config risk",
		"ops-risk":             "classify operation autonomy tier (auto/log/review/human)",
		"parallel-plan":        "LPT fan-out plan across AI agents",
		"param-check":          "long-parameter-list smell (Fowler); complexity's horizontal pair",
		"path-policy":          "gate changed paths against .yagura/paths.json",
		"pin-drift":            "SHA-pinned dep drift (needs YAGURA_GITHUB_TOKEN)",
		"plan-status":          "Plan.md progress for a project (checkboxes)",
		"plugin-audit":         "audit Claude Code plugin.json / marketplace.json",
		"progress-file":        "generate claude-progress.txt for cross-session handoff",
		"publicity-scan":       "pre-publish leak scan (home paths, IPs, emails)",
		"quality-check":        "code lint: prohibited patterns, TODO/FIXME, ts-ignore",
		"recv-check":           "method receiver consistency: inconsistent names",
		"recovery-decide":      "pick recovery action for a failed agent task",
		"register":             "register a project in the registry",
		"release-radar":        "cross-project release readiness ranking",
		"review-gate":          "composite Review verdict (allow/review/block)",
		"risk-triage":          "compound CVE/vulnerability prioritization",
		"sbom":                 "software bill of materials",
		"search":               "search registry projects",
		"secret":               "manage encrypted secrets (list/set/get/delete)",
		"secretscan":           "secret detection in project text fields",
		"self-improve-history": "show the recorded RSI self-assessment trajectory",
		"session-summary":      "aggregate agent event array to session summary",
		"settings-audit":       "audit .claude/settings.json (permissions/hooks)",
		"skill-audit":          "audit .claude/skills (score + retire recommendations)",
		"stats":                "registry statistics",
		"test-audit":           "source-test coverage detection (Go/TS/JS/Python/Rust)",
		"today":                "top portfolio projects to focus on now",
		"unregister":           "unregister a project from the registry",
		"update":               "update a project in the registry",
		"vex-audit":            "validate OpenVEX docs/vex/*.json",
		"verify":               "verify integrity of audit log hash chain",
		"version":              "print version and exit",
		"workflow-audit":       "audit .claude/workflows (Dynamic Workflow lint)",
	}
	var sb strings.Builder
	for _, v := range yaguraVerbs {
		desc := descriptions[v]
		if desc == "" {
			desc = v
		}
		fmt.Fprintf(&sb, "        %q\n", v+":"+desc)
	}
	return sb.String()
}
