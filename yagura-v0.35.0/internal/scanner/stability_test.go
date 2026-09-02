package scanner

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/registry"
)

// TestScanner_LongRunningStability は scanner を 1000 cycle 起動・停止して
// メモリと goroutine の累積 leak がないことを検証する。
//
// 24/7 daemon を想定すると、1 cycle あたり 1 KB の leak でも 1 年で大きな
// 量になる。HeapAlloc の差分を 1 MB 以内、goroutine 数の差分を ±5 以内
// に収めることをゴールとする。
//
// Go runtime の MADV_FREE 動作のため Sys は減らないが、HeapAlloc は GC
// 通過で再利用領域として扱われる。HeapAlloc で leak 判定する。
//
// 走行時間目安: 数秒〜10 秒(short モードでスキップ可)。
func TestScanner_LongRunningStability(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-running stability test in -short mode")
	}

	const cycles = 1000
	const goroutineTolerance = 5
	const heapToleranceBytes = 1 << 20 // 1 MiB

	// baseline
	stabilizeRuntime()
	baseGoroutines := runtime.NumGoroutine()
	var baseStats runtime.MemStats
	runtime.ReadMemStats(&baseStats)

	// 1000 cycle
	dir := t.TempDir()
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s := New(Config{Registry: reg, Logger: log, Interval: 24 * time.Hour})

	for i := 0; i < cycles; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Start(ctx)
		}()
		// 一瞬走らせて止める(スループット最適化、interval は触れない)
		cancel()
		wg.Wait()
	}

	// final 数値(GC 通過後)
	stabilizeRuntime()
	endGoroutines := runtime.NumGoroutine()
	var endStats runtime.MemStats
	runtime.ReadMemStats(&endStats)

	// 検証 1: goroutine 数の差分
	goroutineDiff := endGoroutines - baseGoroutines
	if goroutineDiff > goroutineTolerance {
		t.Errorf("goroutine leak across %d cycles: base=%d end=%d (diff=%d)",
			cycles, baseGoroutines, endGoroutines, goroutineDiff)
	}

	// 検証 2: HeapAlloc 差分
	// 注: HeapAlloc は signed 計算が必要(GC で base より低くなることもある)
	var heapDiff int64
	if endStats.HeapAlloc > baseStats.HeapAlloc {
		heapDiff = int64(endStats.HeapAlloc - baseStats.HeapAlloc)
	} else {
		heapDiff = -int64(baseStats.HeapAlloc - endStats.HeapAlloc)
	}
	if heapDiff > int64(heapToleranceBytes) {
		t.Errorf("heap leak across %d cycles: HeapAlloc diff=+%d bytes (tolerance=%d)",
			cycles, heapDiff, heapToleranceBytes)
	}

	// 検証 3: GC が回って実際に解放が起きていることの確認
	if endStats.NumGC <= baseStats.NumGC {
		t.Logf("WARN: no GC cycles ran during stability test (cycles=%d → NumGC %d → %d)",
			cycles, baseStats.NumGC, endStats.NumGC)
	}

	// 観測値をログに残す(failしなくても診断用)
	t.Logf("stability after %d cycles:", cycles)
	t.Logf("  goroutines: %d → %d (Δ=%+d, tol=±%d)",
		baseGoroutines, endGoroutines, goroutineDiff, goroutineTolerance)
	t.Logf("  HeapAlloc:  %d → %d bytes (Δ=%+d, tol=±%d)",
		baseStats.HeapAlloc, endStats.HeapAlloc, heapDiff, heapToleranceBytes)
	t.Logf("  HeapInuse:  %d → %d bytes", baseStats.HeapInuse, endStats.HeapInuse)
	t.Logf("  TotalAlloc: %d → %d bytes (Δ=+%d)",
		baseStats.TotalAlloc, endStats.TotalAlloc, endStats.TotalAlloc-baseStats.TotalAlloc)
	t.Logf("  NumGC:      %d → %d (Δ=%+d)",
		baseStats.NumGC, endStats.NumGC, endStats.NumGC-baseStats.NumGC)
}

// stabilizeRuntime は GC 通過 + 短い待機で計測値を安定化させる。
// 2 回 GC を回すのは、Go runtime がデファード処理を確実に完了させるため。
func stabilizeRuntime() {
	runtime.GC()
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
}
