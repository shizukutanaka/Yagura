package dashboard

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/quotamonitor"
	"github.com/shizukutanaka/yagura/internal/registry"
)

func setupHandler(t *testing.T) (*Handler, *registry.Registry) {
	t.Helper()
	reg, _ := registry.New(t.TempDir())
	h, err := New(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h, reg
}

func TestNew_RequiresRegistry(t *testing.T) {
	if _, err := New(nil, nil); err == nil {
		t.Error("expected error for nil registry")
	}
}

func TestServeHTTP_RenderEmpty(t *testing.T) {
	h, _ := setupHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"<!DOCTYPE html>", "Yagura", "Portfolio Dashboard", "0 projects"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestServeHTTP_RenderWithProjects(t *testing.T) {
	h, reg := setupHandler(t)

	_ = reg.Add(&project.Project{
		Slug: "mihari", DisplayName: "Mihari", Repository: "shizukutanaka/mihari",
		Stage: project.StageActive, Priority: 5, Language: "Go",
		OpenPRs: 3, CIStatus: project.CIStatusPassing, LatestVersion: "v0.11.0",
	})
	_ = reg.Add(&project.Project{
		Slug: "old-tool", DisplayName: "Old", Repository: "x/old",
		Stage: project.StageArchived,
	})

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{
		"mihari", "shizukutanaka/mihari", "v0.11.0",
		"active", "archived", "Go",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	// Counts セクション
	if !strings.Contains(body, "Active") {
		t.Error("missing Active label")
	}
}

func TestServeHTTP_RejectsPost(t *testing.T) {
	h, _ := setupHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/dashboard", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
	if w.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("Allow header missing: %q", w.Header().Get("Allow"))
	}
}

func TestServeHTTP_NoStoreCache(t *testing.T) {
	h, _ := setupHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"},
		{"日本語テスト", 4, "日本語…"},
		{"", 5, ""},
	}
	for _, c := range cases {
		if got := truncate(c.in, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// TestTruncate_SmallWidthsDoNotPanic pins the boundary: a non-empty string with
// n=0 used to compute r[:n-1] = r[:-1] and panic. n<=0 must yield "" and n=1
// must yield just the ellipsis, never panic.
func TestTruncate_SmallWidthsDoNotPanic(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 0, ""},  // n=0 → nothing fits (was a panic)
		{"hello", -1, ""}, // negative → "" (was a panic)
		{"hello", 1, "…"}, // only the ellipsis fits
		{"", 0, ""},       // empty stays empty
	}
	for _, c := range cases {
		got := truncate(c.in, c.n)
		if got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestStaleClass(t *testing.T) {
	cases := []struct {
		days int
		want string
	}{
		{-1, "unknown"},
		{0, "fresh"},
		{5, "fresh"},
		{15, "warm"},
		{45, "cool"},
		{200, "cold"},
	}
	for _, c := range cases {
		if got := staleClass(c.days); got != c.want {
			t.Errorf("staleClass(%d) = %q, want %q", c.days, got, c.want)
		}
	}
}

func TestStageOrder(t *testing.T) {
	if stageOrder(project.StageActive) >= stageOrder(project.StageArchived) {
		t.Error("active should come before archived")
	}
	if stageOrder(project.StageMaintenance) >= stageOrder(project.StagePaused) {
		t.Error("maintenance should come before paused")
	}
}

func TestStageColor(t *testing.T) {
	colors := map[project.Stage]string{}
	for _, s := range []project.Stage{
		project.StageActive, project.StageMaintenance,
		project.StagePaused, project.StageArchived,
	} {
		c := stageColor(s)
		if c == "" {
			t.Errorf("no color for %q", s)
		}
		if prev, ok := colors[s]; ok && prev != c {
			t.Errorf("inconsistent color for %q", s)
		}
		colors[s] = c
	}
}

// ─── helper coverage ─────────────────────────────────────────

func TestSetNowFunc(t *testing.T) {
	reg, _ := registry.New(t.TempDir())
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h, err := New(reg, logger)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.SetNowFunc(func() time.Time { return fixed })
	if got := h.now(); !got.Equal(fixed) {
		t.Errorf("now should be pinned: got %v want %v", got, fixed)
	}
}

func TestStaleClassByTime_Variants(t *testing.T) {
	now := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		t     time.Time
		stage project.Stage
		want  string
	}{
		{"zero time", time.Time{}, project.StageActive, ""},
		{"non-active stage", now.AddDate(0, 0, -60), project.StageMaintenance, ""},
		{"fresh", now.AddDate(0, 0, -3), project.StageActive, ""},
		{"amber (14d)", now.AddDate(0, 0, -14), project.StageActive, "stale-amber"},
		{"amber (20d)", now.AddDate(0, 0, -20), project.StageActive, "stale-amber"},
		{"red (30d)", now.AddDate(0, 0, -30), project.StageActive, "stale-red"},
		{"red (100d)", now.AddDate(0, 0, -100), project.StageActive, "stale-red"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := staleClassByTime(tt.t, now, tt.stage); got != tt.want {
				t.Errorf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestDaysSince(t *testing.T) {
	now := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "—"},
		{now, "0d"},
		{now.AddDate(0, 0, -7), "7d"},
		{now.AddDate(0, 0, -100), "100d"},
	}
	for _, tt := range tests {
		got := daysSince(tt.t, now)
		if got != tt.want {
			t.Errorf("daysSince(%v): got %q want %q", tt.t, got, tt.want)
		}
	}
}

func TestFmtTime(t *testing.T) {
	if got := fmtTime(time.Time{}); got != "—" {
		t.Errorf("zero time should be em-dash: got %q", got)
	}
	tt := time.Date(2026, 5, 13, 10, 23, 0, 0, time.UTC)
	if got := fmtTime(tt); got != "2026-05-13 10:23" {
		t.Errorf("expected 2026-05-13 10:23, got %q", got)
	}
}

func TestCIColor_AllStates(t *testing.T) {
	tests := map[project.CIStatus]string{
		project.CIStatusPassing: "ci-pass",
		project.CIStatusFailing: "ci-fail",
		project.CIStatusUnknown: "ci-unk",
		project.CIStatus(""):    "ci-unk",
	}
	for in, want := range tests {
		if got := ciColor(in); got != want {
			t.Errorf("ciColor(%q): got %q want %q", in, got, want)
		}
	}
}

func TestPriorityClass_AllRanges(t *testing.T) {
	tests := map[int]string{
		0: "prio-lo",
		1: "prio-lo",
		2: "prio-mid",
		3: "prio-mid",
		4: "prio-hi",
		5: "prio-hi",
	}
	for in, want := range tests {
		if got := priorityClass(in); got != want {
			t.Errorf("priorityClass(%d): got %q want %q", in, got, want)
		}
	}
}

func TestServeHTTP_MethodNotAllowed(t *testing.T) {
	reg, _ := registry.New(t.TempDir())
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h, _ := New(reg, logger)

	req := httptest.NewRequest(http.MethodPost, "/dashboard", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST should be 405, got %d", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow header: got %q", allow)
	}
}

func TestServeHTTP_HEAD(t *testing.T) {
	reg, _ := registry.New(t.TempDir())
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h, _ := New(reg, logger)

	req := httptest.NewRequest(http.MethodHead, "/dashboard", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("HEAD should return 200, got %d", w.Code)
	}
}

// ─── securityCell / scoreClass ───────────────────────────────

func TestSecurityScoreClass(t *testing.T) {
	tests := []struct {
		score float64
		want  string
	}{
		{10.0, "sec-excellent"},
		{8.0, "sec-excellent"},
		{7.5, "sec-good"},
		{6.0, "sec-good"},
		{5.0, "sec-fair"},
		{4.0, "sec-fair"},
		{2.0, "sec-poor"},
		{0.1, "sec-poor"},
		{0.0, "sec-na"},
		{-1, "sec-na"},
	}
	for _, tt := range tests {
		if got := securityScoreClass(tt.score); got != tt.want {
			t.Errorf("score=%g: got %s want %s", tt.score, got, tt.want)
		}
	}
}

func TestScorecardTitle(t *testing.T) {
	tests := map[float64]string{
		9.0: "excellent",
		7.0: "good",
		5.0: "fair",
		2.0: "poor",
		0.0: "poor",
	}
	for in, want := range tests {
		if got := scorecardTitle(in); got != want {
			t.Errorf("score=%g: got %s want %s", in, got, want)
		}
	}
}

func TestSecurityCell_NotScanned(t *testing.T) {
	p := project.Project{Slug: "x"}
	got := string(securityCell(p))
	if !strings.Contains(got, "sec-na") || !strings.Contains(got, "—") {
		t.Errorf("not scanned: got %q", got)
	}
}

func TestSecurityCell_ScorecardOnly(t *testing.T) {
	p := project.Project{
		ScorecardScore: 8.5,
		ScorecardAt:    time.Now(),
	}
	got := string(securityCell(p))
	if !strings.Contains(got, "8.5") {
		t.Errorf("score not rendered: %q", got)
	}
	if !strings.Contains(got, "sec-excellent") {
		t.Errorf("class wrong: %q", got)
	}
	if strings.Contains(got, "vuln-") {
		t.Errorf("should not include vuln tags: %q", got)
	}
}

func TestSecurityCell_WithVulns(t *testing.T) {
	p := project.Project{
		ScorecardScore: 6.0,
		ScorecardAt:    time.Now(),
		VulnCritical:   2,
		VulnHigh:       3,
		VulnMedium:     1,
		VulnScanAt:     time.Now(),
	}
	got := string(securityCell(p))
	if !strings.Contains(got, "2C") {
		t.Errorf("missing critical badge: %q", got)
	}
	if !strings.Contains(got, "3H") {
		t.Errorf("missing high badge: %q", got)
	}
	if !strings.Contains(got, "1M") {
		t.Errorf("missing medium badge: %q", got)
	}
}

func TestSecurityCell_VulnsOnlyNoScorecard(t *testing.T) {
	p := project.Project{
		VulnCritical: 1,
		VulnScanAt:   time.Now(),
	}
	got := string(securityCell(p))
	if !strings.Contains(got, "—") {
		t.Errorf("should show — for missing scorecard: %q", got)
	}
	if !strings.Contains(got, "1C") {
		t.Errorf("should show vuln count: %q", got)
	}
}

func TestSecurityCell_ZeroVulnsHidesBadges(t *testing.T) {
	p := project.Project{
		ScorecardScore: 8.0,
		ScorecardAt:    time.Now(),
		VulnScanAt:     time.Now(), // scanned but found 0
	}
	got := string(securityCell(p))
	if strings.Contains(got, "0C") || strings.Contains(got, "vuln-sep") {
		t.Errorf("zero vulns should hide badges: %q", got)
	}
}

func TestFormatScore(t *testing.T) {
	tests := map[float64]string{
		9.84:  "9.8",
		10.0:  "10.0",
		0.0:   "0.0",
		5.555: "5.6",
	}
	for in, want := range tests {
		if got := formatScore(in); got != want {
			t.Errorf("formatScore(%g): got %s want %s", in, got, want)
		}
	}
}

// ─── buildSparklinePath ───────────────────────────────────────

func TestBuildSparklinePath_Empty(t *testing.T) {
	if got := buildSparklinePath(nil); got != "" {
		t.Errorf("empty samples should return empty, got %q", got)
	}
}

func TestBuildSparklinePath_OneSample(t *testing.T) {
	samples := []quotamonitor.ReportEvent{{RemainingPercent: 50}}
	if got := buildSparklinePath(samples); got != "" {
		t.Errorf("single sample should return empty (need ≥2), got %q", got)
	}
}

func TestBuildSparklinePath_TwoSamples(t *testing.T) {
	samples := []quotamonitor.ReportEvent{
		{RemainingPercent: 100},
		{RemainingPercent: 0},
	}
	got := buildSparklinePath(samples)
	if got == "" {
		t.Fatal("two samples should return a non-empty path")
	}
	// First point at x=0, last point at x=100
	if !strings.HasPrefix(got, "0.0,") {
		t.Errorf("first point should start at x=0: %q", got)
	}
	if !strings.Contains(got, "100.0,") {
		t.Errorf("last point should be at x=100: %q", got)
	}
}

func TestBuildSparklinePath_ClampsBoundary(t *testing.T) {
	// RemainingPercent=100 → y=0 (top), RemainingPercent=0 → y=30 (bottom)
	samples := []quotamonitor.ReportEvent{
		{RemainingPercent: 100},
		{RemainingPercent: 50},
	}
	got := buildSparklinePath(samples)
	if got == "" {
		t.Fatal("should return a path string")
	}
	// 100% remaining → y=0.0
	if !strings.HasPrefix(got, "0.0,0.0") {
		t.Errorf("100%% remaining should map to y=0, path=%q", got)
	}
}

// ─── SetAgentStatusProvider ───────────────────────────────────

func TestSetAgentStatusProvider(t *testing.T) {
	h, _ := setupHandler(t)
	// nil provider disables the quota column
	h.SetAgentStatusProvider(nil)
	// no panic; ServeHTTP should still succeed
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("ServeHTTP after nil provider: code = %d", w.Code)
	}
}
