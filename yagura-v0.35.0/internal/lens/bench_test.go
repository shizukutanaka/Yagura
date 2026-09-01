package lens

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// RunAll の実コストを実リポジトリで測る(v1.81.0)。
//
// 実測 ~4.0 秒 / 352 ファイル。内訳の支配項は **再パース**: 29 レンズが各自
// go/parser を回し、さらに hotspot(12 レンズ束)と lens_overlap(12 レンズ束)が
// 内部で束を再実行するので、1 回の discovery call で延べ ~50 回 × 352 ファイル
// ≈ 18,000 パースが走る。
//
// この数字は 2 つの判断の根拠:
//  1. prealloc backlog(31 件)は **測って閉じた** — スライス再確保はこの 4 秒の
//     誤差にもならない。数字なしで「性能改善」として持ち越さない。
//  2. 4 秒を縮めたくなったら、正しい手はプリアロケートではなく **パースの共有**。
//     ただしレンズは files map を受ける純関数(結合ゼロ)という設計対価で成り立って
//     おり、AST 共有はその対価を崩す——4 秒が実害になってから払う。
func BenchmarkRunAll_RealRepo(b *testing.B) {
	files := map[string]string{}
	root := "../.."
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				switch info.Name() {
				case ".git", "bin", "vendor", ".yagura":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			if b, err := os.ReadFile(p); err == nil {
				rel, _ := filepath.Rel(root, p)
				files[rel] = string(b)
			}
		}
		return nil
	})
	b.Logf("files: %d", len(files))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RunAll(files, Options{})
	}
}
