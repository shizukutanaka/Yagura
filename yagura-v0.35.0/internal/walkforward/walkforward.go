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
		// ManualUp — 小さいファイルから読む。effort-aware 文献で知られた
		// **強い対照群** で、これを置かない比較は都合の良い相手としか戦っていない
		// (Menzies らが繰り返し指摘する落とし穴)。降順ソートなので符号を反転する。
		{"size_loc_asc", func(r defectdataset.Row) float64 { return -float64(r.SizeLOC) }},
		// --- effort-aware 用の density 候補(v1.85.0)---
		//
		// effort-aware 文献の定石は「予測欠陥数 / 費用」で並べること
		// (Mende & Koschke, CSMR 2010)。費用を LOC とするなら、信号を LOC で
		// 割った密度が正しい順序になるはず——という **反証可能な予測** を置く。
		// ManualUp に勝てなければ、この製品の順位付けは正当化できない。
		{"churn_count_per_loc", func(r defectdataset.Row) float64 { return perLOC(float64(r.ChurnCount), r.SizeLOC) }},
		{"complexity_per_loc", func(r defectdataset.Row) float64 { return perLOC(float64(r.Complexity), r.SizeLOC) }},
		{"contributors_per_loc", func(r defectdataset.Row) float64 { return perLOC(float64(r.Contributors), r.SizeLOC) }},
	}
}

// perLOC は「1 行あたり」に直す。SizeLOC 0(走査で見つからなかったファイル)は
// 0 を返す——未知を最良と見なして上位に置くと、測っていないものを推薦してしまう。
func perLOC(v float64, loc int) float64 {
	if loc <= 0 {
		return 0
	}
	return v / float64(loc)
}

// DefaultEffortBudget は effort-aware 評価で読むと仮定する LOC の割合。
// 20% は Arisholm らの CE / Mende & Koschke 以降、この文献で標準的に使われる値。
const DefaultEffortBudget = 0.20

// Options は評価条件。
type Options struct {
	Folds int
	TopK  int
	// GapDays は feature window と label window の間に空ける日数(v0.126.0)。
	// gap 内のコミットは **特徴にもラベルにも使わない**。0 で gap 無し(既定)。
	GapDays int
	// WindowDays > 0 で feature window を **sliding**(直近 N 日のみ)にする。
	// 0(既定)は **expanding**(履歴の先頭から cut まで全部)。v0.127.0。
	WindowDays int
	Scorers    []Scorer
	// EffortBudget は effort-aware 評価で読む LOC の割合(0 で DefaultEffortBudget)。
	EffortBudget float64
}

// FoldInfo は 1 fold の窓の素性(順序保存の検証に使えるよう外に出す)。
type FoldInfo struct {
	Index          int       `json:"index"`
	FeatureCommits int       `json:"feature_commits"`
	LabelCommits   int       `json:"label_commits"`
	Rows           int       `json:"rows"`
	Positives      int       `json:"positives"`
	Skipped        bool      `json:"skipped"` // label window に陽性が無く採点不能
	K              int       `json:"k"`       // precision@K の K(粒度が見えるよう公開)
	GapCommits     int       `json:"gap_commits"`
	FeatureStart   time.Time `json:"feature_start"`
	FeatureEnd     time.Time `json:"feature_end"`
	GapEnd         time.Time `json:"gap_end,omitempty"`
	LabelStart     time.Time `json:"label_start"`
	Reason         string    `json:"reason,omitempty"`
}

// ScorerResult は 1 scorer の集計。
type ScorerResult struct {
	Name          string  `json:"name"`
	MeanPrecision float64 `json:"mean_precision_at_k"`
	MeanBaseline  float64 `json:"mean_baseline"`
	MeanLift      float64 `json:"mean_lift"`
	// MeanRecallAtEffort は「全 LOC の EffortBudget 割を読むまで」に見つかった
	// 欠陥ファイルの割合。precision@K と違い **サイズを支払わせる** ので、
	// 大きいファイルを先に読ませるランキングは予算を食い潰して損をする。
	MeanRecallAtEffort float64 `json:"mean_recall_at_effort"`
	// MeanEffortLift = MeanRecallAtEffort / EffortBudget。ランダムな順で読めば
	// 期待 recall は予算割合そのものになるので、1 がランダム相当。
	MeanEffortLift float64 `json:"mean_effort_lift"`
	ScoredFolds    int     `json:"scored_folds"`
}

// Report は walk-forward 全体の結果。
type Report struct {
	Valid        bool       `json:"valid"`
	GapDays      int        `json:"gap_days"`
	WindowMode   string     `json:"window_mode"` // "expanding" | "sliding"
	WindowDays   int        `json:"window_days"`
	Folds        []FoldInfo `json:"folds"`
	SkippedFolds int        `json:"skipped_folds"`
	// EffortBudget は effort-aware 評価で読んだ LOC の割合(実際に使った値)。
	EffortBudget float64                 `json:"effort_budget"`
	PerScorer    map[string]ScorerResult `json:"per_scorer"`
	Best         string                  `json:"best,omitempty"`
	Note         string                  `json:"note"`
}

const validNote = "Walk-forward validation: each fold's features come strictly from commits " +
	"preceding its label window (Falessi et al., EMSE 2020 — order-preserving validation), with " +
	"an EXPANDING feature window (every fold trains on all history up to its cut). " +
	"Metric is precision@K against files touched by fix commits (SZZ stage 1), with the fold's " +
	"own positive rate as baseline; lift > 1 beats random ranking. " +
	"Reported ALONGSIDE it is an EFFORT-AWARE metric: recall of defective files within a budget of " +
	"20% of the total LOC, read in ranked order (Arisholm, Briand & Johannessen, JSS 2010; " +
	"Mende & Koschke, CSMR 2010). Both are given because they disagree by construction: precision@K " +
	"counts a 4,000-line file and a 50-line file as one pick each, while the label (files touched by " +
	"fix commits) is itself easier to hit on large files — so a ranking can win on precision@K purely " +
	"by preferring big files. effort_lift > 1 beats reading in random order. The size_loc_asc scorer " +
	"is the ManualUp control from that literature: smallest-file-first is a strong effort-aware " +
	"baseline, and a comparison without it is not a fair fight. MEASURED RESULT YOU SHOULD KNOW " +
	"BEFORE TRUSTING A RANKING FROM THIS TOOL: across the eight repositories this project has " +
	"measured (groupcache, gorilla/mux, logrus, prometheus, hugo, etcd, moby, kubernetes), " +
	"size_loc_asc had the best effort-aware lift in 8 of 8 (1.35-2.29) while size_loc was BELOW " +
	"random in 8 of 8 — the exact reverse of the precision@K ranking. Reading the smallest files " +
	"first beat every signal shipped here. Treat a high precision@K with suspicion until the " +
	"effort-aware number agrees with it. " +
	"Label windows are equal-sized across folds so no fold carries extra weight in the mean."

// windowNoteExpanding / windowNoteSliding は使った窓の種類を必ず言明する。
const windowNoteExpanding = " Feature window is EXPANDING: every fold trains on all history up to " +
	"its cut. McIntosh & Kamei (TSE 2018, 37,524 changes in Qt and OpenStack) found that JIT model " +
	"discriminatory power and calibration drop considerably one year after training because the " +
	"properties of fix-inducing changes shift, and recommend training on six months or more of " +
	"history — so keeping old data is not automatically better. Pass window_days>0 for a sliding window."

const windowNoteSliding = " Feature window is SLIDING over the last %d day(s): older history is " +
	"deliberately ignored, following the model-ageing result of McIntosh & Kamei (TSE 2018). NOTE: " +
	"they recommend SIX MONTHS or more of training history; a window shorter than that is this " +
	"project's own choice for exploring the mechanism, not their recommendation."

// gapNoteOn / gapNoteOff は gap の有無を必ず言明する(黙って gap 無しで測らない)。
const gapNoteOn = " A gap of %d day(s) separates each feature window from its label window; " +
	"commits inside the gap feed neither. This follows the verification-latency argument in the " +
	"just-in-time defect-prediction literature: a change cannot be labelled clean until a waiting " +
	"period has passed, and ignoring that latency yields false performance estimates. NOTE: that " +
	"literature specifies the waiting period in DAYS for online JIT models; the adaptation here is " +
	"a day-based gap between offline windows, which is analogous but not the same protocol."

const gapNoteOff = " NO GAP was applied (gap_days=0): a fix landing immediately after the cut still " +
	"counts as a future label even if it was part of the same work. Pass gap_days>0 to separate them. " +
	"Separately, the newest label window sits adjacent to HEAD, so files marked not-fixed there may " +
	"simply not have been fixed YET (one-sided label noise from verification latency)."

// Run は commits を時系列で Folds+1 区間に切り、fold ごとに
// 「先頭〜区間 i」で特徴を作り「区間 i+1」でラベルを作って scorer を評価する。
func Run(commits []churn.Commit, sizes, complexity map[string]int, opts Options) Report {
	if opts.EffortBudget <= 0 || opts.EffortBudget >= 1 {
		opts.EffortBudget = DefaultEffortBudget
	}
	rep := Report{Folds: []FoldInfo{}, PerScorer: map[string]ScorerResult{},
		EffortBudget: opts.EffortBudget, Note: validNote}
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
		recallEffort              float64
		n                         int
	}
	accs := map[string]*acc{}
	for _, s := range scorers {
		accs[s.Name] = &acc{}
	}

	rep.GapDays = opts.GapDays
	rep.WindowDays = opts.WindowDays
	if opts.WindowDays > 0 {
		rep.WindowMode = "sliding"
		rep.Note += fmt.Sprintf(windowNoteSliding, opts.WindowDays)
	} else {
		rep.WindowMode = "expanding"
		rep.Note += windowNoteExpanding
	}
	if opts.GapDays > 0 {
		rep.Note += fmt.Sprintf(gapNoteOn, opts.GapDays)
	} else {
		rep.Note += gapNoteOff
	}

	seg := len(ordered) / (folds + 1)
	if seg < 1 {
		seg = 1
	}
	for i := 1; i <= folds; i++ {
		featEnd := seg * i
		// label 窓は **全 fold 等サイズ**。最後の fold に残り全部を吸わせると、
		// その fold だけ陽性数と baseline が変わるのに平均では 1 票のまま扱われ、
		// 集計が歪む(v0.126.0 で修正)。
		labelEnd := seg * (i + 1)
		if labelEnd > len(ordered) {
			labelEnd = len(ordered)
		}
		if featEnd >= len(ordered) || labelEnd <= featEnd {
			break
		}
		featureWin := ordered[:featEnd]
		// sliding: 直近 WindowDays 日ぶんだけを特徴に使う(古い履歴を捨てる)
		if opts.WindowDays > 0 && len(featureWin) > 0 {
			from := featureWin[len(featureWin)-1].When.AddDate(0, 0, -opts.WindowDays)
			start := 0
			for start < len(featureWin) && featureWin[start].When.Before(from) {
				start++
			}
			featureWin = featureWin[start:]
			if len(featureWin) == 0 {
				rep.Folds = append(rep.Folds, FoldInfo{
					Index: i - 1, Skipped: true,
					Reason: "the sliding feature window contains no commits; nothing to build features from",
				})
				rep.SkippedFolds++
				continue
			}
		}
		labelWin := ordered[featEnd:labelEnd]

		// gap: feature 窓の末尾時刻から GapDays 以内のコミットは label 窓から外す
		// (特徴にも入らない=どちらにも使わない)。
		gapCommits := 0
		var gapEnd time.Time
		if opts.GapDays > 0 && len(featureWin) > 0 {
			cutoff := featureWin[len(featureWin)-1].When.AddDate(0, 0, opts.GapDays)
			gapEnd = cutoff
			drop := 0
			for drop < len(labelWin) && !labelWin[drop].When.After(cutoff) {
				drop++
			}
			gapCommits = drop
			labelWin = labelWin[drop:]
			if len(labelWin) == 0 {
				rep.Folds = append(rep.Folds, FoldInfo{
					Index: i - 1, FeatureCommits: len(featureWin), GapCommits: gapCommits,
					Skipped: true, FeatureEnd: featureWin[len(featureWin)-1].When, GapEnd: gapEnd,
					Reason: "the gap consumed the entire label window; nothing left to score",
				})
				rep.SkippedFolds++
				continue
			}
		}

		ds := defectdataset.BuildWindows(featureWin, labelWin, sizes, complexity)
		info := FoldInfo{
			Index:          i - 1,
			FeatureCommits: len(featureWin),
			LabelCommits:   len(labelWin),
			Rows:           ds.Meta.Rows,
			Positives:      ds.Meta.DefectiveRows,
			GapCommits:     gapCommits,
			FeatureStart:   featureWin[0].When,
			FeatureEnd:     featureWin[len(featureWin)-1].When,
			GapEnd:         gapEnd,
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
		info.K = k
		baseline := ds.Meta.PositiveRate
		budget := effortBudgetLOC(ds.Rows, opts.EffortBudget)
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
			a.recallEffort += recallAtEffort(rows, budget)
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
			Name:               name,
			MeanPrecision:      a.precision / float64(a.n),
			MeanBaseline:       a.baseline / float64(a.n),
			MeanLift:           a.lift / float64(a.n),
			MeanRecallAtEffort: a.recallEffort / float64(a.n),
			MeanEffortLift:     a.recallEffort / float64(a.n) / opts.EffortBudget,
			ScoredFolds:        a.n,
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

// effortBudgetLOC は「読む」と仮定する LOC 数を返す。最低 1 行(0 予算では
// どの scorer も recall 0 になり比較が無意味になる)。
func effortBudgetLOC(rows []defectdataset.Row, fraction float64) int {
	total := 0
	for _, r := range rows {
		total += r.SizeLOC
	}
	b := int(float64(total) * fraction)
	if b < 1 {
		b = 1
	}
	return b
}

// recallAtEffort は「上から予算 LOC 分だけ読む」までに見つかった欠陥ファイルの割合。
//
// これが precision@K と違うのは **サイズを支払わせる** 点にある。上位 K 件では
// 4,000 行のファイルも 50 行のファイルも 1 件と数えられるが、実際に読むのは人間で、
// 予算は行数で尽きる。Arisholm/Briand/Johannessen(JSS 2010)と Mende & Koschke
// (CSMR 2010)が effort-aware 評価を要求したのはこの乖離のためである。
//
// rows は既に scorer の降順に並んでいることを前提とする(呼出側の責務)。
func recallAtEffort(rows []defectdataset.Row, budgetLOC int) float64 {
	positives := 0
	for _, r := range rows {
		if r.Fixed {
			positives++
		}
	}
	if positives == 0 {
		return 0
	}
	spent, found := 0, 0
	for _, r := range rows {
		// 予算に収まらないファイルが来たら **そこで止める**。
		//
		// 最初の 1 件だけは無条件に読ませる救済を一度書いたが、それは間違いだった:
		// 巨大ファイルを先頭に置くランキング——まさにこの指標が罰するはずの
		// 相手——にだけ無料の当たりを配ってしまう。予算 880 行で 4,000 行の
		// ファイルは読めない。読めないものを読めたことにしない。
		//
		// 飛ばして次を読む(skip)のではなく止める(break)のは、skip が
		// **利用者が実際に辿る順序と違う順序**を評価してしまうため。
		if spent+r.SizeLOC > budgetLOC {
			break
		}
		spent += r.SizeLOC
		if r.Fixed {
			found++
		}
	}
	return float64(found) / float64(positives)
}
