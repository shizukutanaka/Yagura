// workflow_audit.go: Dynamic Workflow(~/.claude/workflows/*.js)の heuristic 評価。
//
// Anthropic の Dynamic Workflows(2026-05-28 launch)ガイダンス準拠。workflow は
// Claude がタスク用に書く JavaScript harness で、agent() / parallel() / pipeline()
// で subagent を spawn・協調させる。launch 記事が列挙する "token を浪費する mistakes"
// は、そのまま構造ベースの lint rule になる:
//
//  1. regular session で済む task に workflow を使う(over-reach)
//  2. token budget を設定しない(ambitious workflow は 5–10× に膨らむ)
//  3. 1 つの agent が work と verification を兼ねる(self-preferential bias)
//  4. parallel() と pipeline() を混同(barrier vs streaming)
//  5. loop pattern で /goal を省く(soft completion で早期停止)
//  6. untrusted content を actor に到達させる(quarantine 不在)
//  7. absolute score で sort(comparative/tournament の方が信頼できる)
//
// 全 check は heuristic(LLM 判定は client 側、ここは JS source の構造ベース)。
// AuditSkill / AuditSubagent と同じ shape(Score + Issues + Suggestions)で揃える。
package harness

import (
	"regexp"
)

// WorkflowAuditResult は Dynamic Workflow ファイル評価結果。
//
// Score は 0-100:
//
//	90+ : production ready
//	70-89: usable, 改善余地あり
//	50-69: 複数の anti-pattern、要見直し
//	<50 : workflow として成立していない / 高コスト failure mode 多数
type WorkflowAuditResult struct {
	Score                int      `json:"score"`
	AgentCalls           int      `json:"agent_calls"`                  // agent( の出現数
	UsesParallel         bool     `json:"uses_parallel"`                // parallel() barrier
	UsesPipeline         bool     `json:"uses_pipeline"`                // pipeline() streaming
	HasLoop              bool     `json:"has_loop"`                     // while ループ(loop-until-done)
	HasTokenBudget       bool     `json:"has_token_budget"`             // mistake #2
	HasGoalOnLoop        bool     `json:"has_goal_on_loop"`             // mistake #5(loop 時のみ意味)
	HasAdversarialVerify bool     `json:"has_adversarial_verification"` // mistake #3 の正しい形
	HasPerAgentModel     bool     `json:"has_per_agent_model"`          // per-agent model choice
	ReadsUntrusted       bool     `json:"reads_untrusted"`              // mistake #6 のトリガ
	HasQuarantine        bool     `json:"has_quarantine"`               // mistake #6 の緩和
	SortsByAbsoluteScore bool     `json:"sorts_by_absolute_score"`      // mistake #7
	IsTrivial            bool     `json:"is_trivial"`                   // mistake #1(over-reach)
	IsWorkflow           bool     `json:"is_workflow"`                  // agent() を呼ぶか
	Issues               []string `json:"issues,omitempty"`
	Suggestions          []string `json:"suggestions,omitempty"`
}

var (
	// agent( / parallel( / pipeline( の呼び出し検出(word boundary + 開き括弧)。
	reAgentCall = regexp.MustCompile(`\bagent\s*\(`)
	reParallel  = regexp.MustCompile(`\bparallel\s*\(`)
	rePipeline  = regexp.MustCompile(`\bpipeline\s*\(`)
	// loop-until-done の強いシグナルは while(停止条件付き反復)。for は map/iteration と
	// 紛れるので採らない(false positive 回避)。
	reWhileLoop = regexp.MustCompile(`\bwhile\s*\(`)
	// token budget: 明示識別子 or "10k tokens" / "5000 tokens" 形式。
	reTokenBudget = regexp.MustCompile(`(?i)(max_?tokens|token_?budget|\b\d+k?\s*tokens?\b|use\s+\d+k?\s+tokens)`)
	// per-agent model 指定(model: "opus" 等)。
	rePerAgentModel = regexp.MustCompile(`(?i)\bmodel\s*:`)
	// verification を行っている兆候。term は word boundary で囲み、"preview"/"degrade"
	// のような部分一致 false positive を避ける(review 系は語尾 s/ed/er/ers を許容)。
	reVerification = regexp.MustCompile(`(?i)(\bverif|\badversar|\brefut|\bfact.?check|\bjudge|\bgrade|\bcritique|\breview(s|ed|er|ers)?\b)`)
	// /goal による hard completion。
	reGoal = regexp.MustCompile(`(?i)/goal\b|\bgoal\b`)
	// untrusted / external input source。conservative に絞る。
	reUntrusted = regexp.MustCompile(`(?i)(support\s*ticket|bug\s*report|user\s*feedback|customer\s*feedback|scrape|scraped|untrusted|social\s*media|third.?party|public\s*web|user.?submitted)`)
	// quarantine pattern(read-only reader / low-privilege)。
	reQuarantine = regexp.MustCompile(`(?i)(quarantine|read.?only\s*reader|low.?privilege|no.?privilege)`)
	// .sort( による absolute-score sort。"score" は word boundary 付き(underscore 等を除外)。
	reSortCall  = regexp.MustCompile(`\.sort\s*\(`)
	reScoreWord = regexp.MustCompile(`(?i)\bscore`)
	rePairwise  = regexp.MustCompile(`(?i)(pairwise|tournament|bracket)`)
)

// AuditWorkflow は Dynamic Workflow の JS source を heuristic で評価する。
//
// 入力:
//
//	content: workflow JavaScript ファイル全文
//
// 出力:
//
//	WorkflowAuditResult。Issues/Suggestions は action-oriented。
func AuditWorkflow(content string) WorkflowAuditResult {
	r := WorkflowAuditResult{Score: 100}

	r.AgentCalls = len(reAgentCall.FindAllStringIndex(content, -1))
	r.UsesParallel = reParallel.MatchString(content)
	r.UsesPipeline = rePipeline.MatchString(content)
	r.HasLoop = reWhileLoop.MatchString(content)
	r.IsWorkflow = r.AgentCalls > 0

	// agent() を一度も呼ばない → そもそも workflow ではない(残りの check は意味を成さない)。
	if !r.IsWorkflow {
		r.Score -= 40
		r.Issues = append(r.Issues, "no agent() call found — this does not look like a Dynamic Workflow")
		r.Suggestions = append(r.Suggestions,
			"A workflow spawns subagents via agent(), and coordinates them with parallel()/pipeline(). "+
				"If this file is plain script, it belongs elsewhere, not in the workflows library.")
		if r.Score < 0 {
			r.Score = 0
		}
		return r
	}

	auditWorkflowCost(&r, content)
	auditWorkflowVerification(&r, content)
	auditWorkflowSafety(&r, content)

	if r.Score < 0 {
		r.Score = 0
	}
	return r
}

// auditWorkflowCost は cost 系 anti-pattern を評価する: over-reach(#1)/ token budget
// 不在(#2)/ per-agent model 未選択。
func auditWorkflowCost(r *WorkflowAuditResult, content string) {
	// mistake #1: over-reach。agent 1 個・orchestration なし → regular session で十分。
	r.IsTrivial = r.AgentCalls <= 1 && !r.UsesParallel && !r.UsesPipeline && !r.HasLoop
	if r.IsTrivial {
		r.Score -= 15
		r.Issues = append(r.Issues, "single agent, no parallel/pipeline/loop — a regular Claude Code session likely suffices")
		r.Suggestions = append(r.Suggestions,
			"Workflows earn their cost via isolation/parallelism/verification. "+
				"If one context window finishes this in five minutes, don't wrap it in a workflow (mistake #1).")
	}

	// mistake #2: token budget 不在。
	r.HasTokenBudget = reTokenBudget.MatchString(content)
	if !r.HasTokenBudget {
		r.Score -= 15
		r.Issues = append(r.Issues, "no explicit token budget — ambitious workflows balloon to 5–10× expected cost")
		r.Suggestions = append(r.Suggestions,
			"Set an explicit cap (e.g. a maxTokens option or 'use 10k tokens' in the prompt) so the run is bounded (mistake #2).")
	}

	// per-agent model choice(best practice — Opus/Sonnet/Haiku を agent ごとに選ぶ)。
	r.HasPerAgentModel = rePerAgentModel.MatchString(content)
	if !r.HasPerAgentModel {
		r.Score -= 5
		r.Suggestions = append(r.Suggestions,
			"No per-agent model selection found. Pick the model per agent — Opus for hard reasoning, "+
				"Haiku for cheap exploration, Sonnet for the middle — to control cost.")
	}
}

// auditWorkflowVerification は mistake #3(self-preferential bias)を評価する。
// verification しているのに agent が 1 個だけ = work と verify を同一 Claude が兼ねている。
// 複数 agent があれば separate verifier と推定し adversarial verification の正しい形とみなす。
func auditWorkflowVerification(r *WorkflowAuditResult, content string) {
	if !reVerification.MatchString(content) {
		return
	}
	if r.AgentCalls >= 2 {
		r.HasAdversarialVerify = true
		return
	}
	r.Score -= 20
	r.Issues = append(r.Issues, "verification logic but only one agent — the worker cannot fairly verify itself (self-preferential bias)")
	r.Suggestions = append(r.Suggestions,
		"Spawn a separate verifier agent that sees only the rubric and the artifact, not who produced it (mistake #3 / pattern 07).")
}

// auditWorkflowSafety は completion / safety 系 anti-pattern を評価する: loop の /goal 欠落
// (#5)/ untrusted content の quarantine 不在(#6)/ absolute-score sort(#7)。
func auditWorkflowSafety(r *WorkflowAuditResult, content string) {
	// mistake #5: loop pattern で /goal を欠く。
	r.HasGoalOnLoop = reGoal.MatchString(content)
	if r.HasLoop && !r.HasGoalOnLoop {
		r.Score -= 15
		r.Issues = append(r.Issues, "loop pattern without /goal — the workflow stops at the first soft completion point")
		r.Suggestions = append(r.Suggestions,
			"Pair the loop with /goal to force hard completion (e.g. \"don't stop until one theory works\") (mistake #5).")
	}

	// mistake #6: untrusted content を読むのに quarantine が無い。
	r.ReadsUntrusted = reUntrusted.MatchString(content)
	r.HasQuarantine = reQuarantine.MatchString(content)
	if r.ReadsUntrusted && !r.HasQuarantine {
		r.Score -= 15
		r.Issues = append(r.Issues, "reads untrusted/external content without a quarantine pattern — prompt-injection risk")
		r.Suggestions = append(r.Suggestions,
			"Bar the reader agent from high-privilege actions; let a separate agent (never exposed to the raw content) act (mistake #6 / step 13).")
	}

	// mistake #7: absolute score での sort。pairwise/tournament が無ければ flag。
	if reSortCall.MatchString(content) && reScoreWord.MatchString(content) && !rePairwise.MatchString(content) {
		r.SortsByAbsoluteScore = true
		r.Score -= 10
		r.Issues = append(r.Issues, "sorting by absolute score — comparative judgment (tournament/pairwise) is more reliable")
		r.Suggestions = append(r.Suggestions,
			"For taste-based or large-N ranking, use a tournament (pairwise comparison across fresh agents) instead of sort-by-score (mistake #7 / pattern 09).")
	}
}
