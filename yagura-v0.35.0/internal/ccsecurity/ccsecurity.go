// Package ccsecurity は Claude Code を使うプロジェクトの「セキュリティ姿勢」を
// 決定論的に監査する。
//
// 動機:
//
//	Claude Code 初心者向けの定番セキュリティ対策(機密ファイルを置かない、
//	--dangerously-skip-permissions を使わない、permission の deny ルールを置く、
//	CLAUDE.md にセキュリティルールを書く、git でロールバック点を作る、MCP server を
//	最小限にする…)の多くは、プロジェクトのファイル構成・設定から機械的に判定できる。
//	yagura の他の audit と同様、rule-based・zero-dep・deterministic に「今この
//	プロジェクトは最低限の対策ができているか」をスコア化する。
//
// 設計判断:
//   - ドメインは I/O を持たない純粋関数 Audit(Input) Report。ファイル存在の探索や
//     読み込みは呼出側(CLI)が行い、抽出済みの事実 + 必要な本文だけを Input で渡す。
//     これによりロジックが完全にテスト可能になり、scanner 専用の trust base も汚さない。
//   - 機械判定できない人手プロセス項目(学習データ off / spending limit / active
//     session 確認 / Plan Mode / /clear など)はスコアに含めず、ManualPractices として
//     常にガイダンス提示する(「測れないから無い」ではなく「測れないが重要」)。
//   - 出力は ID 昇順で決定論的。
package ccsecurity

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Severity は finding の深刻度。
type Severity string

const (
	// SevCritical は即時対応が必要な最高深刻度の finding。
	SevCritical Severity = "CRITICAL"
	// SevHigh は優先対応が推奨される高深刻度の finding。
	SevHigh Severity = "HIGH"
	// SevMedium は計画的対応が推奨される中深刻度の finding。
	SevMedium Severity = "MEDIUM"
	// SevLow は任意対応の低深刻度の finding。
	SevLow Severity = "LOW"
	// SevNone は severity なし(pass / n/a の場合)。
	SevNone Severity = "" // pass / n-a
)

// Status は 1 つの practice の判定結果。
type Status string

const (
	// StatusPass は機械判定で対策済みと確認できた状態。
	StatusPass Status = "pass"
	// StatusWarn は対策が部分的または推奨事項を満たしていない状態。
	StatusWarn Status = "warn"
	// StatusFail は対策が不足している状態。
	StatusFail Status = "fail"
	// StatusNA は判定対象外(ファイルが存在しない等)の状態。
	StatusNA Status = "n/a"
)

// Practice は機械判定した 1 項目の結果。
type Practice struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      Status   `json:"status"`
	Severity    Severity `json:"severity,omitempty"`
	Detail      string   `json:"detail,omitempty"`
	Remediation string   `json:"remediation,omitempty"`
}

// ManualPractice は機械判定できない人手プロセス項目(ガイダンス用)。
type ManualPractice struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Report は監査結果。
type Report struct {
	Score           int              `json:"score"` // 0-100, 機械判定項目に対する減点式
	Checked         int              `json:"checked"`
	Passed          int              `json:"passed"`
	Warned          int              `json:"warned"`
	Failed          int              `json:"failed"`
	Practices       []Practice       `json:"practices"`
	ManualPractices []ManualPractice `json:"manual_practices"`
}

// NamedText は走査対象の補助ファイル(scripts / README など)。
type NamedText struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Input は監査に必要な、呼出側が収集済みの事実。
type Input struct {
	EnvFiles       []string    `json:"env_files,omitempty"` // プロジェクト内で見つかった .env 系ファイル名
	HasGitignore   bool        `json:"has_gitignore"`
	Gitignore      string      `json:"gitignore,omitempty"`
	HasGitDir      bool        `json:"has_git_dir"`
	HasClaudeMd    bool        `json:"has_claude_md"`
	ClaudeMd       string      `json:"claude_md,omitempty"`
	HasSettings    bool        `json:"has_settings"`
	SettingsJSON   string      `json:"settings_json,omitempty"`
	HasWorklog     bool        `json:"has_worklog"`
	MCPServerCount int         `json:"mcp_server_count"`
	ExtraText      []NamedText `json:"extra_text,omitempty"` // scripts / docs(危険フラグ走査用)
}

// mcpServerWarnThreshold を超える MCP server 数は「本当に全部使うか」の review nudge。
const mcpServerWarnThreshold = 6

var (
	reDangerousSkip = regexp.MustCompile(`--dangerously-skip-permissions`)
	// CLAUDE.md がセキュリティルールに言及しているかの緩い判定。
	reSecurityKeyword = regexp.MustCompile(`(?i)(\.env|secret|credential|token|rm -rf|dangerously|機密|セキュリティ|security)`)
)

// Audit は Input を評価して Report を返す。純粋関数・決定論的。
func Audit(in Input) Report {
	score := 100
	var ps []Practice

	// ── P05: --dangerously-skip-permissions を使っていないか(最優先)──
	if files := scanDangerousSkip(in); len(files) > 0 {
		score -= 40
		ps = append(ps, Practice{
			ID: "P05-dangerous-skip", Title: "Never use --dangerously-skip-permissions",
			Status: StatusFail, Severity: SevCritical,
			Detail:      "found in: " + strings.Join(files, ", "),
			Remediation: "remove the flag; approve operations individually instead",
		})
	} else {
		ps = append(ps, Practice{
			ID: "P05-dangerous-skip", Title: "Never use --dangerously-skip-permissions",
			Status: StatusPass,
		})
	}

	// ── P02: .env / 機密ファイルをプロジェクト内に置いていないか ──
	if len(in.EnvFiles) > 0 {
		score -= 25
		ps = append(ps, Practice{
			ID: "P02-env-in-project", Title: "Keep .env / secrets out of the project folder",
			Status: StatusFail, Severity: SevHigh,
			Detail:      "found: " + strings.Join(in.EnvFiles, ", "),
			Remediation: "move secret files outside the project, or use a secret manager / env vars",
		})
		// P02-gitignore: .env があるなら最低限 gitignore で守られているか。
		if envCoveredByGitignore(in.Gitignore) {
			ps = append(ps, Practice{
				ID: "P02-env-gitignore", Title: ".env is covered by .gitignore",
				Status: StatusPass,
			})
		} else {
			score -= 10
			ps = append(ps, Practice{
				ID: "P02-env-gitignore", Title: ".env is covered by .gitignore",
				Status: StatusFail, Severity: SevMedium,
				Detail:      "an .env file is present but .gitignore does not exclude it",
				Remediation: "add `.env` to .gitignore (and prefer not keeping it in-project at all)",
			})
		}
	} else {
		ps = append(ps, Practice{
			ID: "P02-env-in-project", Title: "Keep .env / secrets out of the project folder",
			Status: StatusPass,
		})
	}

	// ── P06: settings.json に permission の deny ルールがあるか ──
	switch {
	case !in.HasSettings:
		score -= 12
		ps = append(ps, Practice{
			ID: "P06-permission-deny", Title: "Configure permission deny rules",
			Status: StatusWarn, Severity: SevMedium,
			Detail:      "no .claude/settings.json — no permission guardrails are configured",
			Remediation: "add a settings.json with a permissions.deny list (e.g. Read(./.env), Bash(rm -rf*))",
		})
	case hasDenyRules(in.SettingsJSON):
		ps = append(ps, Practice{
			ID: "P06-permission-deny", Title: "Configure permission deny rules",
			Status: StatusPass,
		})
	default:
		score -= 8
		ps = append(ps, Practice{
			ID: "P06-permission-deny", Title: "Configure permission deny rules",
			Status: StatusWarn, Severity: SevLow,
			Detail:      "settings.json has no permissions.deny entries",
			Remediation: "add a permissions.deny list to block secret reads and destructive commands",
		})
	}

	// ── P07: CLAUDE.md にセキュリティルールが書かれているか ──
	switch {
	case !in.HasClaudeMd:
		score -= 15
		ps = append(ps, Practice{
			ID: "P07-claude-md-rules", Title: "Write security rules in CLAUDE.md",
			Status: StatusWarn, Severity: SevMedium,
			Detail:      "no CLAUDE.md — Claude Code has no project-level security rules to follow",
			Remediation: "add CLAUDE.md with rules (never read .env/secrets, confirm rm -rf / external curl)",
		})
	case reSecurityKeyword.MatchString(in.ClaudeMd):
		ps = append(ps, Practice{
			ID: "P07-claude-md-rules", Title: "Write security rules in CLAUDE.md",
			Status: StatusPass,
		})
	default:
		score -= 8
		ps = append(ps, Practice{
			ID: "P07-claude-md-rules", Title: "Write security rules in CLAUDE.md",
			Status: StatusWarn, Severity: SevLow,
			Detail:      "CLAUDE.md exists but mentions no security rules",
			Remediation: "add a security section (secrets, destructive commands, external URLs)",
		})
	}

	// ── P08: git でロールバック点を作れる状態か ──
	if in.HasGitDir {
		ps = append(ps, Practice{
			ID: "P08-git-rollback", Title: "Use git for a rollback point before big changes",
			Status: StatusPass,
		})
	} else {
		score -= 5
		ps = append(ps, Practice{
			ID: "P08-git-rollback", Title: "Use git for a rollback point before big changes",
			Status: StatusWarn, Severity: SevLow,
			Detail:      "no .git — there is no commit to roll back to if a change goes wrong",
			Remediation: "git init and commit before letting Claude Code make large changes",
		})
	}

	// ── P12: MCP server を最小限にしているか ──
	if in.MCPServerCount > mcpServerWarnThreshold {
		score -= 5
		ps = append(ps, Practice{
			ID: "P12-mcp-minimal", Title: "Enable only the MCP servers you use",
			Status: StatusWarn, Severity: SevLow,
			Detail:      fmt.Sprintf("%d MCP servers configured — each widens what data is reachable", in.MCPServerCount),
			Remediation: "remove MCP servers you are not actively using",
		})
	} else {
		ps = append(ps, Practice{
			ID: "P12-mcp-minimal", Title: "Enable only the MCP servers you use",
			Status: StatusPass,
		})
	}

	// ── P16: WORKLOG を残しているか(suggestion 寄り、減点は軽微なし)──
	if in.HasWorklog {
		ps = append(ps, Practice{
			ID: "P16-worklog", Title: "Keep a WORKLOG of what you asked Claude Code to do",
			Status: StatusPass,
		})
	} else {
		ps = append(ps, Practice{
			ID: "P16-worklog", Title: "Keep a WORKLOG of what you asked Claude Code to do",
			Status: StatusWarn, Severity: SevLow,
			Detail:      "no WORKLOG.md — harder to trace what was done if something breaks",
			Remediation: "keep a WORKLOG.md (or detailed commit messages)",
		})
	}

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })

	r := Report{Score: score, Practices: ps, ManualPractices: manualPractices()}
	for _, p := range ps {
		r.Checked++
		switch p.Status {
		case StatusPass:
			r.Passed++
		case StatusWarn:
			r.Warned++
		case StatusFail:
			r.Failed++
		}
	}
	return r
}

// scanDangerousSkip は settings / CLAUDE.md / ExtraText から
// --dangerously-skip-permissions を含む source 名を昇順で返す。
func scanDangerousSkip(in Input) []string {
	var hits []string
	if reDangerousSkip.MatchString(in.SettingsJSON) {
		hits = append(hits, "settings.json")
	}
	if reDangerousSkip.MatchString(in.ClaudeMd) {
		hits = append(hits, "CLAUDE.md")
	}
	for _, t := range in.ExtraText {
		if reDangerousSkip.MatchString(t.Text) {
			hits = append(hits, t.Name)
		}
	}
	sort.Strings(hits)
	return hits
}

// envCoveredByGitignore は .gitignore が .env を除外しているかを緩く判定する。
func envCoveredByGitignore(gitignore string) bool {
	for _, line := range strings.Split(gitignore, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// ".env" / "*.env" / ".env*" を許容。
		if line == ".env" || line == "*.env" || strings.HasPrefix(line, ".env") {
			return true
		}
	}
	return false
}

// hasDenyRules は settings.json の permissions.deny が非空かを返す。
func hasDenyRules(settingsJSON string) bool {
	var doc struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(settingsJSON), &doc); err != nil {
		return false
	}
	return len(doc.Permissions.Deny) > 0
}

// manualPractices は機械判定できない人手プロセス項目(常にガイダンス提示)。
func manualPractices() []ManualPractice {
	return []ManualPractice{
		{"P01-dedicated-folder", "Launch Claude Code only inside a dedicated project folder"},
		{"P03-training-optout", "Turn off Claude training-data usage in privacy settings"},
		{"P04-plan-mode", "Use Plan Mode: review the plan before executing"},
		{"P09-untrusted-clone", "Don't run Claude Code on untrusted cloned repositories"},
		{"P10-spending-limit", "Set a spending limit on your Anthropic API key"},
		{"P11-no-secrets-in-chat", "Never paste passwords/API keys into the chat"},
		{"P13-clear-context", "Use /clear to reset context, especially after sensitive work"},
		{"P14-review-before-commit", "Visually review Claude-written code before committing"},
		{"P15-no-raw-external-urls", "Don't paste untrusted external URLs into the prompt"},
		{"P17-active-sessions", "Periodically review active sessions in claude.ai settings"},
		{"P18-keep-updated", "Keep Claude Code updated to the latest version"},
	}
}
