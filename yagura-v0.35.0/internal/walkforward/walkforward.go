// Package walkforward は **時系列順を保つ** 評価(walk-forward validation)を行う
// (v0.125.0)。
//
// なぜ必要か:
//
//	v0.123.0 の `validation`(precision@10)は特徴とラベルを **同一の窓** から取って
//	いた。v0.124.0 でその欠陥を自己指摘したのに、tool は今も同じ数字を返し続けている
//	——「誤解を招くと自分で書いた数値」を出し続けるのは honest capability に反する。
//	本パッケージはその置き換えとなる、順序を保った評価を提供する。
//
// 研究的根拠:
//
//	Falessi, Huang, Narayana, Thai & Turhan, "On the need of preserving order of data
//	when validating within-project defect classifiers", EMSE 2020(arXiv:1809.01510)。
//	cross-validation / bootstrap / **walk-forward** を 10 分類器 × 13 OSS + 2 商用
//	プロジェクトで比較し、時系列順を保つのは walk-forward のみだと整理した上で、
//	同一分類器・同一プロジェクトでも 10-fold と walk-forward の **AUC 差が
//	[-0.20, +0.22] に及び、45% のケースで統計的に有意**であることを示した。
//	結論は within-project across-release では **walk-forward を使うべき**。
//
//	v0.124.0 の単発 train/test 分割より一段強い評価であり、「順序を保つ」という
//	要件そのものが本パッケージの実装対象。
//
// Scorer を注入できるようにしてあるのは、**同一プロトコルで競合シグナルを比較する**
// ため。v0.124.0 のデータセットは「変更回数 9.64× vs 相対 churn 1.22×」を示したが、
// processrisk は両者を等重みにしている。重みを勘で変える前に、同じ時系列条件で
// 測って比べる——その測定器がこれ。
//
// 評価指標は precision@K であって AUC ではない。K 件しか読まない運用(alert 予算、
// v0.122.0)に対応する指標を選んだためで、Falessi らの AUC 差の数値をそのまま
// 本パッケージの出力と比較できるわけではない。
//
// zero-dep(ADR-0001): stdlib + internal/{churn,defectdataset} のみ。
package walkforward

import (
	"fmt"
	"sort"
	"time"

	"github.com/shizukutanaka/yagura/internal/churn"
	"github.com/shizukutanaka/yagura/internal/defectdataset"
)

// DefaultFolds は既定の fold 数。
const DefaultFolds = 3

// DefaultTopK は precision@K の K(0 なら行数の 10% を使う)。
const DefaultTopK = 0

// Scorer は 1 行を「危険度」に写す関数。名前つきで複数比較できる。
type Scorer struct {
	Name string
	Of   func(defectdataset.Row) float64
}

// DefaultScorers は既定の比較対象。プロセス指標と製品指標(complexity)を
// **同じ土俵に載せる**ことで、どちらが効くかを見えるようにする。
func DefaultScorers() []Scorer {
	return []Scorer{
		{"relative_churn", func(r defectdataset.Row) float64 { return r.RelativeChurn }},
		{"churn_count", func(r defectdataset.Row) float64 { return float64(r.ChurnCount) }},
		{"complexity", func(r defectdataset.Row) float64 { return float64(r.Complexity) }},
		{"size_loc", func(r defectdataset.Row) float64 { return float64(r.SizeLOC) }},
	}
}

// Options は評価条件。
type Options struct {
	Folds   int
	TopK    int
	Scorers []Scorer
}

// FoldInfo は 1 fold の窓の素性(順序保存の検証に使えるよう外に出す)。
type FoldInfo struct {
	Index          int       `json:"index"`
	FeatureCommits int       `json:"feature_commits"`
	LabelCommits   int       `json:"label_commits"`
	Rows           int       `json:"rows"`
	Positives      int       `json:"positives"`
	Skipped        bool      `json:"skipped"` // label window に陽性が無く採点不能
	FeatureEnd     time.Time `json:"feature_end"`
	LabelStart     time.Time `json:"label_start"`
	Reason         string    `json:"reason,omitempty"`
}

// ScorerResult は 1 scorer の集計。
type ScorerResult struct {
	Name          string  `json:"name"`
	MeanPrecision float64 `json:"mean_precision_at_k"`
	MeanBaseline  float64 `json:"mean_baseline"`
	MeanLift      float64 `json:"mean_lift"`
	ScoredFolds   int     `json:"scored_folds"`
}

// Report は walk-forward 全体の結果。
type Report struct {
	Valid        bool                    `json:"valid"`
	Folds        []FoldInfo              `json:"folds"`
	SkippedFolds int                     `json:"skipped_folds"`
	PerScorer    map[string]ScorerResult `json:"per_scorer"`
	Best         string                  `json:"best,omitempty"`
	Note         string                  `json:"note"`
}

const validNote = "Walk-forward validation: each fold's features come strictly from commits " +
	"preceding its label window (Falessi et al., EMSE 2020 — order-preserving validation). " +
	"Metric is precision@K against files touched by fix commits (SZZ stage 1), with the fold's " +
	"own positive rate as baseline; lift > 1 beats random ranking."

// Run は commits を時系列で Folds+1 区間に切り、fold ごとに
// 「先頭〜区間 i」で特徴を作り「区間 i+1」でラベルを作って scorer を評価する。
func Run(commits []churn.Commit, sizes, complexity map[string]int, opts Options) Report {
	rep := Report{Folds: []FoldInfo{}, PerScorer: map[string]ScorerResult{}, Note: validNote}
	if len(commits) < 2 {
		rep.Note = "not enough commit history for walk-forward validation (need at least 2 commits)"
		return rep
	}
	folds := opts.Folds
	if folds <= 0 {
		folds = DefaultFolds
	}
	// 区間が空にならないよう clamp(fold+1 区間が必要)
	if folds > len(commits)-1 {
		folds = len(commits) - 1
	}
	scorers := opts.Scorers
	if len(scorers) == 0 {
		scorers = DefaultScorers()
	}

	ordered := append([]churn.Commit(nil), commits...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].When.Before(ordered[j].When) })

	// 集計器
	type acc struct {
		precision, baseline, lift float64
		n                         int
	}
	accs := map[string]*acc{}
	for _, s := range scorers {
		accs[s.Name] = &acc{}
	}

	seg := len(ordered) / (folds + 1)
	if seg < 1 {
		seg = 1
	}
	for i := 1; i <= folds; i++ {
		featEnd := seg * i
		labelEnd := seg * (i + 1)
		if i == folds {
			labelEnd = len(ordered) // 最後の fold は残り全部
		}
		if featEnd >= len(ordered) || labelEnd <= featEnd {
			break
		}
		featureWin := ordered[:featEnd]
		labelWin := ordered[featEnd:labelEnd]

		ds := defectdataset.BuildWindows(featureWin, labelWin, sizes, complexity)
		info := FoldInfo{
			Index:          i - 1,
			FeatureCommits: len(featureWin),
			LabelCommits:   len(labelWin),
			Rows:           ds.Meta.Rows,
			Positives:      ds.Meta.DefectiveRows,
			FeatureEnd:     featureWin[len(featureWin)-1].When,
			LabelStart:     labelWin[0].When,
		}
		// 陽性 0 の fold は採点不能。捏造した 0 を平均に混ぜない。
		if ds.Meta.DefectiveRows == 0 || ds.Meta.Rows == 0 {
			info.Skipped = true
			info.Reason = "no fix-labelled files in this label window; nothing to score against"
			rep.SkippedFolds++
			rep.Folds = append(rep.Folds, info)
			continue
		}
		k := opts.TopK
		if k <= 0 {
			k = ds.Meta.Rows / 10
		}
		if k < 1 {
			k = 1
		}
		if k > ds.Meta.Rows {
			k = ds.Meta.Rows
		}
		baseline := ds.Meta.PositiveRate
		for _, s := range scorers {
			rows := append([]defectdataset.Row(nil), ds.Rows...)
			sort.SliceStable(rows, func(a, b int) bool {
				va, vb := s.Of(rows[a]), s.Of(rows[b])
				if va != vb {
					return va > vb
				}
				return rows[a].Path < rows[b].Path
			})
			hits := 0
			for _, r := range rows[:k] {
				if r.Fixed {
					hits++
				}
			}
			p := float64(hits) / float64(k)
			a := accs[s.Name]
			a.precision += p
			a.baseline += baseline
			if baseline > 0 {
				a.lift += p / baseline
			}
			a.n++
		}
		rep.Folds = append(rep.Folds, info)
	}

	names := make([]string, 0, len(scorers))
	for _, s := range scorers {
		names = append(names, s.Name)
	}
	sort.Strings(names) // 決定論的に集計
	bestLift := -1.0
	for _, name := range names {
		a := accs[name]
		if a.n == 0 {
			continue
		}
		res := ScorerResult{
			Name:          name,
			MeanPrecision: a.precision / float64(a.n),
			MeanBaseline:  a.baseline / float64(a.n),
			MeanLift:      a.lift / float64(a.n),
			ScoredFolds:   a.n,
		}
		rep.PerScorer[name] = res
		if res.MeanLift > bestLift {
			bestLift = res.MeanLift
			rep.Best = name
		}
	}
	rep.Valid = len(rep.PerScorer) > 0
	if !rep.Valid {
		rep.Best = ""
		rep.Note = fmt.Sprintf("no fold had any fix-labelled files (%d fold(s) skipped); "+
			"walk-forward validation is unavailable for this history", rep.SkippedFolds)
	}
	return rep
}
