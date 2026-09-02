package walkforward_test

// largeapp_test は **大規模アプリケーションでの再計測プロトコル** を実行可能な形で
// 残す。docs/MULTIREPO_FINDINGS.md の数表はこのテストの出力そのものである。
//
// なぜテストとして残すのか: v1.82.0 の再計測は使い捨てのスクリプトで行い、
// 手順が散文にしか残らなかった。数値を疑う人が再実行できないなら、それは
// 主張であって証拠ではない。既定では YAGURA_LARGE_DIR が無いので skip し、
// `go test ./...` のコストは 0。
//
//	git clone --depth 3000 --single-branch https://github.com/prometheus/prometheus /tmp/prom
//	YAGURA_LARGE_DIR=/tmp/prom go test ./internal/walkforward/ -run TestLargeApp -v
//
// **--filter=blob:none を使ってはならない**: partial clone では --numstat が blob を
// 1 つずつ取りに行き、git log が 60 秒の上限を超える(この罠に一度落ちている)。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/churn"
	"github.com/shizukutanaka/yagura/internal/cochange"
	"github.com/shizukutanaka/yagura/internal/complexity"
	"github.com/shizukutanaka/yagura/internal/srcfiles"
	"github.com/shizukutanaka/yagura/internal/walkforward"
)

func TestLargeApp(t *testing.T) {
	dir := os.Getenv("YAGURA_LARGE_DIR")
	if dir == "" {
		t.Skip("set YAGURA_LARGE_DIR to a full (non-partial) clone to run the protocol")
	}
	n, _ := strconv.Atoi(os.Getenv("YAGURA_LARGE_N"))
	if n == 0 {
		n = 2000
	}

	raw, err := churn.ReadGitLog(context.Background(), dir, n)
	if err != nil {
		t.Fatalf("gitlog: %v", err)
	}
	commits, err := churn.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sr, err := srcfiles.ReadGo(dir)
	if err != nil {
		t.Fatalf("srcfiles: %v", err)
	}
	sizes := map[string]int{}
	for p, c := range sr.Files {
		sizes[p] = strings.Count(c, "\n") + 1
	}
	cx := map[string]int{}
	for _, f := range complexity.Scan(sr.Files, 10).Functions {
		if f.Complexity > cx[f.File] {
			cx[f.File] = f.Complexity
		}
	}
	span := 0.0
	if len(commits) > 1 {
		span = commits[0].When.Sub(commits[len(commits)-1].When).Hours() / 24
	}
	// 読めた割合を必ず出す。1,000 上限に当たっていることを黙って測ると、
	// 「17,878 ファイルのプロジェクトを測った」という誤った主張になる。
	t.Logf("=== %s n=%d: commits=%d files_read=%d/%d incomplete=%v span=%.0fd ===",
		filepath.Base(dir), n, len(commits), len(sr.Files), sr.Matched, sr.Incomplete(), span)

	// git log のパスと走査したソースのパスが **交わっているか** を先に確かめる。
	//
	// dir がリポジトリのルートでない場合(例: モノレポの部分ディレクトリ)、
	// git log は "sub/dir/internal/x.go"、srcfiles は "internal/x.go" を返し、
	// 交差が空になる。すると walkforward は「陽性が 1 つも無い」と報告し、
	// **製品の所見のように見える**。このセッションで実際に一度読み違えた。
	// 計測の失敗を製品の失敗と取り違えないための門。
	touched := map[string]bool{}
	for _, c := range commits {
		for _, f := range c.Files {
			touched[f.Path] = true
		}
	}
	overlap := 0
	for path := range sizes {
		if touched[path] {
			overlap++
		}
	}
	if overlap == 0 {
		t.Fatalf("no git-log path matches any scanned source file (%d logged, %d scanned): "+
			"YAGURA_LARGE_DIR must be the repository ROOT, or the paths will not line up — "+
			"this is a harness error, not a finding about the product", len(touched), len(sizes))
	}
	t.Logf("  path overlap: %d/%d scanned files appear in the log", overlap, len(sizes))

	wf := walkforward.Run(commits, sizes, cx, walkforward.Options{})
	if !wf.Valid {
		t.Logf("  walkforward INVALID (no fold had positives)")
	} else {
		for _, name := range []string{"size_loc", "size_loc_asc", "churn_count", "complexity", "relative_churn", "churn_count_per_loc", "complexity_per_loc", "contributors_per_loc"} {
			s := wf.PerScorer[name]
			t.Logf("  wf %-14s prec=%.3f lift=%.3f | effort recall=%.3f lift=%.3f",
				name, s.MeanPrecision, s.MeanLift, s.MeanRecallAtEffort, s.MeanEffortLift)
		}
		t.Logf("  wf best=%s folds=%d skipped=%d", wf.Best, len(wf.Folds), wf.SkippedFolds)
	}

	train, test := cochange.Split(commits, 0.7)
	opts := cochange.DefaultOptions()
	for _, k := range []int{1, 3, 5} {
		byConf := cochange.Evaluate(train, test, opts, k)
		liftOpts := opts
		liftOpts.RankByLift = true
		byLift := cochange.Evaluate(train, test, liftOpts, k)
		if !byConf.Valid {
			t.Logf("  cochange k=%d INVALID", k)
			continue
		}
		t.Logf("  cochange k=%d conf lift=%s (p %.3f / base %.3f) | lift-rank %s | cov %.2f rules=%d",
			k, liftStr(byConf.Lift), byConf.Precision, byConf.BaselinePrecision,
			liftStr(byLift.Lift), byConf.Coverage, byConf.Rules)
	}
}

// liftStr は「定義できない lift」を数値のふりをさせずに表示する。
func liftStr(l *float64) string {
	if l == nil {
		return "undefined"
	}
	return fmt.Sprintf("%.3f", *l)
}
