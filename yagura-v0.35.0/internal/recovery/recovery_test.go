package recovery

import "testing"

func TestDecide_AuthNeverAutoRetries(t *testing.T) {
	d := Decide(Event{Class: ClassAuth, Attempt: 1})
	if d.Action != ActionEscalate || !d.Terminal {
		t.Errorf("auth must escalate terminally, got %+v", d)
	}
}

func TestDecide_TransientBackoffThenEscalate(t *testing.T) {
	first := Decide(Event{Class: ClassTimeout, Attempt: 1, MaxAttempts: 3})
	if first.Action != ActionBackoffRetry || first.RetryAfterSeconds <= 0 {
		t.Errorf("first timeout should backoff_retry with a delay, got %+v", first)
	}
	// backoff grows.
	second := Decide(Event{Class: ClassRateLimit, Attempt: 2, MaxAttempts: 3})
	if second.RetryAfterSeconds <= first.RetryAfterSeconds {
		t.Errorf("backoff should grow: a1=%d a2=%d", first.RetryAfterSeconds, second.RetryAfterSeconds)
	}
	// budget exhausted → escalate.
	last := Decide(Event{Class: ClassTimeout, Attempt: 3, MaxAttempts: 3})
	if last.Action != ActionEscalate || !last.Terminal {
		t.Errorf("exhausted transient should escalate, got %+v", last)
	}
}

func TestDecide_BudgetExhaustedLowSeverityDegrades(t *testing.T) {
	d := Decide(Event{Class: ClassTimeout, Attempt: 3, MaxAttempts: 3, Severity: "low"})
	if d.Action != ActionDegrade || !d.Terminal {
		t.Errorf("low-severity exhausted should degrade, got %+v", d)
	}
}

func TestDecide_QuotaSubstitutesAgent(t *testing.T) {
	d := Decide(Event{Class: ClassQuota, Attempt: 1, MaxAttempts: 3, Agent: "claude_code"})
	if d.Action != ActionSubstituteAgent {
		t.Errorf("quota should substitute agent, got %+v", d)
	}
	// when budget is gone, even quota escalates.
	if e := Decide(Event{Class: ClassQuota, Attempt: 3, MaxAttempts: 3}); e.Action != ActionEscalate {
		t.Errorf("exhausted quota should escalate, got %+v", e)
	}
}

func TestDecide_BadArgsRepairs(t *testing.T) {
	if d := Decide(Event{Class: ClassBadArgs, Attempt: 1}); d.Action != ActionRepairArgs {
		t.Errorf("bad_args should repair_args, got %+v", d)
	}
}

func TestDecide_ToolInitRetryThenSubstitute(t *testing.T) {
	if d := Decide(Event{Class: ClassToolInit, Attempt: 1, MaxAttempts: 4}); d.Action != ActionBackoffRetry {
		t.Errorf("tool_init attempt 1 should backoff_retry, got %+v", d)
	}
	if d := Decide(Event{Class: ClassToolInit, Attempt: 2, MaxAttempts: 4}); d.Action != ActionSubstituteTool {
		t.Errorf("tool_init attempt 2 should substitute_tool, got %+v", d)
	}
}

func TestDecide_WrongResultReplans(t *testing.T) {
	d := Decide(Event{Class: ClassWrongResult, Attempt: 1, MaxAttempts: 3})
	if d.Action != ActionReplan {
		t.Errorf("verification failure should replan (not blind retry), got %+v", d)
	}
}

func TestDecide_ContextOverflowRefreshes(t *testing.T) {
	if d := Decide(Event{Class: ClassContextOverflow, Attempt: 1, MaxAttempts: 3}); d.Action != ActionRefreshContext {
		t.Errorf("context overflow should refresh_context, got %+v", d)
	}
}

func TestDecide_UnknownRetryThenReplan(t *testing.T) {
	if d := Decide(Event{Class: ClassUnknown, Attempt: 1, MaxAttempts: 4}); d.Action != ActionRetry {
		t.Errorf("unknown attempt 1 should retry, got %+v", d)
	}
	if d := Decide(Event{Class: ClassUnknown, Attempt: 2, MaxAttempts: 4}); d.Action != ActionReplan {
		t.Errorf("unknown attempt 2 should replan, got %+v", d)
	}
}

func TestDecide_BudgetAndAliasesAndDeterminism(t *testing.T) {
	// default budget = 3 when unset.
	d := Decide(Event{Class: ClassTimeout, Attempt: 1})
	if d.Budget.MaxAttempts != 3 || d.Budget.Remaining != 2 {
		t.Errorf("default budget should be 3 (remaining 2), got %+v", d.Budget)
	}
	// alias normalization: "429" → rate_limit, "403" → auth.
	if Decide(Event{Class: "429", Attempt: 1}).Action != ActionBackoffRetry {
		t.Error("'429' should normalize to rate_limit")
	}
	if Decide(Event{Class: "403", Attempt: 1}).Action != ActionEscalate {
		t.Error("'403' should normalize to auth")
	}
	// deterministic.
	e := Event{Class: ClassToolError, Attempt: 2, MaxAttempts: 5, Agent: "x"}
	if Decide(e) != Decide(e) {
		t.Error("Decide must be deterministic")
	}
}

// ─── convergence: a budget of 1 must never recommend a non-terminal retry ──
//
// Loop Engineering: a loop without a real termination condition runs forever.
// With MaxAttempts==1 the budget is exhausted on the only attempt, so every
// failure class must terminate (escalate, or degrade when low severity) rather
// than tell the orchestrator to retry past its budget.

func TestDecide_ToolInitBudgetOfOne_Terminates(t *testing.T) {
	d := Decide(Event{Class: ClassToolInit, Attempt: 1, MaxAttempts: 1})
	if !d.Terminal {
		t.Errorf("tool_init with budget 1 must terminate, got non-terminal %+v", d)
	}
	if d.Action == ActionBackoffRetry || d.Action == ActionRetry {
		t.Errorf("budget-1 must not recommend a retry, got %s", d.Action)
	}
}

func TestDecide_UnknownBudgetOfOne_Terminates(t *testing.T) {
	d := Decide(Event{Class: "totally_unknown_class", Attempt: 1, MaxAttempts: 1})
	if !d.Terminal {
		t.Errorf("unknown failure with budget 1 must terminate, got %+v", d)
	}
	if d.Action == ActionBackoffRetry || d.Action == ActionRetry {
		t.Errorf("budget-1 must not recommend a retry, got %s", d.Action)
	}
}

func TestDecide_BudgetOfOne_LowSeverityDegrades(t *testing.T) {
	d := Decide(Event{Class: ClassToolInit, Attempt: 1, MaxAttempts: 1, Severity: "low"})
	if d.Action != ActionDegrade || !d.Terminal {
		t.Errorf("budget-1 low-severity should degrade terminally, got %+v", d)
	}
}

// Regression: the normal multi-attempt path is unchanged — a tool_init failure
// on attempt 1 of a 3-budget still does an optimistic backoff retry.
func TestDecide_ToolInitFirstAttemptStillBacksOff(t *testing.T) {
	d := Decide(Event{Class: ClassToolInit, Attempt: 1, MaxAttempts: 3})
	if d.Action != ActionBackoffRetry || d.Terminal {
		t.Errorf("tool_init attempt 1 of 3 should backoff_retry non-terminally, got %+v", d)
	}
}

// ─── attempt clamping ────────────────────────────────────────

func TestDecide_AttemptBelowOne_ClampsToOne(t *testing.T) {
	d := Decide(Event{Class: ClassTimeout, Attempt: 0, MaxAttempts: 3})
	if d.Terminal {
		t.Errorf("attempt 0 clamped to 1 should not be terminal: %+v", d)
	}
}

func TestDecide_AttemptAboveMax_ClampsToMax(t *testing.T) {
	d := Decide(Event{Class: ClassTimeout, Attempt: 99, MaxAttempts: 3})
	if !d.Terminal {
		t.Errorf("attempt 99 with maxAttempts 3 should be terminal (clamped to max): %+v", d)
	}
}

// ─── exhaustion for additional failure classes ────────────────

func TestDecide_BadArgs_Exhausted(t *testing.T) {
	d := Decide(Event{Class: ClassBadArgs, Attempt: 3, MaxAttempts: 3})
	if !d.Terminal {
		t.Errorf("ClassBadArgs exhausted should be terminal: %+v", d)
	}
}

func TestDecide_ToolError_Exhausted(t *testing.T) {
	d := Decide(Event{Class: ClassToolError, Attempt: 3, MaxAttempts: 3})
	if !d.Terminal {
		t.Errorf("ClassToolError exhausted should be terminal: %+v", d)
	}
}

func TestDecide_ContextOverflow_Exhausted(t *testing.T) {
	d := Decide(Event{Class: ClassContextOverflow, Attempt: 3, MaxAttempts: 3})
	if !d.Terminal {
		t.Errorf("ClassContextOverflow exhausted should be terminal: %+v", d)
	}
}

func TestDecide_WrongResult_Exhausted(t *testing.T) {
	d := Decide(Event{Class: ClassWrongResult, Attempt: 3, MaxAttempts: 3})
	if !d.Terminal {
		t.Errorf("ClassWrongResult exhausted should be terminal: %+v", d)
	}
}

// ─── backoff ceiling ──────────────────────────────────────────

func TestBackoff_CappedAt60(t *testing.T) {
	v := backoff(20)
	if v > 60 {
		t.Errorf("backoff(20) = %d, want ≤ 60", v)
	}
}
