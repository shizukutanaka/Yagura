// claudemd_audit.go: CLAUDE.md の構造 heuristic 評価。
//
// Anthropic の Claude Code memory ガイド + 運用知見の構造ルールを lint にする:
//
//   - 4 セクション構造(Why / Map / Rules / Workflows)を持つ。Why=背景と禁止理由、
//     Map=重要ファイル地図、Rules=コードから推論不可能なルール、Workflows=常用コマンド。
//   - 命令(箇条書き / 番号付き)の総数は 150-200 が上限。超えると重要ルールが
//     ノイズに埋もれる(Lost in the Middle)。150-200 は警告、200 超は finding。
//   - H1 タイトルを持つ。
//   - 大量の命令を持つのに見出し構造が無い「壁」状態を避ける。
//
// AuditSkill / AuditSettings / AuditWorkflow と同じ shape(Score + Issues +
// Suggestions)。stdlib のみ(ADR-0001)。出力は決定論的(セクションは canonical 順)。
package harness

import (
	"regexp"
	"sort"
	"strings"
)

// ClaudeMdAuditResult は CLAUDE.md 構造評価結果。
//
// Score は 0-100:
//
//	90+ : 4 セクション + 適切な命令量。新 Claude が迷わない。
//	70-89: 構造はあるが欠けたセクション or 命令過多の傾向。
//	<70 : 構造欠如 or 命令過多で重要ルールが埋もれる。
type ClaudeMdAuditResult struct {
	Score            int      `json:"score"`
	HasTitle         bool     `json:"has_title"`
	SectionsFound    []string `json:"sections_found,omitempty"`
	MissingSections  []string `json:"missing_sections,omitempty"`
	InstructionCount int      `json:"instruction_count"`
	Issues           []string `json:"issues,omitempty"`
	Suggestions      []string `json:"suggestions,omitempty"`
}

// canonicalSections は CLAUDE.md が持つべき 4 セクション(canonical 順)。
var canonicalSections = []string{"Why", "Map", "Rules", "Workflows"}

// 命令数の上限・警告閾値(Lost in the Middle 対策)。
const (
	instructionHardLimit = 200
	instructionWarnLimit = 150
)

var (
	reH1          = regexp.MustCompile(`(?m)^#\s+\S`)
	reH2          = regexp.MustCompile(`(?m)^##\s+(.+?)\s*$`)
	reListItem    = regexp.MustCompile(`(?m)^\s*(?:[-*]|\d+\.)\s+\S`)
	reSectionWord = map[string]*regexp.Regexp{
		"Why":       regexp.MustCompile(`(?i)\bwhy\b`),
		"Map":       regexp.MustCompile(`(?i)\bmap\b`),
		"Rules":     regexp.MustCompile(`(?i)\brules?\b`),
		"Workflows": regexp.MustCompile(`(?i)\bworkflows?\b`),
	}
)

// AuditClaudeMd は CLAUDE.md の content を構造 heuristic で評価する。
func AuditClaudeMd(content string) ClaudeMdAuditResult {
	if strings.TrimSpace(content) == "" {
		return emptyClaudeMdResult()
	}

	r := ClaudeMdAuditResult{Score: 100}
	checkTitle(content, &r)
	headingCount := checkSections(content, &r)
	checkInstructionCount(content, &r)
	checkStructureWall(headingCount, &r)

	if r.Score < 0 {
		r.Score = 0
	}
	sort.Strings(r.MissingSections)
	return r
}

// emptyClaudeMdResult は空 CLAUDE.md の固定結果(構造チェック対象なし)。
func emptyClaudeMdResult() ClaudeMdAuditResult {
	return ClaudeMdAuditResult{
		Score:  0,
		Issues: []string{"empty CLAUDE.md — Claude Code has no project-level guidance to load"},
		Suggestions: []string{
			"add Why / Map / Rules / Workflows sections (see the Claude Code memory guide)",
		},
	}
}

// checkTitle は H1 タイトルの有無を評価する。
func checkTitle(content string, r *ClaudeMdAuditResult) {
	r.HasTitle = reH1.MatchString(content)
	if !r.HasTitle {
		r.Score -= 10
		r.Issues = append(r.Issues, "no H1 title — start with `# <ProjectName> CLAUDE.md`")
	}
}

// checkSections は H2 見出しを収集して canonical セクションと照合する。
// 見出し総数(壁チェック用)を返す。
func checkSections(content string, r *ClaudeMdAuditResult) int {
	matches := reH2.FindAllStringSubmatch(content, -1)
	headings := make([]string, 0, len(matches))
	for _, m := range matches {
		headings = append(headings, m[1])
	}
	foundSet := map[string]bool{}
	for _, sec := range canonicalSections {
		re := reSectionWord[sec]
		for _, h := range headings {
			if re.MatchString(h) {
				foundSet[sec] = true
				break
			}
		}
	}
	for _, sec := range canonicalSections {
		if foundSet[sec] {
			r.SectionsFound = append(r.SectionsFound, sec)
		} else {
			r.MissingSections = append(r.MissingSections, sec)
			r.Score -= 10
			r.Issues = append(r.Issues, "missing the '"+sec+"' section — "+sectionPurpose(sec))
		}
	}
	return len(headings)
}

// checkInstructionCount は命令(箇条書き / 番号付き)数を Lost-in-the-Middle
// しきい値と比較する。
func checkInstructionCount(content string, r *ClaudeMdAuditResult) {
	r.InstructionCount = len(reListItem.FindAllString(content, -1))
	switch {
	case r.InstructionCount > instructionHardLimit:
		r.Score -= 20
		r.Issues = append(r.Issues,
			"over 200 instructions — split into .claude/rules/ or Skills; excess buries key rules (Lost in the Middle)")
	case r.InstructionCount >= instructionWarnLimit:
		r.Score -= 8
		r.Suggestions = append(r.Suggestions,
			"approaching the 150-200 instruction limit — keep the most important rules at the top and bottom")
	}
}

// checkStructureWall は大量命令なのに見出し構造が無い「壁」状態を検出する。
func checkStructureWall(headingCount int, r *ClaudeMdAuditResult) {
	if headingCount < 2 && r.InstructionCount > 40 {
		r.Score -= 10
		r.Issues = append(r.Issues,
			"many instructions but almost no section structure — group them under headings")
	}
}

// sectionPurpose は各 canonical セクションの役割(remediation 用)。
func sectionPurpose(sec string) string {
	switch sec {
	case "Why":
		return "background, constraints, and the reason behind prohibitions"
	case "Map":
		return "a map of key directories/files so a new Claude does not get lost"
	case "Rules":
		return "rules that cannot be inferred from code (quantified)"
	case "Workflows":
		return "the commands you run every time (test, build, migrate)"
	}
	return ""
}
