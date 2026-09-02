package scanner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/github"
	"github.com/shizukutanaka/yagura/internal/osv"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/registry"
	"github.com/shizukutanaka/yagura/internal/scorecard"
)

// ─── mocks ───────────────────────────────────────────────────

type mockOSV struct {
	mu       sync.Mutex
	calls    int
	failOn   map[string]error
	vulnsFor map[string][]osv.Vuln
}

func (m *mockOSV) Query(_ context.Context, ecosystem, pkg, version string) ([]osv.Vuln, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	key := ecosystem + "|" + pkg
	if err, ok := m.failOn[key]; ok {
		return nil, err
	}
	return m.vulnsFor[key], nil
}

type mockScorecard struct {
	mu       sync.Mutex
	calls    int
	failOn   map[string]error
	scoreFor map[string]*scorecard.Score
}

func (m *mockScorecard) Fetch(_ context.Context, repo string) (*scorecard.Score, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if err, ok := m.failOn[repo]; ok {
		return nil, err
	}
	return m.scoreFor[repo], nil
}

// ─── test setup helpers ──────────────────────────────────────

func newScannerForSecurityTest(t *testing.T) *Scanner {
	t.Helper()
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	gh := github.NewClient(github.Config{Token: "ghp_test", BaseURL: "http://invalid.example"})
	return New(Config{
		Registry: reg,
		GitHub:   gh,
		Logger:   logger,
		Interval: time.Hour,
	})
}

func addProject(t *testing.T, s *Scanner, slug, repo, lang, version string) {
	t.Helper()
	p := &project.Project{
		Slug:          slug,
		DisplayName:   slug,
		Repository:    repo,
		Language:      lang,
		LatestVersion: version,
		Stage:         project.StageActive,
	}
	if err := s.registry.Add(p); err != nil {
		t.Fatal(err)
	}
}

// ─── tests ───────────────────────────────────────────────────

func TestSecurityScanner_BothClientsNil(t *testing.T) {
	s := newScannerForSecurityTest(t)
	ss := s.NewSecurityScanner(nil, nil, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Start should return immediately without spawning goroutine when both clients are nil
	ss.Start(ctx)
	// If goroutine was started, WaitGroup wouldn't complete on cancel quickly
	cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("disabled security scanner should not spawn goroutine")
	}
}

func TestSecurityScanner_ScorecardSuccess(t *testing.T) {
	s := newScannerForSecurityTest(t)
	addProject(t, s, "alpha", "github.com/x/alpha", "Go", "")

	sc := &mockScorecard{
		scoreFor: map[string]*scorecard.Score{
			"github.com/x/alpha": {
				Repo:  "github.com/x/alpha",
				Score: 8.5,
			},
		},
	}
	ss := s.NewSecurityScanner(nil, sc, 24*time.Hour)
	ss.pause = 0 // テストでは sleep 不要
	ss.runOnce(context.Background())

	got, _ := s.registry.Get("alpha")
	if got.ScorecardScore != 8.5 {
		t.Errorf("scorecard score not saved: got %g", got.ScorecardScore)
	}
	if got.ScorecardAt.IsZero() {
		t.Error("ScorecardAt should be set")
	}
}

func TestSecurityScanner_ScorecardNotScored(t *testing.T) {
	s := newScannerForSecurityTest(t)
	addProject(t, s, "alpha", "github.com/x/alpha", "Go", "")

	sc := &mockScorecard{
		failOn: map[string]error{
			"github.com/x/alpha": scorecard.ErrNotScored,
		},
	}
	ss := s.NewSecurityScanner(nil, sc, 24*time.Hour)
	ss.pause = 0
	ss.runOnce(context.Background())

	got, _ := s.registry.Get("alpha")
	// Not scored should leave fields at zero (no error)
	if got.ScorecardScore != 0 {
		t.Errorf("score should remain 0 for not-scored repo, got %g", got.ScorecardScore)
	}
	if !got.ScorecardAt.IsZero() {
		t.Error("ScorecardAt should remain zero for not-scored repo")
	}
}

func TestSecurityScanner_ScorecardError(t *testing.T) {
	s := newScannerForSecurityTest(t)
	addProject(t, s, "alpha", "github.com/x/alpha", "Go", "")

	sc := &mockScorecard{
		failOn: map[string]error{"github.com/x/alpha": errors.New("network error")},
	}
	ss := s.NewSecurityScanner(nil, sc, 24*time.Hour)
	ss.pause = 0
	ss.runOnce(context.Background())

	// Should not crash; existing values preserved
	got, _ := s.registry.Get("alpha")
	if got.ScorecardScore != 0 {
		t.Errorf("score should be 0, got %g", got.ScorecardScore)
	}
}

func TestSecurityScanner_VulnsAggregation(t *testing.T) {
	s := newScannerForSecurityTest(t)
	addProject(t, s, "alpha", "github.com/x/alpha", "Go", "v1.2.3")

	o := &mockOSV{
		vulnsFor: map[string][]osv.Vuln{
			"Go|github.com/x/alpha": {
				{ID: "C-1", Severity: osv.SeverityCritical},
				{ID: "C-2", Severity: osv.SeverityCritical},
				{ID: "H-1", Severity: osv.SeverityHigh},
				{ID: "M-1", Severity: osv.SeverityMedium},
				{ID: "L-1", Severity: osv.SeverityLow},
				{ID: "U-1", Severity: osv.SeverityUnknown}, // unknown は集計対象外
			},
		},
	}
	ss := s.NewSecurityScanner(o, nil, 24*time.Hour)
	ss.pause = 0
	ss.runOnce(context.Background())

	got, _ := s.registry.Get("alpha")
	if got.VulnCritical != 2 || got.VulnHigh != 1 || got.VulnMedium != 1 || got.VulnLow != 1 {
		t.Errorf("aggregation wrong: c=%d h=%d m=%d l=%d",
			got.VulnCritical, got.VulnHigh, got.VulnMedium, got.VulnLow)
	}
	if got.TotalVulns() != 5 {
		t.Errorf("TotalVulns: got %d", got.TotalVulns())
	}
	if !got.HasCriticalSecurityIssue() {
		t.Error("should have critical issue")
	}
}

func TestSecurityScanner_VulnsNoLanguage(t *testing.T) {
	s := newScannerForSecurityTest(t)
	p := &project.Project{
		Slug:       "nolang",
		Repository: "github.com/x/nolang",
		Language:   "", // missing
		Stage:      project.StageActive,
	}
	_ = s.registry.Add(p)

	o := &mockOSV{} // 呼ばれてはならない
	ss := s.NewSecurityScanner(o, nil, 24*time.Hour)
	ss.pause = 0
	ss.runOnce(context.Background())

	if o.calls != 0 {
		t.Errorf("should skip projects without language, got %d calls", o.calls)
	}
}

func TestSecurityScanner_SkipsArchived(t *testing.T) {
	s := newScannerForSecurityTest(t)
	p := &project.Project{
		Slug:       "old",
		Repository: "github.com/x/old",
		Language:   "Go",
		Stage:      project.StageArchived,
	}
	_ = s.registry.Add(p)

	o := &mockOSV{}
	sc := &mockScorecard{}
	ss := s.NewSecurityScanner(o, sc, 24*time.Hour)
	ss.pause = 0
	ss.runOnce(context.Background())

	if o.calls != 0 || sc.calls != 0 {
		t.Errorf("archived should be skipped: osv=%d sc=%d", o.calls, sc.calls)
	}
}

func TestSecurityScanner_ContextCancel(t *testing.T) {
	s := newScannerForSecurityTest(t)
	// 沢山 project を追加して長い scan にする
	for i := 0; i < 5; i++ {
		slug := "p" + string(rune('a'+i))
		addProject(t, s, slug, "github.com/x/"+slug, "Go", "")
	}

	o := &mockOSV{vulnsFor: map[string][]osv.Vuln{}}
	ss := s.NewSecurityScanner(o, nil, 24*time.Hour)
	ss.pause = 50 * time.Millisecond // ループ中に cancel を入れる隙間

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	ss.runOnce(ctx)

	// Cancel kicked in partway through; should NOT have called for all 5
	if o.calls >= 5 {
		t.Errorf("context cancel didn't stop scan: got %d calls", o.calls)
	}
}

func TestSummarizeTopVulns(t *testing.T) {
	vulns := []osv.Vuln{
		{ID: "A", Severity: osv.SeverityCritical},
		{ID: "B", Severity: osv.SeverityHigh},
		{ID: "C", Severity: osv.SeverityMedium},
	}
	got := summarizeTopVulns(vulns, 2)
	if !strings.Contains(got, "A(CRITICAL)") || !strings.Contains(got, "B(HIGH)") {
		t.Errorf("unexpected: %q", got)
	}
	if strings.Contains(got, "C(") {
		t.Errorf("should only include top 2: %q", got)
	}
	// N larger than slice
	got = summarizeTopVulns(vulns, 99)
	if !strings.Contains(got, "C(MEDIUM)") {
		t.Errorf("should include all when N exceeds len: %q", got)
	}
	// Empty input
	if summarizeTopVulns(nil, 5) != "" {
		t.Error("empty input should give empty string")
	}
}

func TestSecurityScanner_DefaultInterval(t *testing.T) {
	// interval ≤ 0 should be clamped to 24h
	s := newScannerForSecurityTest(t)
	ss := s.NewSecurityScanner(nil, nil, 0)
	if ss.interval != 24*time.Hour {
		t.Errorf("interval = %v, want 24h", ss.interval)
	}
	ss2 := s.NewSecurityScanner(nil, nil, -1*time.Second)
	if ss2.interval != 24*time.Hour {
		t.Errorf("negative interval = %v, want 24h", ss2.interval)
	}
}

func TestSecurityScanner_ScorecardNoRepo(t *testing.T) {
	// project with empty repository → scanScorecard returns false immediately
	s := newScannerForSecurityTest(t)
	p := &project.Project{
		Slug:        "norepo",
		Repository:  "", // empty
		Language:    "Go",
		Stage:       project.StageActive,
		DisplayName: "no repo",
	}
	_ = s.registry.Add(p)

	sc := &mockScorecard{}
	ss := s.NewSecurityScanner(nil, sc, 24*time.Hour)
	ss.pause = 0
	ss.runOnce(context.Background())

	// Fetch should never be called for a project with no repository
	if sc.calls != 0 {
		t.Errorf("scorecard.Fetch should not be called for empty repo, got %d calls", sc.calls)
	}
}

func TestSecurityScanner_VulnsQueryError(t *testing.T) {
	// OSV query error → scan returns false, project is not updated
	s := newScannerForSecurityTest(t)
	addProject(t, s, "errproj", "github.com/x/errproj", "Go", "")

	o := &mockOSV{
		failOn: map[string]error{
			"Go|github.com/x/errproj": errors.New("osv unavailable"),
		},
	}
	ss := s.NewSecurityScanner(o, nil, 24*time.Hour)
	ss.pause = 0
	ss.runOnce(context.Background())

	got, _ := s.registry.Get("errproj")
	if got.VulnCritical != 0 {
		t.Errorf("on query error, VulnCritical should remain 0, got %d", got.VulnCritical)
	}
}

func TestSecurityScanner_Start_ContextCancel(t *testing.T) {
	s := newScannerForSecurityTest(t)
	addProject(t, s, "alpha", "github.com/x/alpha", "Go", "")
	sc := &mockScorecard{
		scoreFor: map[string]*scorecard.Score{
			"github.com/x/alpha": {Score: 7.0},
		},
	}
	ss := s.NewSecurityScanner(nil, sc, 100*time.Millisecond)
	ss.pause = 0

	ctx, cancel := context.WithCancel(context.Background())
	ss.Start(ctx)
	time.Sleep(50 * time.Millisecond) // 初回 fire の時間
	cancel()

	// goroutine が cleanly 停止することを確認
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Error("SecurityScanner did not stop after context cancel")
	}

	if sc.calls < 1 {
		t.Error("should have fired at least once on startup")
	}
}
