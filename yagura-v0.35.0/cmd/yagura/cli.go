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

	"github.com/shizukutanaka/yagura/internal/audit"
	"github.com/shizukutanaka/yagura/internal/ccsecurity"
	"github.com/shizukutanaka/yagura/internal/config"
	"github.com/shizukutanaka/yagura/internal/ghaaudit"
	"github.com/shizukutanaka/yagura/internal/github"
	"github.com/shizukutanaka/yagura/internal/harness"
	"github.com/shizukutanaka/yagura/internal/injectscan"
	"github.com/shizukutanaka/yagura/internal/pathpolicy"
	"github.com/shizukutanaka/yagura/internal/pindrift"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/publicityscan"
	"github.com/shizukutanaka/yagura/internal/registry"
	"github.com/shizukutanaka/yagura/internal/sbom"
	"github.com/shizukutanaka/yagura/internal/secretscan"
	"github.com/shizukutanaka/yagura/internal/vex"
)

// mainModulePath は sbom 生成対象(yagura 自身)の module path。
// run() のハードコードと同じ値を CLI でも使う。
const mainModulePath = "github.com/shizukutanaka/yagura"

// errUsage は flag parse / 引数不足を示す番兵。runCLI で exit code 2 に変換する。
// FlagSet 側が既に詳細を stderr に出している。
var errUsage = errors.New("usage error")

// cliVerbs は dispatch() が runCLI に委譲する direct-mode subcommand の集合。
var cliVerbs = map[string]bool{
	"list": true, "get": true, "search": true, "stats": true,
	"register": true, "update": true, "unregister": true,
	"sbom": true, "secretscan": true, "gha-audit": true, "pin-drift": true,
	"skill-audit": true, "workflow-audit": true, "settings-audit": true,
	"agent-config-audit": true, "plugin-audit": true, "publicity-scan": true,
	"mcp-audit": true, "vex-audit": true, "self-improve-history": true,
	"path-policy": true, "inject-scan": true, "cc-security": true,
	"claudemd-audit": true,
}

// runCLI は direct-mode subcommand を実行し、プロセス exit code を返す。
func runCLI(verb string, args []string, stdout, stderr io.Writer) int {
	var err error
	switch verb {
	case "list":
		err = cliList(args, stdout, stderr)
	case "get":
		err = cliGet(args, stdout, stderr)
	case "search":
		err = cliSearch(args, stdout, stderr)
	case "stats":
		err = cliStats(args, stdout, stderr)
	case "register":
		err = cliRegister(args, stdout, stderr)
	case "update":
		err = cliUpdate(args, stdout, stderr)
	case "unregister":
		err = cliUnregister(args, stdout, stderr)
	case "sbom":
		err = cliSbom(args, stdout, stderr)
	case "secretscan":
		err = cliSecretScan(args, stdout, stderr)
	case "gha-audit":
		err = cliGhaAudit(args, stdout, stderr)
	case "pin-drift":
		err = cliPinDrift(args, stdout, stderr)
	case "skill-audit":
		err = cliSkillAudit(args, stdout, stderr)
	case "workflow-audit":
		err = cliWorkflowAudit(args, stdout, stderr)
	case "settings-audit":
		err = cliSettingsAudit(args, stdout, stderr)
	case "agent-config-audit":
		err = cliAgentConfigAudit(args, stdout, stderr)
	case "plugin-audit":
		err = cliPluginAudit(args, stdout, stderr)
	case "mcp-audit":
		err = cliMCPAudit(args, stdout, stderr)
	case "publicity-scan":
		err = cliPublicityScan(args, stdout, stderr)
	case "vex-audit":
		err = cliVexAudit(args, stdout, stderr)
	case "self-improve-history":
		err = cliSelfImproveHistory(args, stdout, stderr)
	case "path-policy":
		err = cliPathPolicy(args, stdout, stderr)
	case "inject-scan":
		err = cliInjectScan(args, stdout, stderr)
	case "cc-security":
		err = cliCCSecurity(args, stdout, stderr)
	case "claudemd-audit":
		err = cliClaudeMdAudit(args, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "yagura: unknown command %q\n", verb)
		return 2
	}
	if err != nil {
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
	result := secretscan.New().ScanBatch(items)
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
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	files, err := readWorkflowFiles(*dir)
	if err != nil {
		return err
	}
	results := ghaaudit.New().AuditDir(*dir, files)
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

// resolveSingleAuditTarget は単一ファイル audit(agent-config / plugin / mcp)で
// 共通の前段を担う: 対象パスを解決(--file を位置引数で上書き)し、
//   - ファイル不在なら "graceful zero"(scanned:0 / flagged:0 / itemsKey:[])を
//     出力して handled=true を返す(未配置リポジトリでも素直に 0 件)。
//   - 在れば content を読み出して返す(read 失敗は "read <p>:" で wrap)。
// 呼出側は `if handled || err != nil { return err }` で分岐する。itemsKey は
// JSON 空配列のキー("configs" / "manifests")で、各 audit の出力 shape に合わせる。
func resolveSingleAuditTarget(stdout io.Writer, fileFlag string, rest []string, jsonOut bool, itemsKey string) (path, content string, handled bool, err error) {
	path = fileFlag
	if len(rest) > 0 {
		path = rest[0] // 位置引数があれば優先
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if jsonOut {
			return path, "", true, emitJSON(stdout, map[string]any{"scanned": 0, "flagged": 0, itemsKey: []any{}})
		}
		fmt.Fprintln(stdout, "scanned: 0   flagged: 0")
		return path, "", true, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return path, "", false, fmt.Errorf("read %s: %w", path, err)
	}
	return path, string(data), false, nil
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
	path, content, handled, err := resolveSingleAuditTarget(stdout, *file, rest, *jsonOut, "configs")
	if handled || err != nil {
		return err
	}
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
	path, content, handled, err := resolveSingleAuditTarget(stdout, *file, rest, *jsonOut, "manifests")
	if handled || err != nil {
		return err
	}
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
	path, content, handled, err := resolveSingleAuditTarget(stdout, *file, rest, *jsonOut, "configs")
	if handled || err != nil {
		return err
	}
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
	rest, err := parseArgs(fs, args)
	if err != nil {
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
	var findings []publicityFinding
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		for _, f := range publicityscan.Scan(string(data)) {
			findings = append(findings, publicityFinding{Path: p, Finding: f})
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
	rest, err := parseArgs(fs, args)
	if err != nil {
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
			findings = append(findings, injectFinding{Path: p, Finding: f})
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
	defer func() { _ = log.Close() }()
	if err := log.Append(audit.Record{Kind: kind, Actor: "cli", Target: target, Fields: fields}); err != nil {
		fmt.Fprintf(stderr, "yagura: warning: audit append failed: %v\n", err)
	}
}
