// ratelimit.go: GitHub API rate-limit aware pin checking (v0.12.0)
//
// 動機:
//   v0.11 の CheckPinsParallel は API rate limit を見ずに走るため、
//   5000+ pin を含む portfolio audit で:
//     1. 認証付き 5000 req/h の枠を burst 8 req/sec で消費しきる
//     2. 429 / X-RateLimit-Remaining=0 を受け取っても無視して走り続ける
//     3. 残り pin 全てが UNVERIFIABLE で返る
//
// 解決:
//   - 各 API call の前に rate limit 残量をチェック
//   - 閾値 (MinRemaining, デフォルト 100) を下回ったら、reset 時刻まで sleep
//   - context cancellation を尊重(sleep 中も中断可能)
//   - MaxSleep でキャップ(secondary rate limit の場合は短時間で再試行)
//
// 設計判断:
//   - sleep は単純な time.After で、exponential backoff は使わない
//     (GitHub の rate limit は時間ベースで明確なので)
//   - rate limit 情報の取得は Client.LastRateLimit() に依存(呼出後の最新値)
//   - Guard は optional: Checker.RateLimit が nil なら従来動作

package pindrift

import (
	"context"
	"sync"
	"time"

	"github.com/shizukutanaka/yagura/internal/github"
)

// RateLimitGuard は API 残量を見て pause 判断を下す。
//
// Wait() を毎 API call 前に呼ぶことで、残量 < MinRemaining なら reset まで sleep。
// 並行安全: 内部 mutex で sleep 中の重複待機を防ぐ。
type RateLimitGuard struct {
	// Source は最新の rate limit を返す(Client.LastRateLimit を渡す想定)
	Source func() github.RateLimit

	// MinRemaining 未満で sleep を開始(デフォルト 100)
	// portfolio audit を完走するための余裕を確保。
	MinRemaining int

	// MaxSleep は 1 回の sleep の最長(デフォルト 60 秒)
	// secondary rate limit (短期 burst 防止) で reset 時刻が遠い場合のキャップ。
	MaxSleep time.Duration

	// Clock はテスト用時計 hook (nil なら time.Now)
	Clock func() time.Time

	// Sleeper はテスト用 sleep hook (nil なら time.After ベース)
	Sleeper func(ctx context.Context, d time.Duration) error

	// 観測値
	mu         sync.Mutex
	totalWaits int
	totalSleep time.Duration
}

// NewRateLimitGuard は標準値で Guard を生成する。
func NewRateLimitGuard(source func() github.RateLimit) *RateLimitGuard {
	return &RateLimitGuard{
		Source:       source,
		MinRemaining: 100,
		MaxSleep:     60 * time.Second,
	}
}

// Wait は必要なら sleep して資源を待つ。context cancel で中断可能。
func (g *RateLimitGuard) Wait(ctx context.Context) error {
	rl := g.Source()
	if rl.Limit == 0 {
		// 初回(API call 前)は rate limit 情報が空 → pass
		return nil
	}
	if rl.Remaining >= g.minRemaining() {
		return nil
	}

	// 残量不足 → reset まで待つ
	now := g.now()
	wait := rl.Reset.Sub(now)
	if wait <= 0 {
		// reset 時刻が過去(時計ずれ?)→ 短い grace period
		wait = 5 * time.Second
	}
	if wait > g.maxSleep() {
		wait = g.maxSleep()
	}

	// 観測値更新
	g.mu.Lock()
	g.totalWaits++
	g.totalSleep += wait
	g.mu.Unlock()

	return g.sleep(ctx, wait)
}

// Stats は guard の累積待機統計を返す(observability 用)。
func (g *RateLimitGuard) Stats() (waits int, total time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.totalWaits, g.totalSleep
}

func (g *RateLimitGuard) minRemaining() int {
	if g.MinRemaining <= 0 {
		return 100
	}
	return g.MinRemaining
}

func (g *RateLimitGuard) maxSleep() time.Duration {
	if g.MaxSleep <= 0 {
		return 60 * time.Second
	}
	return g.MaxSleep
}

func (g *RateLimitGuard) now() time.Time {
	if g.Clock != nil {
		return g.Clock()
	}
	return time.Now()
}

func (g *RateLimitGuard) sleep(ctx context.Context, d time.Duration) error {
	if g.Sleeper != nil {
		return g.Sleeper(ctx, d)
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
