package lens_test

// determinism_test は「同じ入力なら同じ出力」を **実リポジトリ規模で** 確かめる。
//
// なぜ合成 fixture では足りないか(v1.88.0 の発見):
//
//	`sync_check` は同じ 2,500 ファイルに対して 1 件 ⇄ 26 件と揺れていた。原因は
//	型をパッケージ修飾なしの名前で index し、`parsed`(map)を走査していたこと——
//	`Config` のようなありふれた名前が複数パッケージで衝突し、どの定義が勝つかが
//	map の反復順で決まっていた。**同名衝突が起きない小さなリポジトリでは
//	絶対に現れない**ので、自リポジトリのテストは 20 リリース以上素通ししていた。
//
//	この種のバグは「規模を上げるまで見えない」種類のもので、v1.83.0 の
//	precision@K 飽和と同じ性質を持つ。だから合成 fixture の決定論テスト
//	(lens_test.go 側)と **対で** 実リポジトリ版を置く。
//
// 既定では skip する:
//
//	YAGURA_CAP_DIR=/path/to/large/repo go test ./internal/lens/ -run TestRunAll_StableOnRealRepo -v

import (
	"os"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/lens"
	"github.com/shizukutanaka/yagura/internal/srcfiles"
)

func TestRunAll_StableOnRealRepo(t *testing.T) {
	dir := os.Getenv("YAGURA_CAP_DIR")
	if dir == "" {
		t.Skip("set YAGURA_CAP_DIR to a large repository to run the real-scale determinism check")
	}
	res, err := srcfiles.ReadLimited(dir, 2500, srcfiles.DefaultMaxBytes,
		func(n string) bool { return strings.HasSuffix(n, ".go") })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) < 500 {
		t.Skipf("only %d files found; too small to expose name-collision bugs", len(res.Files))
	}
	t.Logf("checking %d files across %d lenses", len(res.Files), len(lens.Names()))

	base := lens.RunAll(res.Files, lens.Options{})
	for run := 1; run <= 3; run++ {
		got := lens.RunAll(res.Files, lens.Options{})
		if len(got) != len(base) {
			t.Fatalf("run %d: lens count changed: %d vs %d", run, len(got), len(base))
		}
		for i := range got {
			if got[i] != base[i] {
				t.Errorf("run %d: lens %q is not deterministic on identical input: "+
					"%d findings vs %d", run, got[i].Lens, got[i].Findings, base[i].Findings)
			}
		}
	}
}
