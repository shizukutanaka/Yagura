package quotamonitor

import (
	"testing"
	"time"
)

// ─── Forecast: 線形枯渇予測 ─────────────────────────────────

func TestForecast_LinearDepletion(t *testing.T) {
	m := New()
	// 仮の時刻列で 100% → 80% → 60% → 40% (毎 10 秒で 20% 消費)
	// → 残り 40% から枯渇まで 20 秒 → 予測 30 秒後
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	times := []time.Time{
		base,
		base.Add(10 * time.Second),
		base.Add(20 * time.Second),
		base.Add(30 * time.Second),
	}
	percents := []int{100, 80, 60, 40}
	for i, p := range percents {
		when := times[i]
		m.NowFn = func() time.Time { return when }
		_ = m.Report(AgentClaudeCode, p, "auto", time.Time{}, time.Time{})
	}
	// 予測は base + 50 秒(40% から 20%/10s 消費 → 20 秒で empty → base+30s+20s=base+50s)
	expected := base.Add(50 * time.Second)
	f := m.Forecast(AgentClaudeCode)
	diff := f.PredictedEmptyAt.Sub(expected)
	if diff < -2*time.Second || diff > 2*time.Second {
		t.Errorf("predicted: %v, expected near %v (diff %v)",
			f.PredictedEmptyAt, expected, diff)
	}
	if f.Confidence < 0.7 {
		t.Errorf("confidence: got %.2f, expected >= 0.7 (perfect linear, 4/10 samples → ~0.76)", f.Confidence)
	}
	if f.SamplesUsed != 4 {
		t.Errorf("samples_used: got %d, want 4", f.SamplesUsed)
	}
	if f.SlopePerSecond >= 0 {
		t.Errorf("slope should be negative (depleting), got %f", f.SlopePerSecond)
	}
}

func TestForecast_InsufficientSamples(t *testing.T) {
	m := New()
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return base }
	_ = m.Report(AgentClaudeCode, 100, "auto", time.Time{}, time.Time{})
	_ = m.Report(AgentClaudeCode, 90, "auto", time.Time{}, time.Time{})
	// 2 サンプル < MinForecastSamples(3) → 予測不能
	f := m.Forecast(AgentClaudeCode)
	if !f.PredictedEmptyAt.IsZero() {
		t.Errorf("expected zero predicted empty at, got %v", f.PredictedEmptyAt)
	}
	if f.SamplesUsed != 2 {
		t.Errorf("samples_used: got %d, want 2", f.SamplesUsed)
	}
}

func TestForecast_AlreadyExhausted(t *testing.T) {
	m := New()
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	for i, p := range []int{100, 50, 0} {
		when := base.Add(time.Duration(i*10) * time.Second)
		m.NowFn = func() time.Time { return when }
		_ = m.Report(AgentClaudeCode, p, "auto", time.Time{}, time.Time{})
	}
	f := m.Forecast(AgentClaudeCode)
	if !f.PredictedEmptyAt.IsZero() {
		t.Error("forecast should be zero when already exhausted")
	}
}

func TestForecast_RecoveringOrStable(t *testing.T) {
	m := New()
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	// 回復: 50% → 60% → 70% → 80%(slope 正)
	for i, p := range []int{50, 60, 70, 80} {
		when := base.Add(time.Duration(i*10) * time.Second)
		m.NowFn = func() time.Time { return when }
		_ = m.Report(AgentClaudeCode, p, "auto", time.Time{}, time.Time{})
	}
	f := m.Forecast(AgentClaudeCode)
	if !f.PredictedEmptyAt.IsZero() {
		t.Errorf("non-depleting should not forecast empty: %v", f.PredictedEmptyAt)
	}
	if f.SlopePerSecond <= 0 {
		t.Errorf("slope should be positive (recovering), got %f", f.SlopePerSecond)
	}
}

func TestForecast_WindowSizeCap(t *testing.T) {
	m := New()
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	// 30 件の Report → history は最大 ForecastWindowSize(=10)で打ち切られる
	for i := 0; i < 30; i++ {
		when := base.Add(time.Duration(i) * time.Second)
		m.NowFn = func() time.Time { return when }
		_ = m.Report(AgentClaudeCode, 100-i*2, "auto", time.Time{}, time.Time{})
	}
	f := m.Forecast(AgentClaudeCode)
	if f.SamplesUsed != ForecastWindowSize {
		t.Errorf("samples_used: got %d, want %d (window cap)", f.SamplesUsed, ForecastWindowSize)
	}
}

func TestForecast_InvalidAgent(t *testing.T) {
	m := New()
	f := m.Forecast(Agent("invalid"))
	if f.Reason != "unknown agent" {
		t.Errorf("reason: got %q, want 'unknown agent'", f.Reason)
	}
}

func TestForecast_NoisyButTrending(t *testing.T) {
	m := New()
	base := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	// ジッタあり、全体としては減少
	percents := []int{100, 92, 85, 78, 71, 65}
	for i, p := range percents {
		when := base.Add(time.Duration(i*10) * time.Second)
		m.NowFn = func() time.Time { return when }
		_ = m.Report(AgentClaudeCode, p, "auto", time.Time{}, time.Time{})
	}
	f := m.Forecast(AgentClaudeCode)
	if f.PredictedEmptyAt.IsZero() {
		t.Error("should forecast despite noise")
	}
	// noisy trending では confidence は 1.0 未満になるはず
	if f.Confidence < 0.5 || f.Confidence > 1.0 {
		t.Errorf("confidence out of expected range: %.2f", f.Confidence)
	}
}

func TestForecast_ClusteredSamples(t *testing.T) {
	m := New()
	// 全部同じ時刻 → denom=0 → forecast 不能
	fixed := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	m.NowFn = func() time.Time { return fixed }
	for i := 0; i < 5; i++ {
		_ = m.Report(AgentClaudeCode, 100-i*10, "auto", time.Time{}, time.Time{})
	}
	f := m.Forecast(AgentClaudeCode)
	if !f.PredictedEmptyAt.IsZero() {
		t.Error("clustered samples should not forecast")
	}
}

// ─── computeConfidence direct-call branch coverage ────────────

// TestComputeConfidence_InsufficientSamples covers the `return 0` branch when
// len(history) < MinForecastSamples. Called directly since Forecast guards this.
func TestComputeConfidence_InsufficientSamples(t *testing.T) {
	got := computeConfidence([]ReportEvent{}, 0, 0)
	if got != 0 {
		t.Errorf("empty history should give confidence 0, got %f", got)
	}
	// Single sample (< 3)
	one := []ReportEvent{{At: time.Now(), RemainingPercent: 50}}
	if got := computeConfidence(one, -1.0, 50.0); got != 0 {
		t.Errorf("one sample should give confidence 0, got %f", got)
	}
}

// TestComputeConfidence_OversizeSamples covers the `sampleScore = 1.0` clamp.
// Passing > ForecastWindowSize samples makes sampleScore > 1.0 before the clamp.
func TestComputeConfidence_OversizeSamples(t *testing.T) {
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	history := make([]ReportEvent, ForecastWindowSize+2) // 12 > 10
	for i := range history {
		history[i] = ReportEvent{
			At:               base.Add(time.Duration(i*10) * time.Second),
			RemainingPercent: 100 - i*5,
		}
	}
	// slope chosen to be negative so we're past the >= 0 guard in Forecast
	got := computeConfidence(history, -0.5, 100.0)
	if got <= 0 || got > 1 {
		t.Errorf("oversize samples: confidence = %f, want (0,1]", got)
	}
}

// TestComputeConfidence_NoVariance covers `return sampleScore * 0.5` when
// all Y values are identical (totalSS == 0). We pass a fake negative slope so
// the function doesn't short-circuit.
func TestComputeConfidence_NoVariance(t *testing.T) {
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	history := make([]ReportEvent, MinForecastSamples)
	for i := range history {
		history[i] = ReportEvent{
			At:               base.Add(time.Duration(i*10) * time.Second),
			RemainingPercent: 50, // all same → totalSS = 0
		}
	}
	// slope=-1 (non-zero) to prevent the `slope >= 0` guard if called via Forecast
	got := computeConfidence(history, -1.0, 50.0)
	sampleScore := float64(len(history)) / float64(ForecastWindowSize)
	want := sampleScore * 0.5
	if got != want {
		t.Errorf("no-variance confidence = %f, want %f (sampleScore*0.5)", got, want)
	}
}

// TestComputeConfidence_BadFit covers the `rSquared = 0` clamp for rSquared < 0.
// A slope/intercept that predict values wildly off the data will produce
// sumResSq >> totalSS, yielding rSquared < 0.
func TestComputeConfidence_BadFit(t *testing.T) {
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	history := []ReportEvent{
		{At: base, RemainingPercent: 90},
		{At: base.Add(10 * time.Second), RemainingPercent: 80},
		{At: base.Add(20 * time.Second), RemainingPercent: 70},
		{At: base.Add(30 * time.Second), RemainingPercent: 60},
	}
	// Wildly wrong slope/intercept: predicts ~10000% → huge residuals
	got := computeConfidence(history, -500.0, 10000.0)
	if got < 0 || got > 1 {
		t.Errorf("bad-fit confidence should be clamped to [0,1], got %f", got)
	}
}
