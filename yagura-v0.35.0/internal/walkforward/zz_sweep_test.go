package walkforward_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/churn"
	"github.com/shizukutanaka/yagura/internal/complexity"
	"github.com/shizukutanaka/yagura/internal/srcfiles"
	"github.com/shizukutanaka/yagura/internal/walkforward"
)

// TestCostSweep: cost(f) = FileCostLOC + SizeLOC(f) を掃引し、
// density 系が ManualUp を上回る費用観が在るかを測る。
func TestCostSweep(t *testing.T) {
	dir := os.Getenv("YAGURA_LARGE_DIR")
	if dir == "" {
		t.Skip("set YAGURA_LARGE_DIR")
	}
	n, _ := strconv.Atoi(os.Getenv("YAGURA_LARGE_N"))
	if n == 0 {
		n = 2000
	}
	pathspec := churn.DefaultPathspec
	if e := os.Getenv("YAGURA_LARGE_EXT"); e != "" {
		pathspec = []string{"*" + e}
	}
	raw, err := churn.ReadGitLogPaths(context.Background(), dir, n, pathspec)
	if err != nil {
		t.Fatalf("gitlog: %v", err)
	}
	commits, err := churn.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ext := os.Getenv("YAGURA_LARGE_EXT")
	var sr srcfiles.Result
	if ext == "" {
		sr, err = srcfiles.ReadGo(dir)
	} else {
		sr, err = srcfiles.ReadLimited(dir, 1000, srcfiles.DefaultMaxBytes, func(n string) bool {
			return strings.HasSuffix(n, ext)
		})
	}
	if err != nil {
		t.Fatalf("srcfiles: %v", err)
	}
	sizes := map[string]int{}
	for p, c := range sr.Files {
		sizes[p] = strings.Count(c, "\n") + 1
	}
	// complexity は Go パーサ依存なので、非 Go では空のまま渡す(complexity/LOC は
	// その場合 0 になる——測れないものを測れたことにしない)。
	cx := map[string]int{}
	if os.Getenv("YAGURA_LARGE_EXT") == "" {
		for _, f := range complexity.Scan(sr.Files, 10).Functions {
			if f.Complexity > cx[f.File] {
				cx[f.File] = f.Complexity
			}
		}
	}
	names := []string{"size_loc_asc", "churn_count_per_loc", "contributors_per_loc", "complexity_per_loc", "relative_churn"}
	for _, fc := range []int{0, 25, 50, 100, 200, 400, 800} {
		wf := walkforward.Run(commits, sizes, cx, walkforward.Options{FileCostLOC: fc})
		if !wf.Valid {
			continue
		}
		line := fmt.Sprintf("SWEEP %-12s fc=%-4d", envBase(dir), fc)
		for _, nm := range names {
			line += fmt.Sprintf(" %s=%.3f", short(nm), wf.PerScorer[nm].MeanEffortLift)
		}
		t.Log(line)
	}
}

func envBase(d string) string {
	i := strings.LastIndex(d, "/")
	return d[i+1:]
}

func short(n string) string {
	switch n {
	case "size_loc_asc":
		return "manualUp"
	case "churn_count_per_loc":
		return "churn/L"
	case "contributors_per_loc":
		return "contrib/L"
	case "complexity_per_loc":
		return "cplx/L"
	}
	return "relchurn"
}
