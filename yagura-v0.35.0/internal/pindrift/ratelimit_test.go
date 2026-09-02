package pindrift

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/github"
)

// ─── RateLimitGuard unit tests ──────────────────────────────

func TestRateLimitGuard_PassesWhenAmple(t *testing.T) {
	source := func() github.RateLimit {
		return github.RateLimit{Limit: 5000, Remaining: 4000, Reset: time.Now().Add(1 * time.Hour)}
	}
	g := NewRateLimitGuard(source)

	slept := false
	g.Sleeper = func(ctx context.Context, d time.Duration) error {
		slept = true
		return nil
	}
	if err := g.Wait(context.Background()); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if slept {
		t.Error("should not sleep when ample remaining")
	}
}

func TestRateLimitGuard_SleepsWhenLow(t *testing.T) {
	resetIn := 30 * time.Second
	now := time.Now()
	source := func() github.RateLimit {
		return github.RateLimit{Limit: 5000, Remaining: 5, Reset: now.Add(resetIn)}
	}
	g := NewRateLimitGuard(source)
	g.Clock = func() time.Time { return now }

	var slept time.Duration
	g.Sleeper = func(ctx context.Context, d time.Duration) error {
		slept = d
		return nil
	}
	if err := g.Wait(context.Background()); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// remaining < MinRemaining(100) → should sleep until reset
	if slept != resetIn {
		t.Errorf("expected sleep %v, got %v", resetIn, slept)
	}
}

func TestRateLimitGuard_RespectsMaxSleep(t *testing.T) {
	now := time.Now()
	source := func() github.RateLimit {
		// reset is far in future (e.g. 3600s)
		return github.RateLimit{Limit: 5000, Remaining: 0, Reset: now.Add(1 * time.Hour)}
	}
	g := NewRateLimitGuard(source)
	g.Clock = func() time.Time { return now }
	g.MaxSleep = 5 * time.Second // cap

	var slept time.Duration
	g.Sleeper = func(ctx context.Context, d time.Duration) error {
		slept = d
		return nil
	}
	_ = g.Wait(context.Background())
	if slept != 5*time.Second {
		t.Errorf("expected MaxSleep cap, got %v", slept)
	}
}

func TestRateLimitGuard_ContextCancel(t *testing.T) {
	now := time.Now()
	source := func() github.RateLimit {
		return github.RateLimit{Limit: 5000, Remaining: 0, Reset: now.Add(30 * time.Second)}
	}
	g := NewRateLimitGuard(source)
	g.Clock = func() time.Time { return now }
	g.Sleeper = func(ctx context.Context, d time.Duration) error {
		return ctx.Err() // simulate immediate cancellation
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := g.Wait(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRateLimitGuard_EmptyStateDoesNotSleep(t *testing.T) {
	source := func() github.RateLimit {
		return github.RateLimit{} // 初回 (zero value): Limit=0
	}
	g := NewRateLimitGuard(source)
	slept := false
	g.Sleeper = func(ctx context.Context, d time.Duration) error {
		slept = true
		return nil
	}
	_ = g.Wait(context.Background())
	if slept {
		t.Error("should not sleep when rate limit info is empty (initial state)")
	}
}

func TestRateLimitGuard_PastReset_UsesGracePeriod(t *testing.T) {
	now := time.Now()
	source := func() github.RateLimit {
		// Reset 時刻が過去(時計ずれ等)
		return github.RateLimit{Limit: 5000, Remaining: 0, Reset: now.Add(-10 * time.Second)}
	}
	g := NewRateLimitGuard(source)
	g.Clock = func() time.Time { return now }
	var slept time.Duration
	g.Sleeper = func(ctx context.Context, d time.Duration) error {
		slept = d
		return nil
	}
	_ = g.Wait(context.Background())
	// Should use grace period (5s), not negative
	if slept != 5*time.Second {
		t.Errorf("expected grace 5s, got %v", slept)
	}
}

// ─── Stats ──────────────────────────────────────────────────

func TestRateLimitGuard_Stats(t *testing.T) {
	now := time.Now()
	source := func() github.RateLimit {
		return github.RateLimit{Limit: 5000, Remaining: 5, Reset: now.Add(10 * time.Second)}
	}
	g := NewRateLimitGuard(source)
	g.Clock = func() time.Time { return now }
	g.Sleeper = func(ctx context.Context, d time.Duration) error { return nil }

	for i := 0; i < 3; i++ {
		_ = g.Wait(context.Background())
	}
	waits, total := g.Stats()
	if waits != 3 {
		t.Errorf("expected 3 waits, got %d", waits)
	}
	if total != 30*time.Second {
		t.Errorf("expected total 30s, got %v", total)
	}
}

// ─── Defaults ───────────────────────────────────────────────

func TestRateLimitGuard_DefaultsApplied(t *testing.T) {
	g := &RateLimitGuard{
		Source: func() github.RateLimit {
			return github.RateLimit{Limit: 5000, Remaining: 50, Reset: time.Now().Add(10 * time.Second)}
		},
	}
	// MinRemaining=0 → default 100, so 50 < 100 triggers sleep
	g.Sleeper = func(ctx context.Context, d time.Duration) error {
		// Should be called
		return nil
	}
	if err := g.Wait(context.Background()); err != nil {
		t.Error(err)
	}
}

// ─── sleep (default time.After path) ─────────────────────────

// TestRateLimitGuard_DefaultSleepCompletes covers the real `select` in sleep()
// (no Sleeper hook): a tiny duration lets the time.After arm fire → return nil.
func TestRateLimitGuard_DefaultSleepCompletes(t *testing.T) {
	g := &RateLimitGuard{} // no Sleeper → exercises the built-in select
	if err := g.sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("default sleep should complete with nil, got %v", err)
	}
}

// TestRateLimitGuard_DefaultSleepContextCancel covers the `case <-ctx.Done()`
// arm in sleep(): a pre-cancelled context returns ctx.Err() before the timer.
func TestRateLimitGuard_DefaultSleepContextCancel(t *testing.T) {
	g := &RateLimitGuard{} // no Sleeper
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := g.sleep(ctx, time.Hour) // long timer; ctx.Done wins immediately
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled from default sleep, got %v", err)
	}
}

// ─── 統合: Checker と RateLimitGuard ──────────────────────────

// mockGHWithRateLimit は LastRateLimit を持つ mock
type mockGHWithRateLimit struct {
	mockGH
	calls int
}

func TestChecker_UsesRateLimitGuard(t *testing.T) {
	mock := &mockGH{
		commits: map[string]*github.CommitInfo{},
	}
	mock.commits["a/b/"+goodSHA] = makeCommit(goodSHA, time.Now().UTC().Format(time.RFC3339))

	// Guard with always-empty (no rate limit info yet) → pass through
	c := New(mock)
	c.RateLimit = NewRateLimitGuard(func() github.RateLimit {
		return github.RateLimit{} // empty → no sleep
	})
	r := c.CheckPin(context.Background(), Pin{Owner: "a", Repo: "b", PinnedSHA: goodSHA})
	if r.Status != StatusOK {
		t.Errorf("expected OK, got %s: %s", r.Status, r.Detail)
	}
}

func TestChecker_RateLimitCancelsCheck(t *testing.T) {
	mock := &mockGH{commits: map[string]*github.CommitInfo{}}
	c := New(mock)
	c.RateLimit = &RateLimitGuard{
		Source: func() github.RateLimit {
			return github.RateLimit{Limit: 5000, Remaining: 0, Reset: time.Now().Add(1 * time.Hour)}
		},
		Sleeper: func(ctx context.Context, d time.Duration) error {
			return context.Canceled
		},
	}
	r := c.CheckPin(context.Background(), Pin{Owner: "a", Repo: "b", PinnedSHA: goodSHA})
	if r.Status != StatusUnverifiable {
		t.Errorf("expected UNVERIFIABLE on rate limit cancel, got %s", r.Status)
	}
}
