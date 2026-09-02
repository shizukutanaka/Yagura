// settings_audit.go: .claude/settings.json の heuristic 評価。
//
// Boris Cherny(Claude Code 作者)の customization ガイド準拠。settings.json は
// 9 つの customization 軸を束ねる設定基盤で、特に security の要:
//
//   - permissions(allow/deny)を git に check-in してチーム共通の policy にする
//   - deny で破壊的コマンド(`Bash(rm -rf *)` 等)を必ず塞ぐ
//   - `Bash(*)` のような無制限 allow は 4 層防御(prompt-injection 検出 /
//     static 解析 / sandbox / human oversight)を骨抜きにするので避ける
//   - `--dangerously-skip-permissions` ではなく sandbox + permissions を使う
//   - hooks で formatter 自動実行などの規約を deterministic に強制する
//
// 全 check は heuristic(構造ベース)。AuditSkill / AuditSubagent / AuditWorkflow
// と同じ shape(Score + Issues + Suggestions)で揃える。settings.json は strict
// JSON なので stdlib encoding/json で parse する(ADR-0001: 依存ゼロ)。
package harness

import (
	"encoding/json"
	"regexp"
	"strings"
)

// SettingsAuditResult は .claude/settings.json 評価結果。
//
// Score は 0-100:
//
//	90+ : team-shareable な security policy が整っている
//	70-89: usable, deny/allow に改善余地
//	50-69: permissions が緩い / 破壊的コマンド未ガード
//	<50 : permissions 不在 or 無制限 allow(4 層防御が機能しない)
type SettingsAuditResult struct {
	Score                int      `json:"score"`
	ValidJSON            bool     `json:"valid_json"`
	HasPermissions       bool     `json:"has_permissions"`
	HasDenyList          bool     `json:"has_deny_list"`
	GuardsDestructive    bool     `json:"guards_destructive"`     // deny が rm -rf 等を塞ぐ
	HasUnrestrictedAllow bool     `json:"has_unrestricted_allow"` // Bash(*) 等
	HasHooks             bool     `json:"has_hooks"`
	HasDefaultAgent      bool     `json:"has_default_agent"` // settings.agent(lesser-known)
	Issues               []string `json:"issues,omitempty"`
	Suggestions          []string `json:"suggestions,omitempty"`
}

// settingsDoc は settings.json の audit 対象フィールドのみを抽出する。
type settingsDoc struct {
	Permissions *struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
		Ask   []string `json:"ask"`
	} `json:"permissions"`
	Hooks map[string]json.RawMessage `json:"hooks"`
	Agent string                     `json:"agent"`
}

// reDestructiveGuard は deny エントリが rm -rf 系を塞いでいるか判定する。
var reDestructiveGuard = regexp.MustCompile(`(?i)rm\s+-[a-z]*[rf]`)

// reUnrestrictedBash は `Bash(*)` / `Bash(:*)` / `Bash()` 等の無制限 Bash allow を
// 検出する(inner が空 / コロン / アスタリスク / 空白のみ)。Edit(/docs/**) のような
// scope 付き rule は対象外(記事が推奨する正しい形)。
var reUnrestrictedBash = regexp.MustCompile(`^Bash\s*\(\s*[:*\s]*\)$`)

// AuditSettings は .claude/settings.json の content を heuristic で評価する。
//
// 入力:
//
//	content: settings.json 全テキスト(strict JSON)
//
// 出力:
//
//	SettingsAuditResult。Issues/Suggestions は action-oriented。
func AuditSettings(content string) SettingsAuditResult {
	r := SettingsAuditResult{Score: 100}

	var doc settingsDoc
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		// parse 不能 = Claude Code 自身が設定を読めない(=全 customization 不発)。
		r.ValidJSON = false
		r.Score = 0
		r.Issues = append(r.Issues, "invalid JSON — Claude Code cannot load this settings file at all")
		r.Suggestions = append(r.Suggestions,
			"settings.json must be strict JSON (no comments, no trailing commas). Validate before committing.")
		return r
	}
	r.ValidJSON = true

	r.HasHooks = len(doc.Hooks) > 0
	r.HasDefaultAgent = strings.TrimSpace(doc.Agent) != ""

	// permissions 不在 → check-in できる security policy が無い。
	if doc.Permissions == nil {
		r.Score -= 30
		r.Issues = append(r.Issues, "no 'permissions' block — the 4-layer defense has no allow/deny policy to enforce")
		r.Suggestions = append(r.Suggestions,
			"Add a permissions block and check it in, so the whole team shares one policy (Boris). "+
				"e.g. allow: [\"Bash(go test *)\"], deny: [\"Bash(rm -rf *)\"].")
	} else {
		r.HasPermissions = true

		// deny 不在 / 空 → 破壊的コマンドが素通り。
		r.HasDenyList = len(doc.Permissions.Deny) > 0
		if !r.HasDenyList {
			r.Score -= 20
			r.Issues = append(r.Issues, "empty deny list — destructive commands are not guarded")
			r.Suggestions = append(r.Suggestions,
				"Add deny rules for destructive commands, e.g. Bash(rm -rf *), Bash(git push --force*).")
		} else {
			// deny はあるが rm -rf 系を塞いでいるか。
			for _, d := range doc.Permissions.Deny {
				if reDestructiveGuard.MatchString(d) {
					r.GuardsDestructive = true
					break
				}
			}
			if !r.GuardsDestructive {
				r.Score -= 10
				r.Issues = append(r.Issues, "deny list does not guard rm -rf — the canonical destructive command")
				r.Suggestions = append(r.Suggestions,
					"Add Bash(rm -rf *) (or equivalent) to the deny list.")
			}
		}

		// 無制限 allow(Bash(*) 等)→ static 解析 / sandbox prompt を骨抜きにする。
		for _, a := range doc.Permissions.Allow {
			t := strings.TrimSpace(a)
			if t == "*" || t == "Bash" || reUnrestrictedBash.MatchString(t) {
				r.HasUnrestrictedAllow = true
				break
			}
		}
		if r.HasUnrestrictedAllow {
			r.Score -= 15
			r.Issues = append(r.Issues, "unrestricted Bash allow (e.g. Bash(*)) — this defeats static analysis and sandbox prompts")
			r.Suggestions = append(r.Suggestions,
				"Scope allow rules to concrete commands, e.g. Bash(go test *) / Bash(npm test *), not a blanket Bash(*).")
		}
	}

	// hooks 不在 → 規約を deterministic に強制できていない(security ではないので suggestion のみ)。
	if !r.HasHooks {
		r.Score -= 5
		r.Suggestions = append(r.Suggestions,
			"No hooks configured. Consider a format-on-edit hook (gofmt/prettier) or a Stop-time check "+
				"to enforce conventions deterministically (Boris §Hooks).")
	}

	if r.Score < 0 {
		r.Score = 0
	}
	return r
}
