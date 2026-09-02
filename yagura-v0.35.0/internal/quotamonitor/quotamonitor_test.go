package quotamonitor

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ─── New / Report ────────────────────────────────────────────

func TestNew_BothAgentsActive(t *testing.T) {
	m := New()
	for _, a := range []Agent{AgentClaudeCode, AgentWindsurf} {
		s, err := m.Status(a)
		if err != nil {
			t.Fatal(err)
		}
		if s.State != StateActive {
			t.Errorf("%s: expected ACTIVE, got %s", a, s.State)
		}
		if s.RemainingPercent != 100 {
			t.Errorf("%s: expected 100%%, got %d", a, s.RemainingPercent)
		}
	}
}

func TestReport_TransitionsState(t *testing.T) {
	m := New()
	tests := []struct {
		remaining int
		wantState State
	}{
		{100, StateActive},
		{50, StateActive},
		{20, StateActive}, // boundary at WarnThreshold=20
		{19, StateWarn},
		{1, StateWarn},
		{0, StateExhausted},
	}
	for _, tc := range tests {
		_ = m.Report(AgentClaudeCode, tc.remaining, "manual", time.Time{}, time.Time{})
		s, _ := m.Status(AgentClaudeCode)
		if s.State != tc.wantState {
			t.Errorf("remaining=%d: got %s, want %s", tc.remaining, s.State, tc.wantState)
		}
	}
}

func TestReport_429SourceForcesExhausted(t *testing.T) {
	m := New()
	// remaining > 0 でも source=429 なら EXHAUSTED に
	_ = m.Report(AgentClaudeCode, 50, "429", time.Time{}, time.Time{})
	s, _ := m.Status(AgentClaudeCode)
	if s.State != StateExhausted {
		t.Errorf("429 should force EXHAUSTED, got %s", s.State)
	}
}

func TestReport_InvalidAgent(t *testing.T) {
	m := New()
	err := m.Report(Agent("invalid"), 50, "manual", time.Time{}, time.Time{})
	if err == nil {
		t.Error("expected error for invalid agent")
	}
}

func TestReport_InvalidPercent(t *testing.T) {
	m := New()
	if err := m.Report(AgentClaudeCode, -1, "manual", time.Time{}, time.Time{}); err == nil {
		t.Error("expected error for negative percent")
	}
	if err := m.Report(AgentClaudeCode, 101, "manual", time.Time{}, time.Time{}); err == nil {
		t.Error("expected error for percent > 100")
	}
}

func TestReport_ResetTimesPersisted(t *testing.T) {
	m := New()
	reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	_ = m.Report(AgentClaudeCode, 10, "auto", reset, time.Time{})
	s, _ := m.Status(AgentClaudeCode)
	if !s.WindowResetsAt.Equal(reset) {
		t.Errorf("WindowResetsAt: got %v, want %v", s.WindowResetsAt, reset)
	}
}

// ─── MarkSwitched / MarkResumed ──────────────────────────────

func TestMarkSwitched(t *testing.T) {
	m := New()
	_ = m.MarkSwitched(AgentClaudeCode)
	s, _ := m.Status(AgentClaudeCode)
	if s.State != StateSwitched {
		t.Errorf("expected SWITCHED, got %s", s.State)
	}
	if s.HandoffAt.IsZero() {
		t.Error("HandoffAt should be set")
	}
	// SWITCHED 状態では Report で state が上書きされない
	_ = m.Report(AgentClaudeCode, 100, "manual", time.Time{}, time.Time{})
	s2, _ := m.Status(AgentClaudeCode)
	if s2.State != StateSwitched {
		t.Errorf("SWITCHED should persist through Report, got %s", s2.State)
	}
}

func TestMarkResumed(t *testing.T) {
	m := New()
	_ = m.MarkSwitched(AgentClaudeCode)
	_ = m.MarkResumed(AgentClaudeCode, 80)
	s, _ := m.Status(AgentClaudeCode)
	if s.State != StateActive {
		t.Errorf("expected ACTIVE after resume, got %s", s.State)
	}
	if s.RemainingPercent != 80 {
		t.Errorf("expected 80%%, got %d", s.RemainingPercent)
	}
}

// ─── Recommend ───────────────────────────────────────────────

func TestRecommend_BothActive_PicksHigher(t *testing.T) {
	m := New()
	_ = m.Report(AgentClaudeCode, 60, "auto", time.Time{}, time.Time{})
	_ = m.Report(AgentWindsurf, 90, "auto", time.Time{}, time.Time{})
	agent, _ := m.Recommend()
	if agent != AgentWindsurf {
		t.Errorf("expected windsurf (more remaining), got %s", agent)
	}
}

func TestRecommend_ClaudeExhausted_PicksWindsurf(t *testing.T) {
	m := New()
	_ = m.Report(AgentClaudeCode, 0, "429", time.Time{}, time.Time{})
	agent, reason := m.Recommend()
	if agent != AgentWindsurf {
		t.Errorf("expected windsurf, got %s (reason: %s)", agent, reason)
	}
}

func TestRecommend_BothExhausted_ReturnsEmpty(t *testing.T) {
	m := New()
	_ = m.Report(AgentClaudeCode, 0, "429", time.Time{}, time.Time{})
	_ = m.Report(AgentWindsurf, 0, "429", time.Time{}, time.Time{})
	agent, reason := m.Recommend()
	if agent != "" {
		t.Errorf("expected empty agent, got %s", agent)
	}
	if reason == "" {
		t.Error("reason should be set")
	}
}

// ─── ShouldHandoff ───────────────────────────────────────────

func TestShouldHandoff_Exhausted(t *testing.T) {
	m := New()
	_ = m.Report(AgentClaudeCode, 0, "429", time.Time{}, time.Time{})
	should, target, reason := m.ShouldHandoff(AgentClaudeCode)
	if !should {
		t.Errorf("should handoff: got false, reason: %s", reason)
	}
	if target != AgentWindsurf {
		t.Errorf("target: got %s, want windsurf", target)
	}
}

func TestShouldHandoff_WarnWithBetterAlternative(t *testing.T) {
	m := New()
	_ = m.Report(AgentClaudeCode, 10, "auto", time.Time{}, time.Time{}) // WARN
	_ = m.Report(AgentWindsurf, 90, "auto", time.Time{}, time.Time{})   // ACTIVE
	should, target, _ := m.ShouldHandoff(AgentClaudeCode)
	if !should || target != AgentWindsurf {
		t.Errorf("should=%v, target=%s", should, target)
	}
}

func TestShouldHandoff_WarnButOtherAlsoWarn(t *testing.T) {
	m := New()
	_ = m.Report(AgentClaudeCode, 10, "auto", time.Time{}, time.Time{})
	_ = m.Report(AgentWindsurf, 15, "auto", time.Time{}, time.Time{})
	should, _, _ := m.ShouldHandoff(AgentClaudeCode)
	if should {
		t.Error("should not handoff when both in WARN (other is not ACTIVE)")
	}
}

func TestShouldHandoff_ActiveNoChange(t *testing.T) {
	m := New()
	should, _, _ := m.ShouldHandoff(AgentClaudeCode)
	if should {
		t.Error("should not handoff when current is ACTIVE")
	}
}

func TestShouldHandoff_BothExhausted(t *testing.T) {
	m := New()
	_ = m.Report(AgentClaudeCode, 0, "429", time.Time{}, time.Time{})
	_ = m.Report(AgentWindsurf, 0, "429", time.Time{}, time.Time{})
	should, target, _ := m.ShouldHandoff(AgentClaudeCode)
	if should {
		t.Error("should not handoff when both exhausted")
	}
	if target != "" {
		t.Errorf("target should be empty, got %s", target)
	}
}

// ─── AgentFromString ─────────────────────────────────────────

func TestAgentFromString(t *testing.T) {
	cases := []struct {
		in    string
		want  Agent
		isErr bool
	}{
		{"claude_code", AgentClaudeCode, false},
		{"claude-code", AgentClaudeCode, false},
		{"Claude", AgentClaudeCode, false},
		{"  CC  ", AgentClaudeCode, false},
		{"windsurf", AgentWindsurf, false},
		{"cascade", AgentWindsurf, false},
		{"WS", AgentWindsurf, false},
		{"chatgpt", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := AgentFromString(tc.in)
		if (err != nil) != tc.isErr {
			t.Errorf("AgentFromString(%q): err=%v, isErr=%v", tc.in, err, tc.isErr)
		}
		if got != tc.want {
			t.Errorf("AgentFromString(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// ─── Concurrency ─────────────────────────────────────────────

func TestConcurrent_ReportAndStatus(t *testing.T) {
	m := New()
	done := make(chan bool, 20)
	for i := 0; i < 10; i++ {
		go func(p int) {
			for j := 0; j < 100; j++ {
				_ = m.Report(AgentClaudeCode, (p*7)%101, "auto", time.Time{}, time.Time{})
			}
			done <- true
		}(i)
		go func() {
			for j := 0; j < 100; j++ {
				_, _ = m.Status(AgentClaudeCode)
				_, _ = m.Recommend()
			}
			done <- true
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
}

// ─── AllStatuses ─────────────────────────────────────────────

func TestAllStatuses_ReturnsBoth(t *testing.T) {
	m := New()
	all := m.AllStatuses()
	if len(all) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(all))
	}
	if _, ok := all[AgentClaudeCode]; !ok {
		t.Error("missing claude_code")
	}
	if _, ok := all[AgentWindsurf]; !ok {
		t.Error("missing windsurf")
	}
}

// ─── Heartbeat protocol (v0.14.0) ─────────────────────────────

func TestRecordHeartbeat_SetsTimestamp(t *testing.T) {
	m := New()
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return fixed }

	if err := m.RecordHeartbeat(AgentClaudeCode); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Status(AgentClaudeCode)
	if !s.LastHeartbeatAt.Equal(fixed) {
		t.Errorf("LastHeartbeatAt: got %v, want %v", s.LastHeartbeatAt, fixed)
	}
}

func TestRecordHeartbeat_InvalidAgent(t *testing.T) {
	m := New()
	if err := m.RecordHeartbeat(Agent("invalid")); err == nil {
		t.Error("expected error for invalid agent")
	}
}

func TestIsStale_NoHeartbeatEverYet(t *testing.T) {
	m := New()
	// 一度も heartbeat 無し、State=ACTIVE → stale 扱い
	stale, elapsed := m.IsStale(AgentClaudeCode, 1*time.Minute)
	if !stale {
		t.Error("expected stale=true when no heartbeat ever")
	}
	if elapsed != 0 {
		t.Errorf("elapsed should be 0 for never-heartbeated, got %v", elapsed)
	}
}

func TestIsStale_SwitchedAgentNotStale(t *testing.T) {
	m := New()
	_ = m.MarkSwitched(AgentClaudeCode)
	// SWITCHED 状態は controlled silence → stale 扱いしない
	stale, _ := m.IsStale(AgentClaudeCode, 1*time.Nanosecond)
	if stale {
		t.Error("SWITCHED agent should not be marked stale")
	}
}

func TestIsStale_WithinTimeout(t *testing.T) {
	m := New()
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return fixed }
	_ = m.RecordHeartbeat(AgentClaudeCode)
	// 5 秒後 query、IdleTimeout=1 分
	m.NowFn = func() time.Time { return fixed.Add(5 * time.Second) }
	stale, elapsed := m.IsStale(AgentClaudeCode, 1*time.Minute)
	if stale {
		t.Error("within timeout, should not be stale")
	}
	if elapsed != 5*time.Second {
		t.Errorf("elapsed: got %v, want 5s", elapsed)
	}
}

func TestIsStale_BeyondTimeout(t *testing.T) {
	m := New()
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return fixed }
	_ = m.RecordHeartbeat(AgentClaudeCode)
	// 15 分後 query、IdleTimeout=10 分
	m.NowFn = func() time.Time { return fixed.Add(15 * time.Minute) }
	stale, elapsed := m.IsStale(AgentClaudeCode, 10*time.Minute)
	if !stale {
		t.Error("beyond timeout, should be stale")
	}
	if elapsed != 15*time.Minute {
		t.Errorf("elapsed: got %v, want 15m", elapsed)
	}
}

func TestIsStale_InvalidAgent(t *testing.T) {
	m := New()
	stale, _ := m.IsStale(Agent("invalid"), time.Minute)
	if stale {
		t.Error("invalid agent: stale should be false (no panic)")
	}
}

func TestIsStale_ZeroTimeoutUsesDefault(t *testing.T) {
	m := New()
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return fixed }
	_ = m.RecordHeartbeat(AgentClaudeCode)
	m.NowFn = func() time.Time { return fixed.Add(5 * time.Minute) }
	// idleTimeout=0 → default 10 min が適用される → 5min < 10min → not stale
	stale, _ := m.IsStale(AgentClaudeCode, 0)
	if stale {
		t.Error("zero idleTimeout should use default (10m); 5m < 10m should not be stale")
	}
}

func TestAnyStale_DetectsBoth(t *testing.T) {
	m := New()
	// 両 agent 一度も heartbeat 無し → 両方 stale
	stale := m.AnyStale(time.Minute)
	if len(stale) != 2 {
		t.Errorf("expected 2 stale, got %d: %v", len(stale), stale)
	}
}

func TestAnyStale_OnlyOne(t *testing.T) {
	m := New()
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return fixed }
	_ = m.RecordHeartbeat(AgentClaudeCode)
	m.NowFn = func() time.Time { return fixed.Add(1 * time.Second) }
	stale := m.AnyStale(1 * time.Minute)
	if len(stale) != 1 || stale[0] != AgentWindsurf {
		t.Errorf("expected only windsurf stale, got %v", stale)
	}
}

// ─── v0.15.0: Heartbeat-aware Recommend ──────────────────────

func TestRecommend_StaleClaudeCode_PrefersWindsurf(t *testing.T) {
	m := New()
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return fixed }
	// 両 agent quota ACTIVE で 100%
	_ = m.RecordHeartbeat(AgentClaudeCode)
	_ = m.RecordHeartbeat(AgentWindsurf)
	// 15 分後: Claude Code は heartbeat 古い、Windsurf は更新
	m.NowFn = func() time.Time { return fixed.Add(15 * time.Minute) }
	_ = m.RecordHeartbeat(AgentWindsurf) // Windsurf のみ heartbeat 更新
	// Recommend は stale な Claude Code を避ける
	agent, reason := m.Recommend()
	if agent != AgentWindsurf {
		t.Errorf("expected windsurf (claude_code stale), got %s. reason: %s", agent, reason)
	}
}

func TestRecommend_BothStale_ButOneRecovers(t *testing.T) {
	m := New()
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return fixed }
	_ = m.RecordHeartbeat(AgentClaudeCode)
	// 15 分後
	m.NowFn = func() time.Time { return fixed.Add(15 * time.Minute) }
	// CC は stale、Windsurf は no-heartbeat-ever (grace period 扱い)
	agent, reason := m.Recommend()
	// Windsurf は no-heartbeat なので usable と判定される
	if agent != AgentWindsurf {
		t.Errorf("expected windsurf (grace period for never-heartbeated), got %s. reason: %s", agent, reason)
	}
}

func TestRecommend_BothStale_ReturnsEmpty(t *testing.T) {
	m := New()
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return fixed }
	_ = m.RecordHeartbeat(AgentClaudeCode)
	_ = m.RecordHeartbeat(AgentWindsurf)
	// 15 分後、両方 stale
	m.NowFn = func() time.Time { return fixed.Add(15 * time.Minute) }
	agent, reason := m.Recommend()
	if agent != "" {
		t.Errorf("expected empty (both stale), got %s. reason: %s", agent, reason)
	}
	if !strings.Contains(reason, "stale") {
		t.Errorf("reason should mention stale, got: %s", reason)
	}
}

func TestRecommend_NeverHeartbeatedNotStale(t *testing.T) {
	m := New()
	// 起動直後: 一度も heartbeat なし。state は ACTIVE。
	// grace period として usable 扱いされるべき。
	agent, _ := m.Recommend()
	if agent == "" {
		t.Error("never-heartbeated agents should be usable in grace period")
	}
}

// ─── v0.15.0: Background Watch ──────────────────────────────

func TestWatch_EmitsOnStaleTransition(t *testing.T) {
	m := New()
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	// atomic 時刻で race-free に時刻を進める
	var currentTime atomic.Pointer[time.Time]
	currentTime.Store(&fixed)
	m.NowFn = func() time.Time { return *currentTime.Load() }

	_ = m.RecordHeartbeat(AgentClaudeCode)
	_ = m.RecordHeartbeat(AgentWindsurf)

	events := make(chan StaleEvent, 4)
	emit := func(e StaleEvent) { events <- e }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go m.Watch(ctx, 10*time.Millisecond, 100*time.Millisecond, emit)

	// 50ms 後に時刻を 200ms 進めて両 agent stale 化
	time.Sleep(50 * time.Millisecond)
	advanced := fixed.Add(200 * time.Millisecond)
	currentTime.Store(&advanced)

	got := 0
	timeout := time.After(500 * time.Millisecond)
collect:
	for got < 2 {
		select {
		case e := <-events:
			if !e.BecameStale {
				t.Errorf("expected BecameStale=true, got %+v", e)
			}
			got++
		case <-timeout:
			break collect
		}
	}
	if got < 2 {
		t.Errorf("expected 2 stale events, got %d", got)
	}
	cancel()
}

func TestWatch_NoEmitWhenNoChange(t *testing.T) {
	m := New()
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return fixed }
	_ = m.RecordHeartbeat(AgentClaudeCode)
	_ = m.RecordHeartbeat(AgentWindsurf)

	events := make(chan StaleEvent, 8)
	emit := func(e StaleEvent) { events <- e }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Watch(ctx, 10*time.Millisecond, 10*time.Second, emit)

	// 50ms 待つ。stale 化しない条件(timeout=10s)
	time.Sleep(50 * time.Millisecond)
	cancel()

	// イベント数を数える
	close(events)
	count := 0
	for range events {
		count++
	}
	if count != 0 {
		t.Errorf("expected 0 events (no transitions), got %d", count)
	}
}

func TestWatch_StopsOnContextCancel(t *testing.T) {
	m := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Watch(ctx, 5*time.Millisecond, 100*time.Millisecond, nil)
		close(done)
	}()
	cancel()
	select {
	case <-done:
		// OK
	case <-time.After(500 * time.Millisecond):
		t.Error("Watch did not stop within 500ms after cancel")
	}
}

func TestWatch_NilEmitDoesNotPanic(t *testing.T) {
	m := New()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// nil emit を渡しても panic しないこと
	m.Watch(ctx, 5*time.Millisecond, 1*time.Millisecond, nil)
}

func TestWatch_DefaultIntervalAndTimeout(t *testing.T) {
	m := New()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// interval=0, idleTimeout=0 → デフォルト値が使われる(panic しないこと)
	m.Watch(ctx, 0, 0, nil)
}

// ─── UsageSummary (v0.16.0) ──────────────────────────────────

func TestUsageSummary_EmptyHistory(t *testing.T) {
	m := New()
	s := m.UsageSummary(AgentClaudeCode)
	if s.Agent != AgentClaudeCode {
		t.Errorf("agent: got %s", s.Agent)
	}
	if s.TotalReports != 0 {
		t.Errorf("expected 0 reports, got %d", s.TotalReports)
	}
	if s.CurrentPercent != 100 {
		t.Errorf("expected 100%% current, got %d", s.CurrentPercent)
	}
}

func TestUsageSummary_BasicMetrics(t *testing.T) {
	m := New()
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	// 1 時間で 100→80→60→40(40% 消費)
	for i, p := range []int{100, 80, 60, 40} {
		when := base.Add(time.Duration(i*20) * time.Minute)
		m.NowFn = func() time.Time { return when }
		_ = m.Report(AgentClaudeCode, p, "auto", time.Time{}, time.Time{})
	}
	// now を 4 番目の時刻に固定
	m.NowFn = func() time.Time { return base.Add(60 * time.Minute) }

	s := m.UsageSummary(AgentClaudeCode)
	if s.TotalReports != 4 {
		t.Errorf("total reports: got %d, want 4", s.TotalReports)
	}
	if s.CurrentPercent != 40 {
		t.Errorf("current: got %d, want 40", s.CurrentPercent)
	}
	// WindowHours = 1.0(60 min span)
	if s.WindowHours != 1.0 {
		t.Errorf("window hours: got %v, want 1.0", s.WindowHours)
	}
	// AvgConsumePerHour = 60% / 1h = 60 (100→40 over 1h)
	if s.AvgConsumePerHour != 60.0 {
		t.Errorf("avg consume: got %v, want 60.0", s.AvgConsumePerHour)
	}
	// Consumed1h: 1h 前 cutoff → first sample(100) なので 100 - 40 = 60
	if s.Consumed1h != 60.0 {
		t.Errorf("consumed 1h: got %v, want 60.0", s.Consumed1h)
	}
	// SlopePercentPerSec = (40-100) / 3600 = -0.01666...
	if s.SlopePercentPerSec >= 0 {
		t.Errorf("slope should be negative, got %v", s.SlopePercentPerSec)
	}
	// LastConsumeAt: 最後に減った瞬間 = 100→80 から 40 まで毎回減ってる → 最後の sample 時刻
	if s.LastConsumeAt.IsZero() {
		t.Error("LastConsumeAt should be set")
	}
}

func TestUsageSummary_NoConsumption(t *testing.T) {
	m := New()
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	// 同じ % で 3 回 report(消費なし)
	for i := 0; i < 3; i++ {
		when := base.Add(time.Duration(i*10) * time.Minute)
		m.NowFn = func() time.Time { return when }
		_ = m.Report(AgentClaudeCode, 80, "auto", time.Time{}, time.Time{})
	}
	s := m.UsageSummary(AgentClaudeCode)
	if s.AvgConsumePerHour != 0 {
		t.Errorf("no consumption: got %v, want 0", s.AvgConsumePerHour)
	}
	if !s.LastConsumeAt.IsZero() {
		t.Error("LastConsumeAt should be zero when nothing consumed")
	}
}

func TestUsageSummary_Samples(t *testing.T) {
	m := New()
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	for i, p := range []int{100, 70, 30} {
		when := base.Add(time.Duration(i) * time.Hour)
		m.NowFn = func() time.Time { return when }
		_ = m.Report(AgentClaudeCode, p, "auto", time.Time{}, time.Time{})
	}
	s := m.UsageSummary(AgentClaudeCode)
	if len(s.Samples) != 3 {
		t.Errorf("expected 3 samples, got %d", len(s.Samples))
	}
	// 古→新 順序
	if s.Samples[0].RemainingPercent != 100 || s.Samples[2].RemainingPercent != 30 {
		t.Errorf("samples not chronological: %+v", s.Samples)
	}
}

func TestUsageSummary_InvalidAgent(t *testing.T) {
	m := New()
	s := m.UsageSummary(Agent("invalid"))
	if s.TotalReports != 0 {
		t.Errorf("invalid agent should return zero summary")
	}
}

func TestAllUsageSummaries_ReturnsBoth(t *testing.T) {
	m := New()
	all := m.AllUsageSummaries()
	if len(all) != 2 {
		t.Errorf("expected 2 summaries, got %d", len(all))
	}
	if _, ok := all[AgentClaudeCode]; !ok {
		t.Error("missing claude_code")
	}
	if _, ok := all[AgentWindsurf]; !ok {
		t.Error("missing windsurf")
	}
}

// ─── v0.17.0: 精度修正(short-window 時の不信頼マーク)─────────

func TestUsageSummary_ShortWindowMarkedUnreliable(t *testing.T) {
	m := New()
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	// 1 秒間隔で 4 reports(window = 3 秒、MinReliableWindowMinutes=5 未満)
	for i, p := range []int{100, 75, 50, 25} {
		when := base.Add(time.Duration(i) * time.Second)
		m.NowFn = func() time.Time { return when }
		_ = m.Report(AgentClaudeCode, p, "auto", time.Time{}, time.Time{})
	}
	s := m.UsageSummary(AgentClaudeCode)
	if s.Reliable {
		t.Error("3-second window should be marked unreliable")
	}
	if s.AvgConsumePerHour != 0 {
		t.Errorf("unreliable window: AvgConsumePerHour should be 0, got %v", s.AvgConsumePerHour)
	}
	if s.SlopePercentPerSec != 0 {
		t.Errorf("unreliable window: SlopePercentPerSec should be 0, got %v", s.SlopePercentPerSec)
	}
	// 絶対量(Consumed1h/24h)は短 window でも意味があるので保持
	if s.Consumed1h != 75 {
		t.Errorf("Consumed1h should remain valid (75%% drop), got %v", s.Consumed1h)
	}
}

func TestUsageSummary_LongWindowMarkedReliable(t *testing.T) {
	m := New()
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	// 10 分間隔で 3 reports(window = 20 分、reliable)
	for i, p := range []int{100, 75, 50} {
		when := base.Add(time.Duration(i*10) * time.Minute)
		m.NowFn = func() time.Time { return when }
		_ = m.Report(AgentClaudeCode, p, "auto", time.Time{}, time.Time{})
	}
	s := m.UsageSummary(AgentClaudeCode)
	if !s.Reliable {
		t.Error("20-min window should be marked reliable")
	}
	if s.AvgConsumePerHour == 0 {
		t.Error("reliable window: AvgConsumePerHour should be populated")
	}
}

func TestUsageSummary_ExactlyMinWindowReliable(t *testing.T) {
	m := New()
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	// ちょうど 5 分間隔の 2 reports → 境界値で reliable
	m.NowFn = func() time.Time { return base }
	_ = m.Report(AgentClaudeCode, 100, "auto", time.Time{}, time.Time{})
	m.NowFn = func() time.Time { return base.Add(5 * time.Minute) }
	_ = m.Report(AgentClaudeCode, 80, "auto", time.Time{}, time.Time{})
	s := m.UsageSummary(AgentClaudeCode)
	if !s.Reliable {
		t.Error("exact 5-min window should be reliable (boundary inclusive)")
	}
}

func TestWarnThreshold_DefaultsTo20(t *testing.T) {
	m := &Monitor{WarnThreshold: 0}
	if got := m.warnThreshold(); got != 20 {
		t.Errorf("warnThreshold() = %d, want 20 (default)", got)
	}
	m.WarnThreshold = 30
	if got := m.warnThreshold(); got != 30 {
		t.Errorf("warnThreshold() = %d, want 30", got)
	}
}

func TestEarliestReset_BothZero(t *testing.T) {
	a := &AgentStatus{}
	b := &AgentStatus{}
	if got := earliestReset(a, b); got != "unknown" {
		t.Errorf("earliestReset with zero times = %q, want %q", got, "unknown")
	}
}

func TestEarliestReset_PicksEarliest(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	a := &AgentStatus{WindowResetsAt: t2}
	b := &AgentStatus{WindowResetsAt: t1}
	got := earliestReset(a, b)
	if got != t1.Format(time.RFC3339) {
		t.Errorf("earliestReset = %q, want %q", got, t1.Format(time.RFC3339))
	}
}

// TestEarliestReset_T1BeforeT2 covers the `return t1` branch in pickEarliest
// where t1.Before(t2) is true (t1 is earlier than t2).
func TestEarliestReset_T1BeforeT2(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	// a.WeeklyResetsAt = t1, b.WeeklyResetsAt = t2 → pickEarliest returns t1
	a := &AgentStatus{WeeklyResetsAt: t1}
	b := &AgentStatus{WeeklyResetsAt: t2}
	got := earliestReset(a, b)
	if got != t1.Format(time.RFC3339) {
		t.Errorf("earliestReset = %q, want %q (t1 before t2)", got, t1.Format(time.RFC3339))
	}
}

// ─── invalid-agent error paths ────────────────────────────────

func TestMarkSwitched_InvalidAgent(t *testing.T) {
	m := New()
	if err := m.MarkSwitched(Agent("invalid")); err == nil {
		t.Error("MarkSwitched with invalid agent should return error")
	}
}

func TestMarkResumed_InvalidAgent(t *testing.T) {
	m := New()
	if err := m.MarkResumed(Agent("invalid"), 50); err == nil {
		t.Error("MarkResumed with invalid agent should return error")
	}
}

// TestMarkResumed_ExhaustedAndWarn covers the StateExhausted (0%) and
// StateWarn (<20%) branches in MarkResumed.
func TestMarkResumed_ExhaustedAndWarn(t *testing.T) {
	m := New()
	_ = m.MarkSwitched(AgentClaudeCode)
	_ = m.MarkResumed(AgentClaudeCode, 0)
	st, _ := m.Status(AgentClaudeCode)
	if st.State != StateExhausted {
		t.Errorf("MarkResumed(0) → state = %s, want EXHAUSTED", st.State)
	}

	_ = m.MarkResumed(AgentClaudeCode, 10) // 10 < WarnThreshold(20)
	st, _ = m.Status(AgentClaudeCode)
	if st.State != StateWarn {
		t.Errorf("MarkResumed(10) → state = %s, want WARN", st.State)
	}
}

func TestStatus_InvalidAgent(t *testing.T) {
	m := New()
	_, err := m.Status(Agent("invalid"))
	if err == nil {
		t.Error("Status with invalid agent should return error")
	}
}

// ─── Report: WeeklyResetsAt persistence ──────────────────────

func TestReport_WeeklyResetPersisted(t *testing.T) {
	m := New()
	weekly := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	_ = m.Report(AgentClaudeCode, 80, "auto", time.Time{}, weekly)
	st, _ := m.Status(AgentClaudeCode)
	if !st.WeeklyResetsAt.Equal(weekly) {
		t.Errorf("WeeklyResetsAt = %v, want %v", st.WeeklyResetsAt, weekly)
	}
}

// ─── Recommend: windsurf unavailable → pick claude_code ──────

func TestRecommend_WindsurfUnavailable_PicksClaudeCode(t *testing.T) {
	m := New()
	// Exhaust windsurf; leave claude_code healthy.
	_ = m.Report(AgentWindsurf, 0, "auto", time.Time{}, time.Time{})
	agent, reason := m.Recommend()
	if agent != AgentClaudeCode {
		t.Errorf("Recommend = %s, want claude_code (windsurf exhausted)", agent)
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

// ─── ShouldHandoff: invalid agent and windsurf-as-current ────

func TestShouldHandoff_InvalidAgent(t *testing.T) {
	m := New()
	ok, _, reason := m.ShouldHandoff(Agent("invalid"))
	if ok {
		t.Error("ShouldHandoff with invalid agent should return false")
	}
	if reason == "" {
		t.Error("expected non-empty reason for invalid agent")
	}
}

// TestShouldHandoff_WindsurfExhausted covers the `else` branch in ShouldHandoff
// (current == AgentWindsurf → other = statuses[AgentClaudeCode]).
func TestShouldHandoff_WindsurfExhausted(t *testing.T) {
	m := New()
	// Exhaust windsurf; claude_code remains ACTIVE.
	_ = m.Report(AgentWindsurf, 0, "auto", time.Time{}, time.Time{})
	ok, target, _ := m.ShouldHandoff(AgentWindsurf)
	if !ok {
		t.Error("ShouldHandoff(windsurf) should recommend handoff when windsurf is exhausted")
	}
	if target != AgentClaudeCode {
		t.Errorf("ShouldHandoff target = %s, want claude_code", target)
	}
}

// ─── usabilityLocked: SWITCHED state ─────────────────────────

// TestRecommend_SwitchedAgent covers the `return false, "SWITCHED..."` branch
// in usabilityLocked. When both agents are SWITCHED, Recommend returns both-
// unavailable; when only one is SWITCHED, it returns the other.
func TestRecommend_SwitchedAgentUnavailable(t *testing.T) {
	m := New()
	_ = m.MarkSwitched(AgentClaudeCode)
	agent, reason := m.Recommend()
	// claude_code is SWITCHED (unavailable); windsurf should be recommended.
	if agent != AgentWindsurf {
		t.Errorf("Recommend = %s, want windsurf when claude_code is SWITCHED; reason=%s", agent, reason)
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}
