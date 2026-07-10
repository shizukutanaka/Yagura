// security.go: security health (Scorecard + OSV) の定期 scan。
//
// 設計判断:
//   - GitHub scan (5 分) と独立した goroutine で動作
//   - 既定 interval: 24 時間(OSV/Scorecard は変化が遅い)
//   - 1 プロジェクトずつ順次処理し、各呼出間に 1s sleep
//     (OSV.dev / api.scorecard.dev の rate limit 余裕を持たせる)
//   - Scorecard / OSV のどちらかが失敗しても他方は続行(graceful degradation)
//   - 取得失敗時は既存値を保持(古いデータでもないよりまし)
package scanner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shizukutanaka/yagura/internal/osv"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/scorecard"
)

// OSVQuerier は OSV.dev クライアントのインターフェース(テスト差替え用)。
type OSVQuerier interface {
	Query(ctx context.Context, ecosystem, pkg, version string) ([]osv.Vuln, error)
}

// ScorecardFetcher は Scorecard クライアントのインターフェース。
type ScorecardFetcher interface {
	Fetch(ctx context.Context, repo string) (*scorecard.Score, error)
}

// SecurityScanner は portfolio 全体に対して Scorecard + OSV を定期取得する。
type SecurityScanner struct {
	parent    *Scanner
	osv       OSVQuerier
	scorecard ScorecardFetcher
	interval  time.Duration
	pause     time.Duration // 各 project 間の sleep(rate limit 緩和)
}

// NewSecurityScanner は SecurityScanner を生成する。
//
// osv / scorecard が nil の場合、その種類の scan はスキップされる
// (例: osv のみ有効、scorecard 無効 もあり得る)。
func (s *Scanner) NewSecurityScanner(o OSVQuerier, sc ScorecardFetcher, interval time.Duration) *SecurityScanner {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &SecurityScanner{
		parent:    s,
		osv:       o,
		scorecard: sc,
		interval:  interval,
		pause:     1 * time.Second,
	}
}

// Start は SecurityScanner を別 goroutine で起動する。
// ctx.Done() で停止する。Scanner.Stop() からは呼ばれない(別ライフサイクル)。
func (ss *SecurityScanner) Start(ctx context.Context) {
	if ss.osv == nil && ss.scorecard == nil {
		ss.parent.logger.Info("security scanner disabled (no clients)")
		return
	}
	ss.parent.wg.Add(1)
	go ss.run(ctx)
}

func (ss *SecurityScanner) run(ctx context.Context) {
	defer ss.parent.wg.Done()
	ss.parent.logger.Info("security scanner started",
		"interval", ss.interval,
		"osv_enabled", ss.osv != nil,
		"scorecard_enabled", ss.scorecard != nil)

	// 起動時に 1 回 fire(初回 data 取得)、その後 interval ごと
	runSafely(ss.parent.logger, "security-scanner-run-once", func() { ss.runOnce(ctx) })

	t := time.NewTicker(ss.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ss.parent.stopCh:
			return
		case <-t.C:
			runSafely(ss.parent.logger, "security-scanner-run-once", func() { ss.runOnce(ctx) })
		}
	}
}

// runOnce はポートフォリオ全プロジェクトに対して 1 サイクル scan を実行する。
//
// 失敗は WARN レベルでログ、registry 更新は per-project commit
// (1 つ失敗しても他は反映される)。
func (ss *SecurityScanner) runOnce(ctx context.Context) {
	projects := ss.parent.registry.List()
	start := time.Now()
	var scoredOK, vulnsOK int

	for i := range projects {
		p := projects[i]
		// archived は scan しない(更新する意味がない)
		if p.Stage == project.StageArchived {
			continue
		}
		// scan 1 件 / sleep / 次へ
		select {
		case <-ctx.Done():
			return
		default:
		}

		if ss.scorecard != nil {
			if updated := ss.scanScorecard(ctx, p); updated {
				scoredOK++
			}
		}
		if ss.osv != nil {
			if updated := ss.scanVulns(ctx, p); updated {
				vulnsOK++
			}
		}

		// 同一サイクル内で次の project に進む前に rate limit 緩和
		if ss.pause > 0 && i < len(projects)-1 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(ss.pause):
			}
		}
	}
	ss.parent.logger.Info("security scan cycle done",
		"projects", len(projects),
		"scorecard_updated", scoredOK,
		"vulns_updated", vulnsOK,
		"duration", time.Since(start))
}

// scanScorecard は 1 プロジェクトの Scorecard を取得して registry に反映する。
// 戻り値は更新が成功したか。
func (ss *SecurityScanner) scanScorecard(ctx context.Context, p *project.Project) bool {
	if p.Repository == "" {
		return false
	}
	// 短い per-project タイムアウトで全体の進行を保証
	ctxScan, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	score, err := ss.scorecard.Fetch(ctxScan, p.Repository)
	if err != nil {
		if errors.Is(err, scorecard.ErrNotScored) {
			ss.parent.logger.Debug("scorecard not scored yet", "slug", p.Slug)
		} else {
			ss.parent.logger.Warn("scorecard fetch failed",
				"slug", p.Slug, "repo", p.Repository, "err", err)
		}
		return false
	}
	// 取得成功 → 既存値を mutate して save
	cur, err := ss.parent.registry.Get(p.Slug)
	if err != nil {
		return false
	}
	cur.ScorecardScore = score.Score
	cur.ScorecardAt = time.Now().UTC()
	if err := ss.parent.registry.Update(cur); err != nil {
		ss.parent.logger.Warn("registry update failed for scorecard",
			"slug", p.Slug, "err", err)
		return false
	}
	return true
}

// scanVulns は 1 プロジェクトの脆弱性を OSV から取得し、severity 別の件数を保存する。
func (ss *SecurityScanner) scanVulns(ctx context.Context, p *project.Project) bool {
	ecosystem := osv.LanguageToEcosystem(p.Language)
	pkg := p.Repository
	if ecosystem == "" || pkg == "" {
		// 言語不明 or repository 未設定では scan できない
		return false
	}

	ctxScan, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// version は LatestVersion が分かれば指定、不明なら空(=パッケージ全体)
	version := strings.TrimSpace(p.LatestVersion)
	vulns, err := ss.osv.Query(ctxScan, ecosystem, pkg, version)
	if err != nil {
		ss.parent.logger.Warn("osv query failed",
			"slug", p.Slug, "pkg", pkg, "err", err)
		return false
	}

	// severity 別に集計
	var critical, high, medium, low int
	for _, v := range vulns {
		switch v.Severity {
		case osv.SeverityCritical:
			critical++
		case osv.SeverityHigh:
			high++
		case osv.SeverityMedium:
			medium++
		case osv.SeverityLow:
			low++
		}
	}

	cur, err := ss.parent.registry.Get(p.Slug)
	if err != nil {
		return false
	}
	cur.VulnCritical = critical
	cur.VulnHigh = high
	cur.VulnMedium = medium
	cur.VulnLow = low
	cur.VulnScanAt = time.Now().UTC()
	if err := ss.parent.registry.Update(cur); err != nil {
		ss.parent.logger.Warn("registry update failed for vulns",
			"slug", p.Slug, "err", err)
		return false
	}
	if critical+high > 0 {
		ss.parent.logger.Warn("high-severity vulns detected",
			"slug", p.Slug,
			"critical", critical, "high", high,
			"summary", summarizeTopVulns(vulns, 3))
	}
	return true
}

// summarizeTopVulns は最重要 N 件の ID + severity をログ用に要約する。
func summarizeTopVulns(vulns []osv.Vuln, n int) string {
	if n > len(vulns) {
		n = len(vulns)
	}
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("%s(%s)", vulns[i].ID, vulns[i].Severity))
	}
	return strings.Join(parts, ", ")
}
