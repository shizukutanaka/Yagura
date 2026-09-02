package harness

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── RecommendForLanguage ───────────────────────────────────

func TestRecommendForLanguage_Go(t *testing.T) {
	r := RecommendForLanguage("go")
	if r.Language != "go" {
		t.Errorf("language: got %q, want go", r.Language)
	}
	if !strings.Contains(r.ClaudeMd, "Zero external dependencies") {
		t.Error("Go CLAUDE.md should mention zero-dep rule")
	}
	if !strings.Contains(r.SettingsJSON, "gofmt") {
		t.Error("Go settings should have gofmt hook")
	}
	if len(r.Skills) == 0 || len(r.Subagents) == 0 {
		t.Error("Go should have skills + subagents")
	}
}

func TestRecommendForLanguage_TypeScript(t *testing.T) {
	r := RecommendForLanguage("typescript")
	if !strings.Contains(r.ClaudeMd, "strict") {
		t.Error("TS should mention strict mode")
	}
	if !strings.Contains(r.SettingsJSON, "tsc") {
		t.Error("TS settings should have tsc hook")
	}
}

func TestRecommendForLanguage_Python(t *testing.T) {
	r := RecommendForLanguage("python")
	if !strings.Contains(r.ClaudeMd, "mypy") {
		t.Error("Python should mention mypy")
	}
}

func TestRecommendForLanguage_Rust(t *testing.T) {
	r := RecommendForLanguage("rust")
	if !strings.Contains(r.SettingsJSON, "clippy") {
		t.Error("Rust should have clippy hook")
	}
}

func TestRecommendForLanguage_CaseInsensitive(t *testing.T) {
	r1 := RecommendForLanguage("Go")
	r2 := RecommendForLanguage("GOLANG")
	if r1.Language != r2.Language {
		t.Error("case should not matter")
	}
}

func TestRecommendForLanguage_Generic(t *testing.T) {
	r := RecommendForLanguage("brainfuck")
	if r.Language != "brainfuck" {
		t.Errorf("generic should preserve lang label, got %q", r.Language)
	}
	// 最低限の structure があれば OK
	if r.ClaudeMd == "" || r.SettingsJSON == "" {
		t.Error("generic should still produce non-empty templates")
	}
}

func TestRecommendForLanguage_Serializable(t *testing.T) {
	// JSON marshal で MCP 経由送出可能なことを確認
	r := RecommendForLanguage("go")
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(b) < 200 {
		t.Errorf("payload too small: %d bytes", len(b))
	}
}

// ─── AuditSkill ─────────────────────────────────────────────

func TestAuditSkill_Pristine(t *testing.T) {
	// 全部正解の skill
	content := `---
name: go-test
description: Use when the user asks to add tests or fix failing tests in Go code.
---

# Go Test Skill

## Patterns

Use t.TempDir for filesystem isolation.

## Gotchas

- ` + "`time.Now()`" + ` in tests must go through NowFn hook
- ` + "`go test ./...`" + ` without -count=1 uses cache

References in references/test-patterns.md.
`
	r := AuditSkill(content)
	if r.Score < 90 {
		t.Errorf("pristine skill: score=%d, want ≥90, issues=%v", r.Score, r.Issues)
	}
	if !r.IsTriggerFormat || !r.HasGotchasSection {
		t.Errorf("flags wrong: %+v", r)
	}
}

func TestAuditSkill_RetireDecision(t *testing.T) {
	// pristine skill は retire 非推奨
	pristine := `---
name: go-test
description: Use when the user asks to add tests or fix failing tests in Go code.
---

# Go Test Skill

## Gotchas

- ` + "`time.Now()`" + ` in tests must go through NowFn hook
`
	if r := AuditSkill(pristine); r.RetireRecommended {
		t.Errorf("pristine skill should not be retire-recommended: %q", r.RetireReason)
	}

	// stub: frontmatter も本文もほぼ無い → retrieval noise、retire 推奨
	stub := "# placeholder\n\nTODO\n"
	rs := AuditSkill(stub)
	if !rs.RetireRecommended || rs.RetireReason == "" {
		t.Errorf("stub skill should be retire-recommended, got %+v", rs)
	}

	// description はあるが本文が薄く trigger 形式でない低品質 → score で retire
	low := `---
name: x
description: A helpful useful general powerful comprehensive advanced thing.
---
`
	if r := AuditSkill(low); !r.RetireRecommended {
		t.Errorf("low-quality skill (score %d) should be retire-recommended", r.Score)
	}
}

func TestAuditSkill_VagueDescription(t *testing.T) {
	content := `---
name: helper
description: A helpful skill for various coding tasks.
---

# Helper

Does things.
`
	r := AuditSkill(content)
	if !r.HasVagueKeywords {
		t.Error("should detect 'helpful' and 'various'")
	}
	if r.IsTriggerFormat {
		t.Error("'A helpful skill' is not trigger format")
	}
	if r.Score >= 70 {
		t.Errorf("vague skill should score low, got %d", r.Score)
	}
}

func TestAuditSkill_NoFrontmatter(t *testing.T) {
	content := `# Just a markdown
no frontmatter at all.`
	r := AuditSkill(content)
	if r.HasFrontmatter {
		t.Error("should detect missing frontmatter")
	}
	if r.Score >= 70 {
		t.Errorf("no-frontmatter skill: score=%d, want <70", r.Score)
	}
}

func TestAuditSkill_LongDescription(t *testing.T) {
	longDesc := "Use when " + strings.Repeat("very long detail ", 100) // ~1700 chars
	content := "---\nname: x\ndescription: " + longDesc + "\n---\n\n## Gotchas\n- thing"
	r := AuditSkill(content)
	if r.DescriptionLen <= 1024 {
		t.Errorf("test setup: desc should be > 1024, got %d", r.DescriptionLen)
	}
	foundLimit := false
	for _, iss := range r.Issues {
		if strings.Contains(iss, "1024") {
			foundLimit = true
		}
	}
	if !foundLimit {
		t.Error("should report 1024 char limit issue")
	}
}

func TestAuditSkill_MissingGotchas(t *testing.T) {
	content := `---
name: x
description: Use when working with X.
---

# X

` + strings.Repeat("Some content here. ", 30)
	r := AuditSkill(content)
	if r.HasGotchasSection {
		t.Error("should detect missing gotchas")
	}
}

// ─── AuditSubagent ──────────────────────────────────────────

func TestAuditSubagent_Pristine(t *testing.T) {
	content := `---
name: code-reviewer
description: Expert Go code reviewer. Use proactively after any commit.
tools: Read, Grep, Glob
model: sonnet
---

You are a senior Go reviewer. Focus on race conditions and error handling.
For each issue, cite line, explain why, suggest fix. Don't modify files.
`
	r := AuditSubagent(content)
	if r.Score < 90 {
		t.Errorf("pristine subagent: score=%d, issues=%v", r.Score, r.Issues)
	}
	if !r.IsSystemPromptStyle {
		t.Error("'You are a senior' should be system-prompt style")
	}
	if !r.HasToolsAllowlist {
		t.Error("'tools: Read, Grep, Glob' present")
	}
	if !r.IsActionOriented {
		t.Error("'Use proactively' should be action-oriented")
	}
}

func TestAuditSubagent_UserPromptStyle(t *testing.T) {
	// #1 misunderstanding: body が user prompt 風
	content := `---
name: reviewer
description: Reviews code.
tools: Read
---

Please review this code carefully and tell me what's wrong with it.
`
	r := AuditSubagent(content)
	if r.IsSystemPromptStyle {
		t.Error("'Please review' is user-prompt style; should fail check")
	}
	foundIssue := false
	for _, iss := range r.Issues {
		if strings.Contains(iss, "system-prompt") {
			foundIssue = true
		}
	}
	if !foundIssue {
		t.Errorf("expected system-prompt issue, got: %v", r.Issues)
	}
}

func TestAuditSubagent_NoToolsRestriction(t *testing.T) {
	content := `---
name: dangerous
description: Use proactively for everything.
---

You are an all-powerful agent with no tool restrictions.
`
	r := AuditSubagent(content)
	if r.HasToolsAllowlist {
		t.Error("should detect missing tools allowlist")
	}
	// Score should be reduced
	if r.Score >= 90 {
		t.Errorf("no-tools subagent: score=%d, expected reduction", r.Score)
	}
}

func TestAuditSubagent_NotActionOriented(t *testing.T) {
	content := `---
name: thing
description: A thing for stuff.
tools: Read
---

You are a thing.
`
	r := AuditSubagent(content)
	if r.IsActionOriented {
		t.Error("'A thing for stuff' not action-oriented")
	}
}

// ─── splitFrontmatterAndBody helper ─────────────────────────

func TestSplitFrontmatter_Basic(t *testing.T) {
	c := "---\nname: test\ndescription: hello\n---\nbody here"
	name, desc, body := splitFrontmatterAndBody(c)
	if name != "test" || desc != "hello" || !strings.Contains(body, "body here") {
		t.Errorf("parse failed: name=%q desc=%q body=%q", name, desc, body)
	}
}

func TestSplitFrontmatter_NoFrontmatter(t *testing.T) {
	c := "# Just markdown\nno frontmatter"
	name, desc, _ := splitFrontmatterAndBody(c)
	if name != "" || desc != "" {
		t.Error("should return empties when no frontmatter")
	}
}

func TestSplitFrontmatter_QuotedValues(t *testing.T) {
	c := `---
name: "quoted-name"
description: 'single quoted'
---
body`
	name, desc, _ := splitFrontmatterAndBody(c)
	if name != "quoted-name" || desc != "single quoted" {
		t.Errorf("quote stripping failed: name=%q desc=%q", name, desc)
	}
}

// ─── jsRecommendation ────────────────────────────────────────

func TestRecommendForLanguage_JavaScript(t *testing.T) {
	for _, lang := range []string{"javascript", "js", "JS", "JavaScript"} {
		r := RecommendForLanguage(lang)
		if r.Language != "javascript" {
			t.Errorf("RecommendForLanguage(%q).Language = %q, want javascript", lang, r.Language)
		}
		if r.ClaudeMd == "" || r.SettingsJSON == "" {
			t.Errorf("js recommendation missing templates for lang %q", lang)
		}
	}
}

// ─── sectionPurpose ──────────────────────────────────────────

func TestSectionPurpose_AllCases(t *testing.T) {
	cases := []struct {
		sec  string
		want string
	}{
		{"Why", "background"},
		{"Map", "map of key"},
		{"Rules", "cannot be inferred"},
		{"Workflows", "every time"},
		{"Unknown", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := sectionPurpose(tc.sec)
		if tc.want == "" {
			if got != "" {
				t.Errorf("sectionPurpose(%q) = %q, want empty", tc.sec, got)
			}
		} else {
			if !strings.Contains(got, tc.want) {
				t.Errorf("sectionPurpose(%q) = %q, want contains %q", tc.sec, got, tc.want)
			}
		}
	}
}

// ─── extractFrontmatterRaw ───────────────────────────────────

func TestExtractFrontmatterRaw_NoClosingDelimiter(t *testing.T) {
	// starts with --- but has no closing \n---
	content := "---\nname: test\ndescription: hello\n"
	if got := extractFrontmatterRaw(content); got != "" {
		t.Errorf("no closing --- should return empty, got %q", got)
	}
}

func TestExtractFrontmatterRaw_NoPrefix(t *testing.T) {
	content := "# just markdown\nno frontmatter"
	if got := extractFrontmatterRaw(content); got != "" {
		t.Errorf("no --- prefix should return empty, got %q", got)
	}
}

func TestExtractFrontmatterRaw_Success(t *testing.T) {
	content := "---\nname: test\ntools: Read\n---\nbody"
	got := extractFrontmatterRaw(content)
	if !strings.Contains(got, "name: test") {
		t.Errorf("expected frontmatter content, got %q", got)
	}
	if strings.Contains(got, "body") {
		t.Errorf("body should not be in frontmatter raw, got %q", got)
	}
}

// ─── AuditSkill additional branch coverage ───────────────────

func TestAuditSkill_LongName(t *testing.T) {
	// name > 64 chars → issue mentioning "64 char limit"
	longName := strings.Repeat("a", 65)
	content := "---\nname: " + longName + "\ndescription: Use when the user asks to do X.\n---\n\n## Gotchas\n- note\n"
	r := AuditSkill(content)
	if r.NameLen <= 64 {
		t.Fatalf("test setup: NameLen = %d, want > 64", r.NameLen)
	}
	found := false
	for _, iss := range r.Issues {
		if strings.Contains(iss, "64") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 64-char-limit issue, got %v", r.Issues)
	}
}

func TestAuditSkill_VeryLongBody_ProgressiveDisclosureHint(t *testing.T) {
	// body > 1500 words + no progressive disclosure → suggestion about splitting
	bigBody := strings.Repeat("word ", 1600) // 1600 words
	content := "---\nname: x\ndescription: Use when the user asks to do X.\n---\n\n## Gotchas\n- tip\n\n" + bigBody
	r := AuditSkill(content)
	if r.BodyWordCount <= 1500 {
		t.Fatalf("test setup: body word count %d, want > 1500", r.BodyWordCount)
	}
	if r.HasProgDisclosure {
		t.Skip("body accidentally triggered progressive disclosure detection")
	}
	foundSuggestion := false
	for _, s := range r.Suggestions {
		if strings.Contains(s, "references") || strings.Contains(s, "split") || strings.Contains(s, "progressive") {
			foundSuggestion = true
		}
	}
	if !foundSuggestion {
		t.Errorf("expected progressive disclosure suggestion for large body, got %v", r.Suggestions)
	}
}

func TestAuditSkill_ExcessiveBody_BodyLimitIssue(t *testing.T) {
	// body > 3000 words → issue mentioning "3000 words"
	bigBody := strings.Repeat("content ", 3001) // 3001 words
	content := "---\nname: x\ndescription: Use when the user asks to do X.\n---\n\n## Gotchas\n- tip\n\n" + bigBody
	r := AuditSkill(content)
	if r.BodyWordCount <= 3000 {
		t.Fatalf("test setup: body word count %d, want > 3000", r.BodyWordCount)
	}
	found := false
	for _, iss := range r.Issues {
		if strings.Contains(iss, "3000") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected body-limit issue mentioning 3000, got %v", r.Issues)
	}
}

// ─── splitFrontmatterAndBody unclosed-frontmatter branch ─────

// TestSplitFrontmatter_NoClosingDelimiter covers the `end < 0` path in
// splitFrontmatterAndBody: input starts with "---" but never has a
// closing "\n---", so the function returns ("", "", content).
func TestSplitFrontmatter_NoClosingDelimiter(t *testing.T) {
	content := "---\nname: test\ndescription: hello\n" // no closing \n---
	name, desc, body := splitFrontmatterAndBody(content)
	if name != "" || desc != "" {
		t.Errorf("unclosed frontmatter: want empty name/desc, got name=%q desc=%q", name, desc)
	}
	if body != content {
		t.Errorf("unclosed frontmatter: body should be original content, got %q", body)
	}
}

// ─── AuditSubagent missing-frontmatter and missing-desc branches ──

// TestAuditSubagent_NoFrontmatter covers the r.Score -= 30 ("frontmatter missing")
// and r.Score -= 20 ("missing description") deductions when content has no
// frontmatter at all.
func TestAuditSubagent_NoFrontmatter(t *testing.T) {
	content := "Please review this carefully and list all bugs. It should be a thorough audit."
	r := AuditSubagent(content)
	if r.HasFrontmatter {
		t.Error("plain text should have no frontmatter")
	}
	if r.DescriptionLen != 0 {
		t.Errorf("no frontmatter → no description, got DescriptionLen=%d", r.DescriptionLen)
	}
	// Both -30 (no frontmatter) and -20 (no desc) should have fired.
	found30, found20 := false, false
	for _, iss := range r.Issues {
		if strings.Contains(iss, "frontmatter") {
			found30 = true
		}
		if strings.Contains(iss, "description") {
			found20 = true
		}
	}
	if !found30 {
		t.Errorf("expected 'frontmatter missing' issue, got %v", r.Issues)
	}
	if !found20 {
		t.Errorf("expected 'missing description' issue, got %v", r.Issues)
	}
}

// ─── AuditSkill score-clamp branch ───────────────────────────

// TestAuditSkill_ScoreClamped covers the `r.Score = 0` clamp at the end of
// AuditSkill. Achieved by: long name (−10) + non-trigger description full of all
// 10 vague keywords (−20 trigger + 10×−8 keywords) = −110 deduction → raw score
// −10, clamped to 0.
func TestAuditSkill_ScoreClamped(t *testing.T) {
	longName := strings.Repeat("a", 65)
	// All 10 vague keywords in one description, no trigger phrase.
	desc := "A helpful useful various general smart easy powerful comprehensive advanced professional skill."
	content := "---\nname: " + longName + "\ndescription: " + desc + "\n---\n"
	r := AuditSkill(content)
	if r.Score != 0 {
		t.Errorf("massively-penalised skill should be clamped to score 0, got %d (issues %v)", r.Score, r.Issues)
	}
}

// ─── AuditClaudeMd wall-of-text branch ───────────────────────

// TestAuditClaudeMd_WallOfText covers the `len(headings) < 2 && InstructionCount > 40`
// branch ("many instructions but almost no section structure").
func TestAuditClaudeMd_WallOfText(t *testing.T) {
	// Build a CLAUDE.md with 50 list items but only one H2 heading.
	var sb strings.Builder
	sb.WriteString("# MyProject\n\n## Rules\n")
	for i := 0; i < 50; i++ {
		sb.WriteString("- rule\n")
	}
	r := AuditClaudeMd(sb.String())
	if r.InstructionCount <= 40 {
		t.Fatalf("test setup: InstructionCount = %d, want > 40", r.InstructionCount)
	}
	found := false
	for _, iss := range r.Issues {
		if strings.Contains(iss, "many instructions") || strings.Contains(iss, "section structure") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected wall-of-text issue, got %v", r.Issues)
	}
}

// ─── AuditSkill: missing-name-in-frontmatter and Gotchas branches ─

// TestAuditSkill_MissingNameInFrontmatter covers `r.NameLen == 0 && r.HasFrontmatter`
// → r.Score -= 10 for "missing 'name' field in frontmatter".
// The frontmatter has a description (so HasFrontmatter = true) but no name field.
func TestAuditSkill_MissingNameInFrontmatter(t *testing.T) {
	content := "---\ndescription: Use when the user asks to add tests.\n---\n\n## Gotchas\n- note\n"
	r := AuditSkill(content)
	if !r.HasFrontmatter {
		t.Fatal("description-only frontmatter should be detected")
	}
	if r.NameLen != 0 {
		t.Fatalf("test setup: NameLen = %d, want 0", r.NameLen)
	}
	found := false
	for _, iss := range r.Issues {
		if strings.Contains(iss, "missing 'name' field") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing-name-field issue, got %v", r.Issues)
	}
}

// TestAuditSkill_NoGotchasWithLongBody covers the Gotchas check:
// body > 100 words but no ## Gotchas section → r.Score -= 15.
func TestAuditSkill_NoGotchasWithLongBody(t *testing.T) {
	// 35 × "Some content here. " = 35 × 3 words = 105 words > 100
	bigBody := strings.Repeat("Some content here. ", 35)
	content := "---\nname: x\ndescription: Use when the user asks to work with X.\n---\n\n# X\n\n" + bigBody
	r := AuditSkill(content)
	if r.BodyWordCount <= 100 {
		t.Fatalf("test setup: BodyWordCount = %d, want > 100", r.BodyWordCount)
	}
	if r.HasGotchasSection {
		t.Fatal("test setup: body should not have a Gotchas section")
	}
	found := false
	for _, iss := range r.Issues {
		if strings.Contains(iss, "Gotchas") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Gotchas issue for long body without Gotchas section, got %v", r.Issues)
	}
}
