package harness

import (
	"strings"
	"testing"
)

// hasIssueContaining は Issues のいずれかが substr を含むか。
func hasIssueContaining(issues []string, substr string) bool {
	for _, i := range issues {
		if strings.Contains(i, substr) {
			return true
		}
	}
	return false
}

// pristine な fan-out-and-synthesize workflow(記事 pattern 06 の形)。
// parallel barrier + per-agent model + token budget + 複数 agent。
const pristineWorkflow = `
// fan out one agent per file, then synthesize. use 20k tokens.
const reviews = await parallel(
  files.map(file => () => agent(
    ` + "`Review ${file} for security issues`" + `,
    { model: "haiku", maxTokens: 2000 }
  ))
)
const report = await agent(
  ` + "`Merge these reviews into one prioritized report`" + `,
  { model: "opus" }
)
`

func TestAuditWorkflow_Pristine(t *testing.T) {
	r := AuditWorkflow(pristineWorkflow)
	if !r.IsWorkflow {
		t.Fatal("should be detected as a workflow")
	}
	if r.AgentCalls != 2 {
		t.Errorf("expected 2 agent calls, got %d", r.AgentCalls)
	}
	if !r.UsesParallel {
		t.Error("should detect parallel()")
	}
	if !r.HasTokenBudget {
		t.Error("should detect token budget")
	}
	if !r.HasPerAgentModel {
		t.Error("should detect per-agent model")
	}
	if r.IsTrivial {
		t.Error("multi-agent parallel workflow is not trivial")
	}
	if r.Score < 90 {
		t.Errorf("pristine workflow should score >=90, got %d (issues: %v)", r.Score, r.Issues)
	}
}

func TestAuditWorkflow_NotAWorkflow(t *testing.T) {
	r := AuditWorkflow("const x = 1;\nconsole.log(x);\n")
	if r.IsWorkflow {
		t.Error("plain script is not a workflow")
	}
	if !hasIssueContaining(r.Issues, "does not look like a Dynamic Workflow") {
		t.Errorf("expected not-a-workflow issue, got %v", r.Issues)
	}
}

func TestAuditWorkflow_Trivial(t *testing.T) {
	// 単一 agent・orchestration なし → over-reach（mistake #1）。
	r := AuditWorkflow(`const out = await agent("explain this file", { model: "sonnet", maxTokens: 5000 })`)
	if !r.IsTrivial {
		t.Error("single agent without orchestration should be trivial")
	}
	if !hasIssueContaining(r.Issues, "regular Claude Code session likely suffices") {
		t.Errorf("expected over-reach issue, got %v", r.Issues)
	}
}

func TestAuditWorkflow_NoTokenBudget(t *testing.T) {
	src := `
const a = await agent("do work", { model: "opus" })
const b = await agent("do more", { model: "haiku" })
`
	r := AuditWorkflow(src)
	if r.HasTokenBudget {
		t.Error("should not detect a token budget")
	}
	if !hasIssueContaining(r.Issues, "no explicit token budget") {
		t.Errorf("expected token-budget issue, got %v", r.Issues)
	}
}

func TestAuditWorkflow_SelfVerification(t *testing.T) {
	// verification の語があるのに agent 1 個 → self-preferential bias。
	src := `const out = await agent("write the fix and verify it against the rubric", { model: "opus", maxTokens: 4000 })`
	r := AuditWorkflow(src)
	if r.HasAdversarialVerify {
		t.Error("single-agent self-verification is not adversarial")
	}
	if !hasIssueContaining(r.Issues, "self-preferential bias") {
		t.Errorf("expected self-preference issue, got %v", r.Issues)
	}
}

func TestAuditWorkflow_AdversarialVerifyOK(t *testing.T) {
	// 別 agent が verify → 正しい adversarial verification。
	src := `
const fix = await agent("write the fix", { model: "opus", maxTokens: 4000 })
const ok  = await agent("verify this fix against the rubric", { model: "sonnet", maxTokens: 2000 })
`
	r := AuditWorkflow(src)
	if !r.HasAdversarialVerify {
		t.Error("two-agent verify/work split should count as adversarial verification")
	}
	if hasIssueContaining(r.Issues, "self-preferential bias") {
		t.Errorf("should not flag self-preference with separate verifier: %v", r.Issues)
	}
}

func TestAuditWorkflow_LoopWithoutGoal(t *testing.T) {
	src := `
let done = false
while (!done) {
  const theory = await agent("form and test a theory from the logs", { model: "opus", maxTokens: 3000 })
  done = theory.verified
}
`
	r := AuditWorkflow(src)
	if !r.HasLoop {
		t.Error("should detect while loop")
	}
	if !hasIssueContaining(r.Issues, "loop pattern without /goal") {
		t.Errorf("expected loop-without-goal issue, got %v", r.Issues)
	}
}

func TestAuditWorkflow_LoopWithGoalOK(t *testing.T) {
	src := `
// /goal don't stop until one theory holds
let done = false
while (!done) {
  const theory = await agent("test a theory", { model: "opus", maxTokens: 3000 })
  done = theory.verified
}
`
	r := AuditWorkflow(src)
	if !r.HasGoalOnLoop {
		t.Error("should detect /goal")
	}
	if hasIssueContaining(r.Issues, "loop pattern without /goal") {
		t.Errorf("should not flag loop when /goal present: %v", r.Issues)
	}
}

func TestAuditWorkflow_UntrustedWithoutQuarantine(t *testing.T) {
	src := `
const tickets = loadSupportTickets()
const actions = await parallel(
  tickets.map(t => () => agent(` + "`triage and fix: ${t.body}`" + `, { model: "sonnet", maxTokens: 3000 }))
)
`
	r := AuditWorkflow(src)
	if !r.ReadsUntrusted {
		t.Error("should detect untrusted input (support tickets)")
	}
	if !hasIssueContaining(r.Issues, "quarantine") {
		t.Errorf("expected quarantine issue, got %v", r.Issues)
	}
}

func TestAuditWorkflow_SortByAbsoluteScore(t *testing.T) {
	src := `
const ideas = await parallel(seeds.map(s => () => agent("score this idea", { model: "haiku", maxTokens: 1000 })))
const ranked = ideas.sort((a, b) => b.score - a.score)
`
	r := AuditWorkflow(src)
	if !r.SortsByAbsoluteScore {
		t.Error("should detect absolute-score sort")
	}
	if !hasIssueContaining(r.Issues, "comparative judgment") {
		t.Errorf("expected tournament suggestion, got %v", r.Issues)
	}
}

func TestAuditWorkflow_TournamentNotFlagged(t *testing.T) {
	src := `
const ranked = items.sort(byScore) // tournament bracket already applied pairwise
const w = await agent("pick the bracket winner", { model: "opus", maxTokens: 2000 })
const v = await agent("verify the winner", { model: "sonnet", maxTokens: 2000 })
`
	r := AuditWorkflow(src)
	if r.SortsByAbsoluteScore {
		t.Error("should not flag sort when tournament/pairwise present")
	}
}

// TestAuditWorkflow_NoPerAgentModel covers r.Score -= 5 for workflows that
// have agent() calls but no `model:` property on any agent invocation.
func TestAuditWorkflow_NoPerAgentModel(t *testing.T) {
	// Both agents lack a `model:` property — rePerAgentModel won't match.
	src := `
const a = await agent("do work", { maxTokens: 5000 })
const b = await agent("synthesize", { maxTokens: 3000 })
`
	r := AuditWorkflow(src)
	if r.HasPerAgentModel {
		t.Error("should not detect per-agent model when model: is absent")
	}
	found := false
	for _, s := range r.Suggestions {
		if strings.Contains(s, "per agent") || strings.Contains(s, "per-agent") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected per-agent model suggestion, got %v", r.Suggestions)
	}
}

// 回帰: "preview"(review を部分含有)や "underscore"(score を部分含有)で
// self-preferential bias / absolute-score-sort を誤検出しないこと(word boundary 化)。
func TestAuditWorkflow_NoSubstringFalsePositive(t *testing.T) {
	src := `
// generate a preview; sort items by their under_score timestamp
const out = await agent("build a preview of the page", { model: "sonnet", maxTokens: 4000 })
const ranked = items.sort((a, b) => a.under_score - b.under_score)
`
	r := AuditWorkflow(src)
	if hasIssueContaining(r.Issues, "self-preferential bias") {
		t.Errorf("'preview' must not trigger verification/self-preference: %v", r.Issues)
	}
	if r.SortsByAbsoluteScore || hasIssueContaining(r.Issues, "comparative judgment") {
		t.Errorf("'under_score' must not trigger absolute-score-sort: %v", r.Issues)
	}
}
