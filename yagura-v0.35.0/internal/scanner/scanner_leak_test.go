package scanner

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/github"
	"github.com/shizukutanaka/yagura/internal/registry"
)

// TestScanner_NoGoroutineLeak は scanner が context cancellation 後に
// すべての goroutine を確実に終了することを確認する。
//
// uber-go/goleak の手法を zero-dep で再実装(ADR-0001 準拠):
//  1. baseline goroutine 数を記録
//  2. scanner を 10 サイクル起動・停止
//  3. GC + 安定化待機後の数を再計測
//  4. baseline+3 以上の差があれば leak と判定
//
// 24/7 daemon を意図するため、たとえ 1 サイクル 1 goroutine leak でも
// 1 年で大量に積み上がる。zero-tolerance で検証する。
func TestScanner_NoGoroutineLeak(t *testing.T) {
	stabilizeGoroutines()
	baseline := runtime.NumGoroutine()

	for i := 0; i < 10; i++ {
		runScanner(t)
	}

	stabilizeGoroutines()
	after := runtime.NumGoroutine()

	// 1-3 個の差異は許容(GC やテストランナーの揺らぎ)
	if after > baseline+3 {
		t.Errorf("scanner goroutine leak: baseline=%d, after=%d (diff=%d)",
			baseline, after, after-baseline)
	}
}

// TestSecurityScanner_NoGoroutineLeak は security scanner の leak を検証。
func TestSecurityScanner_NoGoroutineLeak(t *testing.T) {
	stabilizeGoroutines()
	baseline := runtime.NumGoroutine()

	for i := 0; i < 10; i++ {
		runSecurityScanner(t)
	}

	stabilizeGoroutines()
	after := runtime.NumGoroutine()

	if after > baseline+3 {
		t.Errorf("security scanner goroutine leak: baseline=%d, after=%d (diff=%d)",
			baseline, after, after-baseline)
	}
}

// ─── helpers (zero-dep goleak風) ─────────────────────────────

// stabilizeGoroutines は GC 通過 + 待機で goroutine 数を安定化させる。
func stabilizeGoroutines() {
	runtime.GC()
	runtime.GC()
	time.Sleep(80 * time.Millisecond)
}

// runScanner は 1 サイクル scanner を起動 → cancel → 終了待ち。
func runScanner(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s := New(Config{
		Registry: reg,
		GitHub:   nil, // 起動して即 cancel するので外部呼び出しに到達しない
		Logger:   log,
		Interval: time.Hour, // 長いので tick 発火前に cancel
	})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Start(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	wg.Wait()
}

func runSecurityScanner(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	parent := New(Config{Registry: reg, Logger: log, Interval: time.Hour})
	ss := parent.NewSecurityScanner(nil, nil, time.Hour) // 起動 + 即 cancel
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ss.Start(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	wg.Wait()
}

// 上記 helper で使う github の型を一応 alias (linter avoidance)
var _ = github.NewClient
