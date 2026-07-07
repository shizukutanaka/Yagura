// Package regress は 2 つのコード状態(old / new)を比較し、関数ごとの品質
// メトリクスが *悪化* した箇所を報告する(ソクラテス新視点 XIV — 時系列/回帰軸)。
//
// 動機:
//
//	既存の ~13 レンズはすべて単一スナップショット(`Scan(files) Report`)を測る。
//	しかし CI で最も重要なのは「この変更が品質を後退させていないか」——complexity
//	8→14、引数 3→6、新たな naked return——という *差分* である。どのレンズもこの
//	時系列軸を見ていなかった。regress は old/new 2 つの file set を取り、calibrate の
//	per-function メトリクス(complexity/params/returns/func_lines)を双方に適用し、
//	(file, func) で突き合わせて *増加* した関数を報告する。これは「品質のラチェット」
//	——絶対値が完璧でなくとも後退だけは止める——を機械化する。
//
// マッチングは (File, Func) 完全一致。リネーム/移動は old 側削除 + new 側追加と
// 見なし、既存関数の regression としては扱わない(保守的・型解決なし)。
// Crossed = new 値がそのメトリクスの慣習しきい値(calibrate.MetricDefault)を超過。
// CI gate(--strict)は Crossed な regression のみで fail させるのが想定運用。
//
// calibrate.FuncMetrics を再利用(メトリクス定義の単一情報源)。決定論的。
package regress

import (
	"sort"

	"github.com/shizukutanaka/yagura/internal/calibrate"
)

// Regression は 1 関数の 1 メトリクスが old→new で増加した(=悪化した)記録。
type Regression struct {
	File    string `json:"file"`
	Func    string `json:"func"`
	Metric  string `json:"metric"`
	Old     int    `json:"old"`
	New     int    `json:"new"`
	Delta   int    `json:"delta"`   // New - Old (>0)
	Crossed bool   `json:"crossed"` // New が慣習しきい値を超過
}

// Report は Compare の集計。
type Report struct {
	OldFuncs    int          `json:"old_funcs"`
	NewFuncs    int          `json:"new_funcs"`
	Regressed   int          `json:"regressed"` // 悪化メトリクス総数(= len(Regressions))
	Crossed     int          `json:"crossed"`   // うち慣習しきい値を超えた数
	Regressions []Regression `json:"regressions"`
}

// Compare は old/new の file set を比較し、関数ごとの品質メトリクス悪化を報告する。
// 出力は決定論的(Delta desc → File → Func → Metric)。conventional threshold
// (calibrate.MetricDefault)のみで Crossed を判定する — CompareWithThresholds(nil)
// と等価。
func Compare(oldFiles, newFiles map[string]string) Report {
	return CompareWithThresholds(oldFiles, newFiles, nil)
}

// CompareWithThresholds は Compare と同じだが、Crossed の判定に使うしきい値を
// metric 名(complexity/params/returns/func_lines)ごとに overrides で上書きできる
// (v0.104.0、calibrate --write が書く .yagura/thresholds.json を消費する呼出側
// 向け)。overrides に無い metric は従来どおり calibrate.MetricDefault にフォール
// バックする。overrides の未知キーは無視する(検証は calibrate.LoadThresholdsFile
// の責務)。nil を渡せば Compare と完全に同じ挙動。
func CompareWithThresholds(oldFiles, newFiles map[string]string, overrides map[string]int) Report {
	oldM := calibrate.FuncMetrics(oldFiles)
	newM := calibrate.FuncMetrics(newFiles)

	// old を (file, func) で索引化。
	oldIdx := make(map[key]calibrate.FuncMetric, len(oldM))
	for _, fm := range oldM {
		oldIdx[key{fm.File, fm.Func}] = fm
	}

	r := Report{OldFuncs: len(oldM), NewFuncs: len(newM)}
	for _, nf := range newM {
		of, ok := oldIdx[key{nf.File, nf.Func}]
		if !ok {
			continue // new 関数 = regression ではない
		}
		for _, metric := range calibrate.MetricNames() {
			oldV, newV := of.Value(metric), nf.Value(metric)
			if newV <= oldV {
				continue
			}
			def := calibrate.MetricDefault(metric)
			if v, ok := overrides[metric]; ok {
				def = v
			}
			crossed := def > 0 && newV > def
			r.Regressions = append(r.Regressions, Regression{
				File: nf.File, Func: nf.Func, Metric: metric,
				Old: oldV, New: newV, Delta: newV - oldV, Crossed: crossed,
			})
			if crossed {
				r.Crossed++
			}
		}
	}
	r.Regressed = len(r.Regressions)
	sortRegressions(r.Regressions)
	return r
}

type key struct{ file, fn string }

// sortRegressions は決定論的順序: Delta desc → File → Func → Metric。
func sortRegressions(rs []Regression) {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Delta != b.Delta {
			return a.Delta > b.Delta
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Func != b.Func {
			return a.Func < b.Func
		}
		return a.Metric < b.Metric
	})
}
