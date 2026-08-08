package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/github"
	"github.com/shizukutanaka/yagura/internal/logging"
	"github.com/shizukutanaka/yagura/internal/osv"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/registry"
	"github.com/shizukutanaka/yagura/internal/scorecard"
)

// fakeGitHub は最小限の GitHub REST API を模す httptest server。
func fakeGitHub(t *testing.T, archived bool, prCount, issuesCount int, conclusion string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			// per_page=1 + Link header trick
			if prCount > 1 {
				w.Header().Set("Link",
					`<https://api.github.com/repos/o/r/pulls?per_page=1&page=`+itoa(prCount)+`>; rel="last"`)
			}
			items := make([]struct{}, minI(prCount, 1))
			_ = json.NewEncoder(w).Encode(items)
		case strings.HasSuffix(r.URL.Path, "/issues"):
			// issue count via per_page=1 + Link header
			if issuesCount > 1 {
				w.Header().Set("Link",
					`<https://api.github.com/repos/o/r/issues?per_page=1&page=`+itoa(issuesCount)+`>; rel="last"`)
			}
			items := make([]struct{}, minI(issuesCount, 1))
			_ = json.NewEncoder(w).Encode(items)
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":     "v1.2.3",
				"published_at": time.Now().Format(time.RFC3339),
			})
		case strings.Contains(r.URL.Path, "/actions/runs") || strings.Contains(r.URL.Path, "/check-runs"):
			runs := []map[string]any{}
			if conclusion != "" {
				runs = append(runs, map[string]any{
					"status":      "completed",
					"conclusion":  conclusion,
					"head_branch": "main",
					"updated_at":  time.Now().Format(time.RFC3339),
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count":   len(runs),
				"workflow_runs": runs,
				"check_runs":    runs,
			})
		case strings.HasPrefix(r.URL.Path, "/repos/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"full_name":         "o/r",
				"description":       "test repo",
				"language":          "Go",
				"open_issues_count": issuesCount,
				"pushed_at":         time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
				"updated_at":        time.Now().Format(time.RFC3339),
				"default_branch":    "main",
				"archived":          archived,
				"disabled":          false,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func newTestEnv(t *testing.T, prCount, issuesCount int, conclusion string, archived bool) (*Scanner, *registry.Registry, *httptest.Server) {
	t.Helper()
	srv := fakeGitHub(t, archived, prCount, issuesCount, conclusion)
	gh := github.NewClient(github.Config{
		Token:   "test-token",
		BaseURL: srv.URL,
		Timeout: 5 * time.Second,
	})

	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	s := New(Config{
		Interval: 30 * time.Second,
		GitHub:   gh,
		Registry: reg,
		Logger:   logging.Discard(),
	})
	return s, reg, srv
}

func sampleProj(slug string) *project.Project {
	return &project.Project{
		Slug:        slug,
		DisplayName: slug,
		Repository:  "github.com/o/r",
		Stage:       project.StageActive,
		Priority:    3,
		Notes:       "manual notes",
		Tags:        []string{"manual-tag"},
	}
}

func TestScanner_ScanAll_Empty(t *testing.T) {
	s, _, srv := newTestEnv(t, 0, 0, "", false)
	defer srv.Close()
	s.ScanAll(context.Background())
	// no panic, no projects to scan
}

func TestScanner_AfterScanHookRuns(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	s := New(Config{
		Interval: 30 * time.Second,
		Registry: reg,
		Logger:   logging.Discard(),
		AfterScan: func(context.Context) {
			calls++
		},
	})
	s.ScanAll(context.Background())
	if calls != 1 {
		t.Errorf("AfterScan should run once per ScanAll cycle, got %d", calls)
	}
	// a second cycle invokes it again
	s.ScanAll(context.Background())
	if calls != 2 {
		t.Errorf("AfterScan should run each cycle, got %d", calls)
	}
}

func TestScanner_NilAfterScanIsSafe(t *testing.T) {
	reg, _ := registry.New(t.TempDir())
	s := New(Config{Registry: reg, Logger: logging.Discard()}) // AfterScan nil
	s.ScanAll(context.Background())                            // must not panic
}

func TestScanner_ScanProject_UpdatesAutoFields(t *testing.T) {
	s, reg, srv := newTestEnv(t, 3, 7, "success", false)
	defer srv.Close()

	if err := reg.Add(sampleProj("p1")); err != nil {
		t.Fatal(err)
	}
	s.ScanAll(context.Background())

	got, err := reg.Get("p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestVersion == "" {
		t.Errorf("LatestVersion not updated: %q", got.LatestVersion)
	}
	if got.CIStatus == "" || got.CIStatus == project.CIStatusUnknown {
		t.Errorf("CIStatus not updated: %q", got.CIStatus)
	}
	// The fake repo omits "private" → public; scanner records observed visibility.
	if !got.RepoPublic {
		t.Errorf("RepoPublic should be true for a public repo, got %v", got.RepoPublic)
	}
}

func TestScanner_ScanProject_PreservesManualFields(t *testing.T) {
	s, reg, srv := newTestEnv(t, 1, 1, "success", false)
	defer srv.Close()

	if err := reg.Add(sampleProj("preserve")); err != nil {
		t.Fatal(err)
	}
	s.ScanAll(context.Background())

	got, _ := reg.Get("preserve")
	if got.Priority != 3 {
		t.Errorf("manual Priority overwritten: %d", got.Priority)
	}
	if got.Notes != "manual notes" {
		t.Errorf("manual Notes overwritten: %q", got.Notes)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "manual-tag" {
		t.Errorf("manual Tags overwritten: %v", got.Tags)
	}
}

func TestScanner_FailingCI(t *testing.T) {
	s, reg, srv := newTestEnv(t, 0, 0, "failure", false)
	defer srv.Close()

	if err := reg.Add(sampleProj("fail-ci")); err != nil {
		t.Fatal(err)
	}
	s.ScanAll(context.Background())

	got, _ := reg.Get("fail-ci")
	if got.CIStatus != project.CIStatusFailing {
		t.Errorf("CIStatus = %q, want failing", got.CIStatus)
	}
}

func TestScanner_SkipsNonScannable(t *testing.T) {
	s, reg, srv := newTestEnv(t, 99, 99, "success", false)
	defer srv.Close()

	// Pre-archive a project — scanner should skip it via IsScannable()
	p := sampleProj("skipme")
	p.Stage = project.StageArchived
	if err := reg.Add(p); err != nil {
		t.Fatal(err)
	}
	s.ScanAll(context.Background())

	got, _ := reg.Get("skipme")
	if got.LatestVersion != "" {
		t.Errorf("archived stage should be skipped, got ver=%q", got.LatestVersion)
	}
}

func TestScanner_Start_StopsOnContextCancel(t *testing.T) {
	s, _, srv := newTestEnv(t, 0, 0, "", false)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	s.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not stop within 2s of cancel")
	}
}

// concurrent counter for verifying max concurrency
type concMetrics struct {
	current atomic.Int32
	max     atomic.Int32
}

func (c *concMetrics) IncScanned() {
	n := c.current.Add(1)
	for {
		m := c.max.Load()
		if n <= m || c.max.CompareAndSwap(m, n) {
			break
		}
	}
}
func (c *concMetrics) IncFailed()                        {}
func (c *concMetrics) SetLastScanDuration(time.Duration) {}
func (c *concMetrics) SetLastScanAt(time.Time)           {}

// countMetrics tracks IncFailed calls for coverage of ScanAll failure branches.
type countMetrics struct {
	scanned atomic.Int32
	failed  atomic.Int32
}

func (m *countMetrics) IncScanned()                       { m.scanned.Add(1) }
func (m *countMetrics) IncFailed()                        { m.failed.Add(1) }
func (m *countMetrics) SetLastScanDuration(time.Duration) {}
func (m *countMetrics) SetLastScanAt(time.Time)           {}

// ─── helper / interface coverage ─────────────────────────────

func TestNoopMetrics(t *testing.T) {
	// 単純に呼出が panic しないことを保証
	var m noopMetrics
	m.IncScanned()
	m.IncFailed()
	m.SetLastScanDuration(time.Second)
	m.SetLastScanAt(time.Now())
}

func TestMapCIStatus_AllInputs(t *testing.T) {
	tests := map[string]project.CIStatus{
		"success":         project.CIStatusPassing,
		"SUCCESS":         project.CIStatusPassing,
		"Success":         project.CIStatusPassing,
		"failure":         project.CIStatusFailing,
		"timed_out":       project.CIStatusFailing,
		"startup_failure": project.CIStatusFailing,
		"action_required": project.CIStatusFailing,
		"":                project.CIStatusUnknown,
		"pending":         project.CIStatusUnknown,
		"queued":          project.CIStatusUnknown,
		"in_progress":     project.CIStatusUnknown,
		"skipped":         project.CIStatusUnknown,
		"cancelled":       project.CIStatusUnknown,
		"random_garbage":  project.CIStatusUnknown,
	}
	for in, want := range tests {
		if got := mapCIStatus(in); got != want {
			t.Errorf("mapCIStatus(%q): got %q want %q", in, got, want)
		}
	}
}

// ─── noopMetrics ─────────────────────────────────────────────

func TestNoopMetrics_AllMethodsNoop(t *testing.T) {
	m := noopMetrics{}
	m.IncScanned()
	m.IncFailed()
	m.SetLastScanDuration(time.Second)
	m.SetLastScanAt(time.Now())
	// Also exercise via New with nil Metrics (falls through to noopMetrics).
	dir := t.TempDir()
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Registry: reg, Metrics: nil})
	if s == nil {
		t.Error("New with nil Metrics should return a Scanner")
	}
}

// ─── Stop() branch in run() ───────────────────────────────────

// TestScanner_StopWithoutContextCancel exercises the stopCh branch of run()
// by calling Stop() directly without cancelling the background context first.
func TestScanner_StopWithoutContextCancel(t *testing.T) {
	s, _, srv := newTestEnv(t, 0, 0, "", false)
	defer srv.Close()

	// Use a background context that is never cancelled — Stop() must terminate.
	s.Start(context.Background())
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2s")
	}
}

// TestScanner_ScanAll_ContextCancelledDuringScan exercises the ctx.Done() branch
// inside the ScanAll project-iteration loop by cancelling the context immediately
// before ScanAll scans the registered project.
func TestScanner_ScanAll_ContextCancelledDuringScan(t *testing.T) {
	s, reg, srv := newTestEnv(t, 1, 1, "success", false)
	defer srv.Close()

	if err := reg.Add(sampleProj("ctx-cancel-test")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately before ScanAll starts iterating
	s.ScanAll(ctx)
	// Should complete without panic; the cancelled context exits the inner loop early.
}

// TestScanner_ScanAll_IncFailedAndMetrics exercises the scanOne-failure
// branch in ScanAll (IncFailed) via a GitHub server that returns 500 for the
// repository endpoint, making scanOne return an error.
func TestScanner_ScanAll_IncFailedAndMetrics(t *testing.T) {
	// Serve 500 for all requests to force scanOne to fail at GetRepository.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forced error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	gh := github.NewClient(github.Config{
		Token:   "test-token",
		BaseURL: srv.URL,
		Timeout: 2 * time.Second,
	})
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var failCount int32
	m := &countMetrics{}
	s := New(Config{
		Interval: 30 * time.Second,
		GitHub:   gh,
		Registry: reg,
		Metrics:  m,
		Logger:   logging.Discard(),
	})
	_ = failCount

	if err := reg.Add(sampleProj("fail-scan")); err != nil {
		t.Fatal(err)
	}
	s.ScanAll(context.Background())
	if m.failed.Load() == 0 {
		t.Error("expected IncFailed to be called at least once")
	}
}

// TestScanner_Run_TickerFires exercises the ticker.C branch in run() by using
// a very short interval so a second ScanAll fires naturally.
func TestScanner_Run_TickerFires(t *testing.T) {
	s, reg, srv := newTestEnv(t, 0, 0, "", false)
	defer srv.Close()

	if err := reg.Add(sampleProj("ticker-test")); err != nil {
		t.Fatal(err)
	}

	// Override the scanner interval to something tiny.
	s.interval = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	s.Start(ctx)
	// Wait for context to expire (at least one ticker fire must happen).
	<-ctx.Done()
	s.Stop()
}

func TestNoopMetrics_AllMethodsCovered(t *testing.T) {
	// noopMetrics is the default when Scanner is built without a Metrics option.
	// Calling each method confirms they are reachable and don't panic.
	m := noopMetrics{}
	m.IncScanned()
	m.IncFailed()
	m.SetLastScanDuration(0)
	m.SetLastScanAt(time.Time{})
}

// ─── scanOne error paths ──────────────────────────────────────

// TestScanOne_InvalidRepo covers the owner==""||repo=="" branch in scanOne.
func TestScanOne_InvalidRepo(t *testing.T) {
	s, _, srv := newTestEnv(t, 0, 0, "", false)
	defer srv.Close()

	p := &project.Project{
		Slug:        "bad-repo",
		DisplayName: "bad-repo",
		Repository:  "", // OwnerRepo() → ("", "")
		Stage:       project.StageActive,
	}
	err := s.scanOne(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "invalid repository") {
		t.Errorf("expected invalid-repository error, got %v", err)
	}
}

// TestScanOne_LatestReleaseFails covers the LatestRelease error fallback in scanOne.
// A 404 is treated as "no release" (no error), so we use 500 to force an error.
func TestScanOne_LatestReleaseFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			http.Error(w, "forced error", http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_ = json.NewEncoder(w).Encode([]struct{}{})
		case strings.HasSuffix(r.URL.Path, "/issues"):
			_ = json.NewEncoder(w).Encode([]struct{}{})
		case strings.Contains(r.URL.Path, "/actions/runs"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": 0, "workflow_runs": []any{},
			})
		case strings.HasPrefix(r.URL.Path, "/repos/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"full_name": "o/r", "default_branch": "main",
				"pushed_at": time.Now().Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	gh := github.NewClient(github.Config{Token: "test-token", BaseURL: srv.URL, Timeout: 2 * time.Second})
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Interval: 30 * time.Second, GitHub: gh, Registry: reg, Logger: logging.Discard()})

	p := sampleProj("release-fail")
	p.LatestVersion = "v0.9.0"
	if err := reg.Add(p); err != nil {
		t.Fatal(err)
	}

	if err := s.scanOne(context.Background(), p); err != nil {
		t.Errorf("scanOne should succeed with release fallback, got: %v", err)
	}
	got, _ := reg.Get("release-fail")
	if got.LatestVersion != "v0.9.0" {
		t.Errorf("LatestVersion fallback: want v0.9.0, got %q", got.LatestVersion)
	}
}

// TestScanOne_LatestCIStatusFails covers the LatestCIStatus error fallback in scanOne.
// A 404 is treated as "no CI" (no error), so we use 500 to force an error.
func TestScanOne_LatestCIStatusFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/actions/runs"):
			http.Error(w, "forced error", http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_ = json.NewEncoder(w).Encode([]struct{}{})
		case strings.HasSuffix(r.URL.Path, "/issues"):
			_ = json.NewEncoder(w).Encode([]struct{}{})
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.0.0", "published_at": time.Now().Format(time.RFC3339),
			})
		case strings.HasPrefix(r.URL.Path, "/repos/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"full_name": "o/r", "default_branch": "main",
				"pushed_at": time.Now().Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	gh := github.NewClient(github.Config{Token: "test-token", BaseURL: srv.URL, Timeout: 2 * time.Second})
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Interval: 30 * time.Second, GitHub: gh, Registry: reg, Logger: logging.Discard()})

	p := sampleProj("ci-fail")
	if err := reg.Add(p); err != nil {
		t.Fatal(err)
	}

	if err := s.scanOne(context.Background(), p); err != nil {
		t.Errorf("scanOne should succeed with CI fallback, got: %v", err)
	}
	got, _ := reg.Get("ci-fail")
	if got.CIStatus != project.CIStatusUnknown {
		t.Errorf("CIStatus fallback: want Unknown, got %q", got.CIStatus)
	}
}

// TestScanOne_RegistryGetFails covers the registry.Get failure path in scanOne.
func TestScanOne_RegistryGetFails(t *testing.T) {
	s, _, srv := newTestEnv(t, 0, 0, "success", false)
	defer srv.Close()

	// p is not registered → registry.Get("not-registered") → ErrNotFound
	p := &project.Project{
		Slug:        "not-registered",
		DisplayName: "not-registered",
		Repository:  "github.com/o/r",
		Stage:       project.StageActive,
	}
	err := s.scanOne(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "registry.Get during scan") {
		t.Errorf("expected registry.Get-during-scan error, got %v", err)
	}
}

// ─── SecurityScanner error-path tests ────────────────────────
// (mockScorecard and mockOSV are defined in security_test.go)

// newSecurityReg returns a Scanner backed by a fresh registry.
func newSecurityReg(t *testing.T) (*Scanner, *registry.Registry) {
	t.Helper()
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Interval: 30 * time.Second, Registry: reg, Logger: logging.Discard()}), reg
}

// addActiveProject adds a project with all required fields to the registry.
func addActiveProject(t *testing.T, reg *registry.Registry, slug, lang string) {
	t.Helper()
	p := &project.Project{
		Slug:        slug,
		DisplayName: slug,
		Repository:  "github.com/o/r",
		Language:    lang,
		Stage:       project.StageActive,
	}
	if err := reg.Add(p); err != nil {
		t.Fatalf("addActiveProject %s: %v", slug, err)
	}
}

// TestSecurityScanner_StopChBranch exercises the stopCh case inside run().
func TestSecurityScanner_StopChBranch(t *testing.T) {
	s, _ := newSecurityReg(t)
	ss := s.NewSecurityScanner(
		nil,
		&mockScorecard{scoreFor: map[string]*scorecard.Score{}},
		24*time.Hour,
	)
	ss.Start(context.Background())

	done := make(chan struct{})
	go func() {
		s.Stop() // closes stopCh → run() exits via stopCh case
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SecurityScanner did not stop within 3s after Stop()")
	}
}

// TestSecurityScanner_TickerBranch exercises the ticker.C case inside run().
func TestSecurityScanner_TickerBranch(t *testing.T) {
	s, _ := newSecurityReg(t)
	ss := s.NewSecurityScanner(
		nil,
		&mockScorecard{scoreFor: map[string]*scorecard.Score{}},
		10*time.Millisecond, // short interval → ticker fires quickly
	)
	ss.pause = 0

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ss.Start(ctx)
	<-ctx.Done()
	s.Stop()
}

// TestSecurityScanner_RunOnce_ArchiveSkip covers the archived-project continue in runOnce.
func TestSecurityScanner_RunOnce_ArchiveSkip(t *testing.T) {
	s, reg := newSecurityReg(t)
	sc := &mockScorecard{scoreFor: map[string]*scorecard.Score{}}
	ss := s.NewSecurityScanner(nil, sc, time.Hour)
	ss.pause = 0

	// Must include DisplayName so registry.Add succeeds (Validate requires it).
	arch := &project.Project{
		Slug:        "arch",
		DisplayName: "arch",
		Repository:  "github.com/o/r",
		Stage:       project.StageArchived,
	}
	if err := reg.Add(arch); err != nil {
		t.Fatalf("add archived: %v", err)
	}
	ss.runOnce(context.Background())
	if sc.calls != 0 {
		t.Errorf("scorecard.Fetch should not be called for archived project, got %d calls", sc.calls)
	}
}

// TestSecurityScanner_RunOnce_CtxCancelled covers ctx.Done() in runOnce's inner select.
func TestSecurityScanner_RunOnce_CtxCancelled(t *testing.T) {
	s, reg := newSecurityReg(t)
	sc := &mockScorecard{scoreFor: map[string]*scorecard.Score{}}
	ss := s.NewSecurityScanner(nil, sc, time.Hour)
	ss.pause = 0

	addActiveProject(t, reg, "active", "Go")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before runOnce → inner select fires ctx.Done() immediately
	ss.runOnce(ctx) // must return quickly, not panic
}

// TestScanScorecard_EmptyRepository covers the p.Repository=="" guard in scanScorecard.
func TestScanScorecard_EmptyRepository(t *testing.T) {
	s, _ := newSecurityReg(t)
	sc := &mockScorecard{scoreFor: map[string]*scorecard.Score{}}
	ss := s.NewSecurityScanner(nil, sc, time.Hour)

	p := &project.Project{Slug: "empty-repo", DisplayName: "empty-repo", Repository: ""}
	if got := ss.scanScorecard(context.Background(), p); got {
		t.Error("scanScorecard should return false when Repository is empty")
	}
}

// TestScanScorecard_RegistryGetFails covers the registry.Get failure in scanScorecard.
func TestScanScorecard_RegistryGetFails(t *testing.T) {
	s, _ := newSecurityReg(t)
	// scoreFor has a score for the repo, but the slug is not in the registry.
	sc := &mockScorecard{
		scoreFor: map[string]*scorecard.Score{
			"github.com/o/r": {Score: 7.0},
		},
	}
	ss := s.NewSecurityScanner(nil, sc, time.Hour)

	// Slug "ghost" is not in the registry → Get inside scanScorecard fails.
	p := &project.Project{Slug: "ghost", DisplayName: "ghost", Repository: "github.com/o/r"}
	if got := ss.scanScorecard(context.Background(), p); got {
		t.Error("scanScorecard should return false when registry.Get fails")
	}
}

// TestScanVulns_NoEcosystem covers the ecosystem=="" early return in scanVulns.
func TestScanVulns_NoEcosystem(t *testing.T) {
	s, _ := newSecurityReg(t)
	o := &mockOSV{} // must not be called
	ss := s.NewSecurityScanner(o, nil, time.Hour)

	// Language="" → LanguageToEcosystem("") → "" → return false immediately.
	p := &project.Project{Slug: "no-eco", DisplayName: "no-eco", Repository: "github.com/o/r", Language: ""}
	if got := ss.scanVulns(context.Background(), p); got {
		t.Error("scanVulns should return false when ecosystem is empty")
	}
	if o.calls != 0 {
		t.Errorf("osv.Query should not be called, got %d", o.calls)
	}
}

// TestScanVulns_RegistryGetFails covers the registry.Get failure in scanVulns.
func TestScanVulns_RegistryGetFails(t *testing.T) {
	s, _ := newSecurityReg(t)
	// Query returns no vulns, but "ghost-vuln" is not in the registry.
	o := &mockOSV{vulnsFor: map[string][]osv.Vuln{}}
	ss := s.NewSecurityScanner(o, nil, time.Hour)

	p := &project.Project{Slug: "ghost-vuln", DisplayName: "ghost-vuln", Repository: "github.com/o/r", Language: "Go"}
	if got := ss.scanVulns(context.Background(), p); got {
		t.Error("scanVulns should return false when registry.Get fails")
	}
}

// TestScanVulns_HighSeverityLog covers the critical+high warn log path in scanVulns.
func TestScanVulns_HighSeverityLog(t *testing.T) {
	s, reg := newSecurityReg(t)
	o := &mockOSV{
		vulnsFor: map[string][]osv.Vuln{
			"Go|github.com/o/r": {
				{ID: "CVE-2025-0001", Severity: osv.SeverityCritical},
				{ID: "CVE-2025-0002", Severity: osv.SeverityHigh},
			},
		},
	}
	ss := s.NewSecurityScanner(o, nil, time.Hour)

	addActiveProject(t, reg, "high-vuln", "Go")
	p, _ := reg.Get("high-vuln")
	if !ss.scanVulns(context.Background(), p) {
		t.Error("scanVulns should return true on success")
	}
	got, _ := reg.Get("high-vuln")
	if got.VulnCritical != 1 || got.VulnHigh != 1 {
		t.Errorf("vuln counts: critical=%d high=%d, want 1,1", got.VulnCritical, got.VulnHigh)
	}
}

// Suppress unused-import errors: errors is used in scanOne tests above.
var _ = errors.New
