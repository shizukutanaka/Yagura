// Package recovery は agentic orchestration の「self-healing(自己修復)」判断を
// rule-based / deterministic に行う層。
//
// 着想は arXiv 2606.01416 "Self-Healing Agentic Orchestrators" — orchestration を
// monitor → detect → root-cause classify → budgeted recovery-policy 選択 →
// post-recovery verify → observe に分解し、failure class を recovery action
// (retry / argument repair / tool substitution / replan / graceful degradation /
// escalation 等)へ budget 付きでマップする。
//
// Yagura は LLM を呼ばず agent を実行もしない(control plane / not brain)。本層は
// 「失敗が報告されたとき、次にどの recovery action を取るべきか」を決定論的に返す
// 判断エンジンで、実際の retry/replan は MCP client が行う。budget(最大試行回数)で
// 無限ループを防ぎ、credential/permission 系は決して自動 retry せず human へ escalate
// する(Human-in-the-Loop / trust base)。parallel_plan(dispatch)と組で、Yagura を
// fleet の reliability control plane にする。
package recovery

import (
	"fmt"
	"strings"
)

// FailureClass は失敗の根本原因カテゴリ(arXiv 2601.16280 の tool-failure taxonomy 準拠)。
type FailureClass string

const (
	// ClassTimeout は一時的・タイムアウト失敗。
	ClassTimeout FailureClass = "timeout"
	// ClassRateLimit は 429 等・backoff 対象の失敗。
	ClassRateLimit FailureClass = "rate_limit"
	// ClassToolInit は tool 初期化/接続失敗。
	ClassToolInit FailureClass = "tool_init"
	// ClassBadArgs は引数/パラメータ不正。
	ClassBadArgs FailureClass = "bad_args"
	// ClassToolError は tool 実行エラー。
	ClassToolError FailureClass = "tool_error"
	// ClassAuth は認証/権限失敗 → 自動 retry 禁止。
	ClassAuth FailureClass = "auth"
	// ClassQuota は agent quota 枯渇 → 別 agent へ。
	ClassQuota FailureClass = "quota"
	// ClassContextOverflow は context 溢れ → 圧縮/replan。
	ClassContextOverflow FailureClass = "context_overflow"
	// ClassWrongResult は検証失敗(verifier が reject)。
	ClassWrongResult FailureClass = "wrong_result"
	// ClassUnknown は分類不能な失敗。
	ClassUnknown FailureClass = "unknown"
)

// Action は次に取る recovery 操作。
type Action string

const (
	// ActionRetry はそのまま即時再試行。
	ActionRetry Action = "retry"
	// ActionBackoffRetry は待機(backoff)後に再試行。
	ActionBackoffRetry Action = "backoff_retry"
	// ActionRepairArgs は引数を修正して再試行。
	ActionRepairArgs Action = "repair_args"
	// ActionSubstituteTool は代替 tool に切り替える。
	ActionSubstituteTool Action = "substitute_tool"
	// ActionSubstituteAgent は代替 agent に切り替える。
	ActionSubstituteAgent Action = "substitute_agent"
	// ActionRefreshContext は context を圧縮 / retrieval refresh する。
	ActionRefreshContext Action = "refresh_context"
	// ActionReplan は計画を立て直す。
	ActionReplan Action = "replan"
	// ActionDegrade は graceful degradation(低 severity)。
	ActionDegrade Action = "degrade"
	// ActionEscalate は Human-in-the-Loop へエスカレートする。
	ActionEscalate Action = "escalate"
)

// Event は 1 つの失敗報告。Yagura はこれだけで決定論的に判断する(stateless)。
type Event struct {
	Class       FailureClass `json:"class"`
	Attempt     int          `json:"attempt"`      // この task の試行回数(1-based)
	MaxAttempts int          `json:"max_attempts"` // budget。<=0 で既定 3
	Agent       string       `json:"agent"`        // 現在の agent(substitute 判断用)
	Severity    string       `json:"severity"`     // "low" なら budget 切れ時に degrade
}

// Budget は試行予算の状態。
type Budget struct {
	AttemptsUsed int `json:"attempts_used"`
	MaxAttempts  int `json:"max_attempts"`
	Remaining    int `json:"remaining"`
}

// Decision は recovery 判断の結果。
type Decision struct {
	Action            Action `json:"action"`
	Reason            string `json:"reason"`
	Terminal          bool   `json:"terminal"`                      // これ以上の自動 recovery が無い
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"` // backoff 用
	Budget            Budget `json:"budget"`
}

const defaultMaxAttempts = 3

// Decide は failure event から次の recovery action を決定論的に選ぶ。
func Decide(e Event) Decision {
	maxAtt := e.MaxAttempts
	if maxAtt <= 0 {
		maxAtt = defaultMaxAttempts
	}
	attempt := e.Attempt
	if attempt < 1 {
		attempt = 1
	}
	if attempt > maxAtt {
		attempt = maxAtt
	}
	budget := Budget{AttemptsUsed: attempt, MaxAttempts: maxAtt, Remaining: maxAtt - attempt}
	exhausted := attempt >= maxAtt
	low := strings.EqualFold(strings.TrimSpace(e.Severity), "low")

	d := Decision{Budget: budget}
	// budget 切れ時の終端: 低 severity は degrade、それ以外は escalate。
	exhaust := func(reason string) Decision {
		if low {
			d.Action, d.Terminal, d.Reason = ActionDegrade, true,
				"recovery budget exhausted on a low-severity task — accept the degraded result instead of paging a human"
			return d
		}
		d.Action, d.Terminal, d.Reason = ActionEscalate, true, reason
		return d
	}

	switch normClass(e.Class) {
	case ClassAuth:
		// 認証/権限は決して自動 retry しない(security / trust base)。
		d.Action, d.Terminal, d.Reason = ActionEscalate, true,
			"auth/permission failure — never auto-retried; a human must check credentials/scope"
		return d
	case ClassQuota:
		if exhausted {
			return exhaust("agent quota exhausted and no recovery budget left — escalate to re-plan capacity")
		}
		d.Action, d.Reason = ActionSubstituteAgent,
			fmt.Sprintf("agent %q is out of quota — route this task to another capable agent", strings.TrimSpace(e.Agent))
		return d
	case ClassBadArgs:
		if exhausted {
			return exhaust("argument repair did not converge within budget")
		}
		d.Action, d.Reason = ActionRepairArgs, "parameter/argument error — repair the call arguments, then retry"
		return d
	case ClassToolInit:
		// exhausted を先に判定する: budget が 1 だと attempt 1 で既に使い切っており、
		// optimistic な backoff retry を返すと budget を超えてループが回り続ける。
		if exhausted {
			return exhaust("tool keeps failing to initialize within budget")
		}
		if attempt == 1 {
			d.Action, d.Reason, d.RetryAfterSeconds = ActionBackoffRetry, "tool init failure on first attempt — may be transient; retry with backoff", backoff(attempt)
			return d
		}
		d.Action, d.Reason = ActionSubstituteTool, "tool repeatedly fails to initialize — substitute an equivalent tool"
		return d
	case ClassToolError:
		if exhausted {
			return exhaust("tool keeps erroring within budget")
		}
		if attempt == 1 {
			d.Action, d.Reason = ActionRetry, "tool execution error — retry"
			return d
		}
		d.Action, d.Reason = ActionSubstituteTool, "tool keeps erroring — substitute an equivalent tool"
		return d
	case ClassTimeout, ClassRateLimit:
		if exhausted {
			return exhaust("transient failures did not clear within budget")
		}
		d.Action, d.Reason, d.RetryAfterSeconds = ActionBackoffRetry, "transient failure (timeout/rate-limit) — retry with exponential backoff", backoff(attempt)
		return d
	case ClassContextOverflow:
		if exhausted {
			return exhaust("context still overflows after compaction/replan budget")
		}
		d.Action, d.Reason = ActionRefreshContext, "context window overflow — compact/refresh context (or split the task) and retry"
		return d
	case ClassWrongResult:
		if exhausted {
			return exhaust("output keeps failing verification within budget")
		}
		d.Action, d.Reason = ActionReplan, "verifier rejected the output — replan the approach (do not just retry the same plan)"
		return d
	default: // unknown
		// exhausted を先に判定する(budget 1 で attempt 1 の retry がループを溢れさせる)。
		if exhausted {
			return exhaust("unknown failure persisted through the recovery budget")
		}
		if attempt == 1 {
			d.Action, d.Reason = ActionRetry, "unknown failure on first attempt — retry once before replanning"
			return d
		}
		d.Action, d.Reason = ActionReplan, "unknown failure persists — replan the approach"
		return d
	}
}

// normClass は別名/表記ゆれを正規化する(tool 入力に寛容に)。
func normClass(c FailureClass) FailureClass {
	switch strings.ToLower(strings.TrimSpace(string(c))) {
	case "timeout", "deadline", "deadline_exceeded":
		return ClassTimeout
	case "rate_limit", "ratelimit", "429", "throttled", "throttle":
		return ClassRateLimit
	case "tool_init", "tool-init", "init", "connect", "connection", "unavailable":
		return ClassToolInit
	case "bad_args", "bad-args", "invalid_args", "invalid_argument", "params", "parameter", "schema":
		return ClassBadArgs
	case "tool_error", "tool-error", "execution", "exec":
		return ClassToolError
	case "auth", "authentication", "authorization", "permission", "forbidden", "401", "403", "credential":
		return ClassAuth
	case "quota", "exhausted", "out_of_quota", "no_capacity":
		return ClassQuota
	case "context_overflow", "context", "overflow", "token_limit", "context_length":
		return ClassContextOverflow
	case "wrong_result", "verification_failed", "verify_failed", "rejected", "incorrect":
		return ClassWrongResult
	}
	return ClassUnknown
}

// backoff は attempt(1-based)に対する指数 backoff 秒(2,4,8,... 上限 60)。
func backoff(attempt int) int {
	s := 1
	for i := 0; i < attempt; i++ {
		s *= 2
		if s >= 60 {
			return 60
		}
	}
	return s
}
