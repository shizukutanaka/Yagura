package lens_test

// capsweep_test は `srcfiles` の走査上限を **測って決める** ためのハーネス。
//
// 経緯: 上限 1,000 は v0.118.0 以来「安全のため」の定数で、根拠となる測定が
// 無かった。v1.83.0 で kubernetes を測ったとき 13,424 ファイル中 1,000
// (7.4%)しか読めておらず、応答は `incomplete: true` の一語だった。
// v1.86.0 で `files_read`/`files_total` を出して **見えるように**はしたが、
// 上限そのものは「上げるべき値を決める根拠がまだ無い」として据え置いた。
// これはその根拠を作るための測定である。
//
// 既定では skip する(`go test ./...` のコストを 0 にする):
//
//	YAGURA_CAP_DIR=/path/to/big/repo go test ./internal/lens/ -run TestCapSweep -v

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/lens"
	"github.com/shizukutanaka/yagura/internal/srcfiles"
)

func TestCapSweep(t *testing.T) {
	dir := os.Getenv("YAGURA_CAP_DIR")
	if dir == "" {
		t.Skip("set YAGURA_CAP_DIR to a large repository to run the sweep")
	}
	accept := func(n string) bool { return strings.HasSuffix(n, ".go") }

	for _, cap := range []int{1000, 2500, 5000, 10000, 25000} {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)

		t0 := time.Now()
		res, err := srcfiles.ReadLimited(dir, cap, srcfiles.DefaultMaxBytes, accept)
		readMS := time.Since(t0).Milliseconds()
		if err != nil {
			t.Fatalf("cap=%d: %v", cap, err)
		}

		t1 := time.Now()
		out := lens.RunAll(res.Files, lens.Options{})
		runMS := time.Since(t1).Milliseconds()

		runtime.ReadMemStats(&m1)
		heapMB := float64(m1.HeapAlloc-m0.HeapAlloc) / (1 << 20)
		if m1.HeapAlloc < m0.HeapAlloc {
			heapMB = float64(m1.HeapAlloc) / (1 << 20)
		}

		bytes := 0
		for _, c := range res.Files {
			bytes += len(c)
		}
		findings := 0
		for _, r := range out {
			findings += r.Findings
		}
		fmt.Fprintf(os.Stderr,
			"cap=%-6d read=%-5d/%-6d src=%-6.1fMB readMS=%-6d runAllMS=%-7d heap=%-7.1fMB findings=%d truncated=%v\n",
			cap, len(res.Files), res.Matched, float64(bytes)/(1<<20), readMS, runMS, heapMB, findings, res.Truncated)
	}
}
