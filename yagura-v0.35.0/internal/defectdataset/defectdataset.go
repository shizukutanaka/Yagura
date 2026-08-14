// Package defectdataset は各リポジトリの git 履歴から **ファイル単位の欠陥データセット**
// を組み立てる(v0.124.0)。
//
// なぜ必要か:
//
//	v0.119-v0.123 でプロセス指標(churn/ownership)・製品指標(complexity)・
//	fix ラベル(fixhistory = SZZ 第 1 段)が揃ったが、それらを **1 つの表** として
//	出力する口が無かった。表にできれば (1) 外部ツールで自由に検証でき、
//	(2) 将来 processrisk の重みをデータ駆動で学習でき、(3) 複数リポジトリのデータを
//	集めて「1 リポジトリ = 1 観測点」という v0.123.0 の限界を超えられる。
//
// 研究的根拠 — 形式と時間分割:
//
//   - Zimmermann, Premraj & Zeller, "Predicting Defects for Eclipse"(PROMISE 2007)。
//     公開欠陥データセットの古典で、**リリース前のメトリクス + リリース後の欠陥数**
//     をファイル/パッケージ単位の行として公開し、後続研究の標準ベンチマークになった。
//     「行 = ファイル、列 = メトリクス群 + 欠陥ラベル」という本パッケージの形式は
//     これに倣う(PROMISE リポジトリの CSV 慣行も同じ)。
//
//   - **時間分割(temporal split)は飾りではない。** Eclipse データセットが
//     pre-release metrics → post-release defects と時間を分けているのは、同一期間から
//     特徴とラベルを取ると「過去を予測する」リーク付きデータセットになるため。
//     本パッケージは既定でコミット履歴を時間で切り、**古い側から特徴・新しい側から
//     ラベル**を作る。
//
//     自己指摘: v0.123.0 の validation(precision@10)は同一期間の特徴とラベルを
//     突き合わせていた。あの数値は「同じ窓の中でよく当たる」ことしか示しておらず、
//     将来予測の証拠ではない。本パッケージではその弱点を既定で塞ぎ、分割を切る場合は
//     Meta.Leakage=true をデータ自身に刻む(黙って混ぜない)。
//
// ラベルの由来は fixhistory と同じく **SZZ 第 1 段のみ**(メッセージからの fix 特定)で、
// bug 導入コミットの blame 遡行は行っていない。したがって fixed は「その窓で fix が
// 触れた」という近似であり、真の欠陥ラベルではない——Meta.Note に明記する。
//
// zero-dep(ADR-0001): stdlib + internal/{churn,ownership,fixhistory} のみ。
package defectdataset

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/shizukutanaka/yagura/internal/churn"
	"github.com/shizukutanaka/yagura/internal/fixhistory"
	"github.com/shizukutanaka/yagura/internal/ownership"
)

// FormatVersion はデータセット形式のバージョン(列が変わったら上げる)。
const FormatVersion = "1"

// DefaultSplitRatio は既定の時間分割比(古い側 70% を特徴、新しい側 30% をラベル)。
const DefaultSplitRatio = 0.7

// Options は生成条件。
type Options struct {
	// SplitRatio は feature window の割合(0 < r < 1)。
	// 0 以下 or 1 以上を渡すと **分割なし**(同一期間から特徴とラベルを取る)になり、
	// Meta.Leakage=true が立つ。明示的な opt-in であって既定ではない。
	SplitRatio float64
}

// Row は 1 ファイル分の観測(特徴列 + ラベル列)。
type Row struct {
	Path string `json:"path"`

	// --- features(feature window 由来)---
	RelativeChurn float64 `json:"relative_churn"`
	ChurnCount    int     `json:"churn_count"`
	ChurnedLOC    int     `json:"churned_loc"`
	DeletedLOC    int     `json:"deleted_loc"`
	Ownership     float64 `json:"ownership"`
	MinorContrib  int     `json:"minor_contributors"`
	MajorContrib  int     `json:"major_contributors"`
	Contributors  int     `json:"contributors"`
	Complexity    int     `json:"complexity"`
	SizeLOC       int     `json:"size_loc"`

	// --- labels(label window 由来。慣行どおり末尾に置く)---
	FixCount int  `json:"fix_count"`
	Fixed    bool `json:"fixed"`
}

// Meta はデータセットの素性。数字だけ渡して由来を隠さない。
type Meta struct {
	FormatVersion      string    `json:"format_version"`
	SplitRatio         float64   `json:"split_ratio"`
	Leakage            bool      `json:"leakage"` // 特徴とラベルが同一期間か
	FeatureCommits     int       `json:"feature_commits"`
	LabelCommits       int       `json:"label_commits"`
	FeatureStart       time.Time `json:"feature_start,omitempty"`
	FeatureEnd         time.Time `json:"feature_end,omitempty"`
	LabelStart         time.Time `json:"label_start,omitempty"`
	LabelEnd           time.Time `json:"label_end,omitempty"`
	Rows               int       `json:"rows"`
	DefectiveRows      int       `json:"defective_rows"`
	PositiveRate       float64   `json:"positive_rate"`
	SkippedUnknownSize int       `json:"skipped_unknown_size"`
	Note               string    `json:"note"`
}

// Dataset は Meta + Rows。
type Dataset struct {
	Meta Meta  `json:"meta"`
	Rows []Row `json:"rows"`
}

const baseNote = "One row per file. Features come from the feature window, labels (fix_count/fixed) " +
	"from the later label window — a temporal split in the manner of Zimmermann/Premraj/Zeller's " +
	"Eclipse dataset (PROMISE 2007), so features never see the future. Labels are SZZ stage 1 only " +
	"(fix commits identified by message); no blame trace to bug-introducing commits, so 'fixed' means " +
	"'a fix commit touched this file in the label window', not a verified defect."

const leakNote = "WARNING: LEAKING DATASET. split_ratio disabled the temporal split, so features and " +
	"labels are drawn from the same commits. Any model trained on this will look better than it is. " +
	baseNote

// Build は commits を時間で分割し、特徴とラベルを別々の窓から作ってデータセットを返す。
// sizes に無いファイル(現存しない/対象外)は相対化できないので除外し、Meta に数える。
func Build(commits []churn.Commit, sizes map[string]int, complexity map[string]int, opts Options) Dataset {
	d := Dataset{Rows: []Row{}}
	d.Meta.FormatVersion = FormatVersion
	d.Meta.SplitRatio = opts.SplitRatio

	// 時系列に並べる(git log は新しい順で来るため)
	ordered := append([]churn.Commit(nil), commits...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].When.Before(ordered[j].When) })

	featureCommits, labelCommits := split(ordered, opts.SplitRatio)
	if opts.SplitRatio <= 0 || opts.SplitRatio >= 1 {
		d.Meta.Leakage = true
		d.Meta.Note = leakNote
	} else {
		d.Meta.Note = baseNote
	}
	d.Meta.FeatureCommits = len(featureCommits)
	d.Meta.LabelCommits = len(labelCommits)
	d.Meta.FeatureStart, d.Meta.FeatureEnd = span(featureCommits)
	d.Meta.LabelStart, d.Meta.LabelEnd = span(labelCommits)

	// 特徴: feature window のみ
	chRep := churn.Analyze(featureCommits, sizes, complexity)
	ownRep := ownership.Analyze(featureCommits, nil)
	ownByPath := make(map[string]ownership.FileOwnership, len(ownRep.Files))
	for _, o := range ownRep.Files {
		ownByPath[o.Path] = o
	}
	// ラベル: label window のみ
	fixRep := fixhistory.Analyze(labelCommits)

	for _, c := range chRep.Files {
		size, known := sizes[c.Path]
		if !known || size <= 0 {
			continue // churn.Analyze 側で既に除外されているが二重に守る
		}
		r := Row{
			Path:          c.Path,
			RelativeChurn: c.RelativeChurn,
			ChurnCount:    c.ChurnCount,
			ChurnedLOC:    c.ChurnedLOC,
			DeletedLOC:    c.DeletedLOC,
			Complexity:    c.Complexity,
			SizeLOC:       c.SizeLOC,
			FixCount:      fixRep.FixesByFile[c.Path],
		}
		r.Fixed = r.FixCount > 0
		if o, ok := ownByPath[c.Path]; ok {
			r.Ownership = o.Ownership
			r.MinorContrib = o.Minor
			r.MajorContrib = o.Major
			r.Contributors = o.Total
		}
		d.Rows = append(d.Rows, r)
		if r.Fixed {
			d.Meta.DefectiveRows++
		}
	}
	d.Meta.SkippedUnknownSize = chRep.Skipped

	sort.SliceStable(d.Rows, func(i, j int) bool { return d.Rows[i].Path < d.Rows[j].Path })
	d.Meta.Rows = len(d.Rows)
	if d.Meta.Rows > 0 {
		d.Meta.PositiveRate = float64(d.Meta.DefectiveRows) / float64(d.Meta.Rows)
	}
	return d
}

// split は時系列順の commits を feature / label に切る。
// ratio が範囲外なら分割せず、両方に全件を返す(= リーク、Meta で明示される)。
func split(ordered []churn.Commit, ratio float64) (feature, label []churn.Commit) {
	if len(ordered) == 0 {
		return nil, nil
	}
	if ratio <= 0 || ratio >= 1 {
		return ordered, ordered
	}
	// 切り捨てではなく四捨五入。3 件を ratio 0.66 で切ると int() では 1 件(=33%)に
	// なり、要求した比率から大きくずれる。
	cut := int(math.Round(float64(len(ordered)) * ratio))
	if cut < 1 {
		cut = 1
	}
	if cut > len(ordered)-1 {
		cut = len(ordered) - 1
	}
	return ordered[:cut], ordered[cut:]
}

// span は時系列順コミット列の開始/終了時刻を返す。
func span(cs []churn.Commit) (start, end time.Time) {
	if len(cs) == 0 {
		return
	}
	return cs[0].When, cs[len(cs)-1].When
}

// csvColumns は列順(features → labels)。ラベルを末尾に置くのは慣行。
var csvColumns = []string{
	"path", "relative_churn", "churn_count", "churned_loc", "deleted_loc",
	"ownership", "minor_contributors", "major_contributors", "contributors",
	"complexity", "size_loc",
	"fix_count", "fixed",
}

// CSV は PROMISE 風の CSV を返す(行が 0 でもヘッダは必ず出す)。
func (d Dataset) CSV() string {
	var b strings.Builder
	b.WriteString(strings.Join(csvColumns, ","))
	b.WriteString("\n")
	for _, r := range d.Rows {
		fmt.Fprintf(&b, "%s,%.6f,%d,%d,%d,%.6f,%d,%d,%d,%d,%d,%d,%t\n",
			csvField(r.Path), r.RelativeChurn, r.ChurnCount, r.ChurnedLOC, r.DeletedLOC,
			r.Ownership, r.MinorContrib, r.MajorContrib, r.Contributors,
			r.Complexity, r.SizeLOC, r.FixCount, r.Fixed)
	}
	return b.String()
}

// csvField は , " 改行 を含む値を RFC4180 風に quote する。
func csvField(s string) string {
	if !strings.ContainsAny(s, ",\"\n\r") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
