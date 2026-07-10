// Package scanner はプロジェクトの自動取得フィールドを GitHub から定期更新する。
//
// 設計判断:
//   - 1 プロジェクトあたり 4 リクエスト(repo + open PRs + open issues + CI run)
//   - 23 プロジェクト x 4 req = 92 req / 1 scan サイクル
//   - PAT の rate limit 5000/h なので 5 分間隔なら余裕(92 x 12 = 1104 req/h)
//   - エラーが起きても他のプロジェクトの scan は続行
//   - 重要: scanner は手動管理フィールド(Priority/Notes/Tags/DependsOn)を上書きしない
package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/shizukutanaka/yagura/internal/github"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/registry"
)

// Metrics は scanner が emit するメトリクスのインターフェース。
type Metrics interface {
	IncScanned()
	IncFailed()
	SetLastScanDuration(d time.Duration)
	SetLastScanAt(t time.Time)
}

type noopMetrics struct{}

func (noopMetrics) IncScanned()                       {}
func (noopMetrics) IncFailed()                        {}
func (noopMetrics) SetLastScanDuration(time.Duration) {}
func (noopMetrics) SetLastScanAt(time.Time)           {}

// Scanner は定期的に GitHub をポーリングして registry を更新する。
type Scanner struct {
	registry    *registry.Registry
	gh          *github.Client
	logger      *slog.Logger
	metrics     Metrics
	interval    time.Duration
	scanTimeout time.Duration
	afterScan   func(context.Context)

	wg     sync.WaitGroup
	stopCh chan struct{}
	once   sync.Once
}

// Config は Scanner の初期設定。
type Config struct {
	Registry    *registry.Registry
	GitHub      *github.Client
	Logger      *slog.Logger
	Metrics     Metrics
	Interval    time.Duration
	ScanTimeout time.Duration
	// AfterScan, if set, is invoked at the end of each ScanAll cycle that runs to
	// completion (after metrics are recorded). It is skipped when the cycle returns
	// early due to ctx cancellation — i.e. at shutdown — since there is no point
	// refreshing derived state on the way out. The daemon uses it for a periodic
	// alert_fix health sweep. Optional; nil disables it.
	AfterScan func(context.Context)
}

// New は Config に基づいて Scanner を作る。Interval/ScanTimeout が 0 なら
// それぞれ 5 分/30 秒をデフォルトとして使う。
func New(c Config) *Scanner {
	if c.Interval == 0 {
		c.Interval = 5 * time.Minute
	}
	if c.ScanTimeout == 0 {
		c.ScanTimeout = 30 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Metrics == nil {
		c.Metrics = noopMetrics{}
	}
	return &Scanner{
		registry:    c.Registry,
		gh:          c.GitHub,
		logger:      c.Logger,
		metrics:     c.Metrics,
		interval:    c.Interval,
		scanTimeout: c.ScanTimeout,
		afterScan:   c.AfterScan,
		stopCh:      make(chan struct{}),
	}
}

// Start は scan ループを goroutine で起動する。初回 scan は即時実行。
func (s *Scanner) Start(ctx context.Context) {
	s.wg.Add(1)
	go s.run(ctx)
}

// Stop は scan ループを停止し、完了を待つ。複数回呼出は安全。
func (s *Scanner) Stop() {
	s.once.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

func (s *Scanner) run(ctx context.Context) {
	defer s.wg.Done()
	runSafely(s.logger, "scanner-scan-all", func() { s.ScanAll(ctx) })
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			runSafely(s.logger, "scanner-scan-all", func() { s.ScanAll(ctx) })
		}
	}
}

// ScanAll はすべての scannable プロジェクトを順次 scan する。
func (s *Scanner) ScanAll(ctx context.Context) {
	start := time.Now()
	defer func() {
		s.metrics.SetLastScanDuration(time.Since(start))
		s.metrics.SetLastScanAt(time.Now().UTC())
	}()

	projects := s.registry.Filter(func(p *project.Project) bool {
		return p.IsScannable()
	})
	s.logger.Info("scanner starting cycle", "count", len(projects))

	scanned, failed := 0, 0
	for _, p := range projects {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := s.scanOne(ctx, p); err != nil {
			s.logger.Warn("scan failed",
				"slug", p.Slug, "repository", p.Repository, "error", err)
			s.metrics.IncFailed()
			failed++
			continue
		}
		scanned++
		s.metrics.IncScanned()
	}
	s.logger.Info("scanner cycle done",
		"scanned", scanned, "failed", failed,
		"duration", time.Since(start).Round(time.Millisecond))

	// v0.35: post-scan hook. The daemon wires this to run an alert_fix health
	// sweep over freshly-scanned sensor data (Scanner ↔ alert_fix periodic loop).
	// Kept as a generic callback so the scanner stays decoupled from alertfix.
	if s.afterScan != nil {
		s.afterScan(ctx)
	}
}

func (s *Scanner) scanOne(parentCtx context.Context, p *project.Project) error {
	ctx, cancel := context.WithTimeout(parentCtx, s.scanTimeout)
	defer cancel()

	owner, repo := p.OwnerRepo()
	if owner == "" || repo == "" {
		return fmt.Errorf("invalid repository: %s", p.Repository)
	}

	r, err := s.gh.GetRepository(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("repo: %w", err)
	}

	openPRs, openIssues, err := s.gh.CountOpenItems(ctx, owner, repo)
	if err != nil {
		s.logger.Debug("count failed", "slug", p.Slug, "error", err)
		openPRs, openIssues = p.OpenPRs, p.OpenIssues
	}

	tag, err := s.gh.LatestRelease(ctx, owner, repo)
	if err != nil {
		s.logger.Debug("release failed", "slug", p.Slug, "error", err)
		tag = p.LatestVersion
	}

	ciStatus, err := s.gh.LatestCIStatus(ctx, owner, repo, r.DefaultBranch)
	if err != nil {
		s.logger.Debug("ci failed", "slug", p.Slug, "error", err)
		ciStatus = ""
	}

	current, err := s.registry.Get(p.Slug)
	if err != nil {
		return fmt.Errorf("registry.Get during scan: %w", err)
	}

	current.LatestVersion = tag
	current.LatestActivity = r.PushedAt
	current.OpenPRs = openPRs
	current.OpenIssues = openIssues
	current.StarCount = r.StargazersCount
	current.RepoPublic = !r.Private // observed visibility (sensor; drives the visibility-mismatch alert)
	current.CIStatus = mapCIStatus(ciStatus)
	if current.Language == "" && r.Language != "" {
		current.Language = r.Language
	}

	return s.registry.Update(current)
}

func mapCIStatus(s string) project.CIStatus {
	switch strings.ToLower(s) {
	case "success":
		return project.CIStatusPassing
	case "failure", "timed_out", "startup_failure", "action_required":
		return project.CIStatusFailing
	default:
		return project.CIStatusUnknown
	}
}
