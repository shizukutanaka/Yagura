// audit.go: SKILL.md / .claude/agents/*.md の heuristic 評価
//
// Thariq(Anthropic)のガイダンス準拠:
//   - description は trigger condition であって summary ではない
//   - Gotchas section が highest-signal content
//   - 一般知識を再述しない、デフォルト挙動の divergence を書く
//   - 1500-2000 word body 目安(progressive disclosure)
//   - subagent body は SYSTEM PROMPT(NOT user prompt) ← #1 misunderstanding
//
// 全 check は heuristic(LLM 判定は client 側、ここは構造ベース)。
package harness

import (
	"fmt"
	"regexp"
	"strings"
)

// SkillAuditResult は SKILL.md 評価結果。
//
// Score は 0-100。実用判定:
//   90+ : production ready
//   70-89: usable, 改善余地あり
//   50-69: 多くの問題、要 refactor
//   <50 : ほぼ動かない(trigger 不発火 / Claude が無視)
type SkillAuditResult struct {
	Score              int      `json:"score"`              // 0-100
	HasFrontmatter     bool     `json:"has_frontmatter"`
	DescriptionLen     int      `json:"description_len"`    // 1024 制限への距離測定
	NameLen            int      `json:"name_len"`           // 64 制限への距離測定
	BodyWordCount      int      `json:"body_word_count"`    // 1500-2000 目安
	IsTriggerFormat    bool     `json:"is_trigger_format"`  // "Use when ..." 形式
	HasGotchasSection  bool     `json:"has_gotchas_section"`
	HasVagueKeywords   bool     `json:"has_vague_keywords"`
	HasProgDisclosure  bool     `json:"has_progressive_disclosure"` // references/ scripts/ への参照
	// v0.35: skill lifecycle(MUSE-Autoskill 由来)。低品質/スタブ skill を
	// ライブラリに残すと retrieval noise を増やすだけなので、rule-based に
	// 「retire 候補」を明示する(自動削除はせず人間判断に委ねる)。
	RetireRecommended  bool     `json:"retire_recommended"`
	RetireReason       string   `json:"retire_reason,omitempty"`
	Issues             []string `json:"issues,omitempty"`
	Suggestions        []string `json:"suggestions,omitempty"`
}

// skillRetireThreshold は「これ未満なら rewrite より retire を推奨」する score。
// 既存バンド(<50 = ほぼ動かない)より厳しめに取り、明確なケースだけ flag する。
const skillRetireThreshold = 40

// vague description で頻出する単語(filler — 何の trigger にも match しない)
var vagueKeywords = []string{
	"helpful", "useful", "various", "general", "smart",
	"easy", "powerful", "comprehensive", "advanced", "professional",
}

// AuditSkill は SKILL.md の content 全体を heuristic で評価する。
//
// 入力:
//   content: SKILL.md 全テキスト(frontmatter + body)
//
// 出力:
//   SkillAuditResult。Issues/Suggestions は action-oriented。
func AuditSkill(content string) SkillAuditResult {
	r := SkillAuditResult{Score: 100}

	// frontmatter parse(YAML 厳密 parse は不要、key=value 抽出のみ)
	name, desc, body := splitFrontmatterAndBody(content)
	r.HasFrontmatter = name != "" || desc != ""

	if !r.HasFrontmatter {
		r.Score -= 30
		r.Issues = append(r.Issues, "frontmatter not found (need '---' separators)")
		r.Suggestions = append(r.Suggestions,
			"Add YAML frontmatter:\n  ---\n  name: skill-name\n  description: Use when ...\n  ---")
	}

	auditSkillName(&r, name)
	auditSkillDescription(&r, desc)
	auditSkillBody(&r, body)

	if r.Score < 0 {
		r.Score = 0
	}
	r.RetireRecommended, r.RetireReason = skillRetireDecision(r)
	return r
}

// auditSkillName は name フィールドの存在と 64 char 制限を評価する。
func auditSkillName(r *SkillAuditResult, name string) {
	r.NameLen = len(name)
	if r.NameLen == 0 && r.HasFrontmatter {
		r.Score -= 10
		r.Issues = append(r.Issues, "missing 'name' field in frontmatter")
	}
	if r.NameLen > 64 {
		r.Score -= 10
		r.Issues = append(r.Issues,
			"name exceeds 64 char limit (Anthropic spec)")
	}
}

// auditSkillDescription は description(= primary routing key)を評価する:
// 存在・1024 制限・trigger 形式・vague keyword の累積減点。
func auditSkillDescription(r *SkillAuditResult, desc string) {
	r.DescriptionLen = len(desc)
	if r.DescriptionLen == 0 {
		r.Score -= 25
		r.Issues = append(r.Issues, "missing 'description' field (= primary routing key)")
		r.Suggestions = append(r.Suggestions,
			"description is the *only* thing Claude sees at startup. Without it, your skill never fires.")
		return
	}
	if r.DescriptionLen > 1024 {
		r.Score -= 10
		r.Issues = append(r.Issues,
			"description exceeds 1024 char limit (Anthropic spec)")
		r.Suggestions = append(r.Suggestions,
			"Split skill into narrower scopes, or move detail to body")
	}
	// trigger 形式判定: "Use when" / "Triggers on" / "Use immediately" など
	descLower := strings.ToLower(desc)
	triggerPhrases := []string{
		"use when", "use after", "use immediately",
		"use proactively", "triggers on", "fires when",
		"should be used when", "should be invoked when",
	}
	for _, p := range triggerPhrases {
		if strings.Contains(descLower, p) {
			r.IsTriggerFormat = true
			break
		}
	}
	if !r.IsTriggerFormat {
		r.Score -= 20
		r.Issues = append(r.Issues,
			"description not in trigger format (per Thariq, Anthropic)")
		r.Suggestions = append(r.Suggestions,
			"Rewrite as 'Use when the user asks to ...' or 'Use proactively after ...'. "+
				"The description is a routing condition, not a summary.")
	}
	// vague keywords 検出(累積減点 — 1 description に複数あれば各 -8)
	for _, kw := range vagueKeywords {
		if strings.Contains(descLower, kw) {
			if !r.HasVagueKeywords {
				r.HasVagueKeywords = true
				r.Issues = append(r.Issues,
					"description contains vague keyword(s): see suggestion")
				r.Suggestions = append(r.Suggestions,
					"Rewrite without filler words like 'helpful', 'useful', 'various', 'general'. "+
						"Name the concrete trigger condition.")
			}
			r.Score -= 8
		}
	}
}

// auditSkillBody は body の word count / Gotchas section / progressive disclosure を評価する。
func auditSkillBody(r *SkillAuditResult, body string) {
	r.BodyWordCount = countWords(body)
	if r.BodyWordCount > 3000 {
		r.Score -= 10
		r.Issues = append(r.Issues,
			"body exceeds 3000 words (Anthropic target: 1500-2000)")
		r.Suggestions = append(r.Suggestions,
			"Move detailed content to references/*.md, link from SKILL.md (progressive disclosure)")
	}

	// Gotchas section 検出
	bodyLower := strings.ToLower(body)
	gotchasMatcher := regexp.MustCompile(`(?m)^#+\s*gotchas`)
	r.HasGotchasSection = gotchasMatcher.MatchString(bodyLower)
	if !r.HasGotchasSection && r.BodyWordCount > 100 {
		r.Score -= 15
		r.Issues = append(r.Issues,
			"no '## Gotchas' section (highest-signal per Thariq)")
		r.Suggestions = append(r.Suggestions,
			"Add a Gotchas section listing where Claude's defaults diverge from this codebase. "+
				"This is the place to accumulate institutional memory.")
	}

	// progressive disclosure 検出(references/ or scripts/ への参照)
	r.HasProgDisclosure = strings.Contains(body, "references/") ||
		strings.Contains(body, "scripts/") ||
		strings.Contains(body, "assets/")
	if !r.HasProgDisclosure && r.BodyWordCount > 1500 {
		r.Score -= 5
		r.Suggestions = append(r.Suggestions,
			"Consider splitting large content into references/*.md or scripts/*.sh "+
				"and linking from SKILL.md(progressive disclosure)")
	}
}

// skillRetireDecision は MUSE-Autoskill の "Skill Retiree" 相当を rule-based で
// 判定する。低価値/obsolete な skill を残すと、ライブラリ拡大に伴い retrieval
// noise(Claude が無関係 skill を引く確率)を増やすだけなので retire を促す。
func skillRetireDecision(r SkillAuditResult) (bool, string) {
	// stub: routing 用の description が無い(=決して発火しない)かつ本文も空同然。
	// score は frontmatter 欠落の減点止まりで高く出ることがあるため別判定する。
	if r.DescriptionLen == 0 && r.BodyWordCount < 30 {
		return true, "stub skill: no routing description and near-empty body — pure retrieval noise; retire or rewrite from scratch"
	}
	if r.Score < skillRetireThreshold {
		return true, fmt.Sprintf(
			"low quality (score %d < %d): misfires or gets ignored at runtime, so it only adds retrieval noise; retire or rewrite",
			r.Score, skillRetireThreshold)
	}
	return false, ""
}

// SubagentAuditResult は subagent 定義(.claude/agents/*.md)評価結果。
type SubagentAuditResult struct {
	Score             int      `json:"score"`
	HasFrontmatter    bool     `json:"has_frontmatter"`
	NameLen           int      `json:"name_len"`
	DescriptionLen    int      `json:"description_len"`
	BodyWordCount     int      `json:"body_word_count"`
	IsActionOriented  bool     `json:"is_action_oriented"`   // description が "Use proactively..." 等
	HasToolsAllowlist bool     `json:"has_tools_allowlist"`  // tools 明示(omit はリスク)
	HasSafetyMode     bool     `json:"has_safety_mode"`      // permissionMode 明示
	IsSystemPromptStyle bool   `json:"is_system_prompt_style"` // "You are X" 形式(主語)
	Issues            []string `json:"issues,omitempty"`
	Suggestions       []string `json:"suggestions,omitempty"`
}

// AuditSubagent は .claude/agents/*.md content を評価する。
//
// #1 misunderstanding: body は SYSTEM PROMPT。ここを user-prompt 風に書くと
// subagent が混乱する。冒頭 "You are X" / "Your role is ..." を確認。
func AuditSubagent(content string) SubagentAuditResult {
	r := SubagentAuditResult{Score: 100}
	name, desc, body := splitFrontmatterAndBody(content)
	r.HasFrontmatter = name != "" || desc != ""

	if !r.HasFrontmatter {
		r.Score -= 30
		r.Issues = append(r.Issues, "frontmatter missing")
		r.Suggestions = append(r.Suggestions,
			"Add ---\\nname: agent-name\\ndescription: ...\\n---")
	}

	r.NameLen = len(name)
	auditSubagentDescription(&r, desc)
	auditSubagentTools(&r, content)
	auditSubagentBody(&r, body)

	if r.Score < 0 {
		r.Score = 0
	}
	return r
}

// auditSubagentDescription は description(= auto-delegation key)が action-oriented かを評価する。
func auditSubagentDescription(r *SubagentAuditResult, desc string) {
	r.DescriptionLen = len(desc)
	if r.DescriptionLen == 0 {
		r.Score -= 20
		r.Issues = append(r.Issues, "missing description (= auto-delegation key)")
		return
	}
	// action-oriented description: "Use proactively" / "Reviews" / "Specializes in"
	descLower := strings.ToLower(desc)
	actions := []string{
		"use proactively", "use immediately", "use after",
		"reviews", "specializes in", "expert", "handles",
	}
	for _, a := range actions {
		if strings.Contains(descLower, a) {
			r.IsActionOriented = true
			break
		}
	}
	if !r.IsActionOriented {
		r.Score -= 15
		r.Issues = append(r.Issues,
			"description not action-oriented")
		r.Suggestions = append(r.Suggestions,
			"Start with verb: 'Reviews code...', 'Use proactively after ...'")
	}
}

// auditSubagentTools は frontmatter の tools allowlist / permissionMode を評価する。
func auditSubagentTools(r *SubagentAuditResult, content string) {
	fmRaw := extractFrontmatterRaw(content)
	r.HasToolsAllowlist = strings.Contains(fmRaw, "tools:") ||
		strings.Contains(fmRaw, "allowedTools:")
	if !r.HasToolsAllowlist {
		r.Score -= 15
		r.Issues = append(r.Issues,
			"no 'tools' allowlist (subagent inherits ALL tools incl. MCP — security risk)")
		r.Suggestions = append(r.Suggestions,
			"Explicitly list tools: 'tools: Read, Grep, Glob' for read-only reviewer")
	}
	r.HasSafetyMode = strings.Contains(fmRaw, "permissionMode:") ||
		strings.Contains(fmRaw, "disallowedTools:")
}

// auditSubagentBody は body が system-prompt style("You are X" 等)かを評価する。
// #1 misunderstanding: body は SYSTEM PROMPT であって user prompt ではない。
func auditSubagentBody(r *SubagentAuditResult, body string) {
	r.BodyWordCount = countWords(body)
	bodyLower := strings.ToLower(strings.TrimSpace(body))
	systemPromptOpeners := []string{
		"you are", "your role", "your task", "you act as",
		"act as", "you specialize",
	}
	for _, op := range systemPromptOpeners {
		if strings.HasPrefix(bodyLower, op) {
			r.IsSystemPromptStyle = true
			break
		}
	}
	if !r.IsSystemPromptStyle && r.BodyWordCount > 10 {
		r.Score -= 20
		r.Issues = append(r.Issues,
			"body not in system-prompt style — this is the #1 subagent misunderstanding")
		r.Suggestions = append(r.Suggestions,
			"Body is the SYSTEM PROMPT, not a user prompt. "+
				"Start with 'You are a senior X' / 'Your role is to Y'. "+
				"See https://code.claude.com/docs/en/sub-agents")
	}
}

// ─── Internal helpers ───────────────────────────────────────

// splitFrontmatterAndBody は markdown content を frontmatter と body に分割し、
// name / description を抽出する。簡易 YAML parser(行ベース key: value 抽出)。
func splitFrontmatterAndBody(content string) (name, desc, body string) {
	if !strings.HasPrefix(content, "---") {
		return "", "", content
	}
	rest := strings.TrimPrefix(content, "---")
	rest = strings.TrimLeft(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", "", content
	}
	fm := rest[:end]
	body = rest[end+4:]
	body = strings.TrimLeft(body, "\n")

	// 行ベース key: value 抽出(複雑な YAML 構造は対象外)
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			name = strings.Trim(name, `"'`)
		} else if strings.HasPrefix(line, "description:") {
			desc = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			desc = strings.Trim(desc, `"'`)
		}
	}
	return
}

// extractFrontmatterRaw は frontmatter の raw text を返す(tool 検出用)。
func extractFrontmatterRaw(content string) string {
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	rest := strings.TrimPrefix(content, "---")
	rest = strings.TrimLeft(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// countWords は markdown body の概算 word count(コードブロック含む)。
func countWords(s string) int {
	return len(strings.Fields(s))
}
