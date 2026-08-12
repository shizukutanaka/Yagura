// Package fixhistory は「実際に fix されたファイルはどれか」を git 履歴から特定し、
// リスクランキングの **自己較正** に使う(v0.123.0)。
//
// なぜ必要か:
//
//	v0.121.0 / v0.122.0 の CHANGELOG は同じ限界を 2 回明記した——「なにも欠陥
//	データセットに較正されていない。これはリスク *指標* であって検証済み予測ではない」。
//	本パッケージはその較正データを、外部データセットではなく **各リポジトリ自身の
//	fix 履歴** から作る。
//
// 研究的根拠:
//
//	SZZ アルゴリズム(Śliwerski, Zimmermann & Zeller, "When Do Changes Induce
//	Fixes?", MSR 2005)。欠陥予測研究におけるグラウンドトゥルース構築の標準手法で、
//	3 段からなる: (1) bug-fix コミットの特定(メッセージ/イシュー参照から)、
//	(2) fix が変更した行の特定、(3) blame 遡行で bug を **導入した** コミットの特定。
//
// 実装範囲(honest capability):
//
//	本パッケージが実装するのは **第 1 段のみ**。第 2-3 段(行レベル diff + blame
//	遡行)は実装していない。よって得られるのは「bug を導入したコミット」ではなく
//	「fix が触れたファイル」であり、これを欠陥出現ファイルの *近似* として使う。
//	メッセージベース fix 特定の既知の限界も承知の上で使うこと:
//	  - "fix" と書かれない fix は見逃す(recall は完全ではない)
//	  - 単語断片の誤検出(prefix/suffix/fixture)は SZZ 追試("SZZ revisited:
//	    verifying when changes induce fixes", Williams & Spacco らの検証系列)が
//	    指摘する古典的弱点なので、**単語境界マッチ**で排除する(テストが固定)。
//
// Validate は precision@K を **ランダム基準線**(fix されたファイル比率)と併記する。
// 基準線なしの precision は「高く見えるだけの数字」になりうるため。fix データが
// 無いリポジトリでは Valid=false を返し、偽のスコアを出さない——常に成功する検証器は
// 何も検証しない。
//
// zero-dep(ADR-0001): stdlib + internal/churn のみ。
package fixhistory

import (
	"regexp"
	"sort"
	"strings"

	"github.com/shizukutanaka/yagura/internal/churn"
)

// fixRe は fix コミットの件名パターン。**単語境界**でマッチする——
// substring マッチだと prefix/suffix/fixture が偽 fix になる(SZZ 追試の指摘)。
var fixRe = regexp.MustCompile(`(?i)\b(fix(es|ed|ing)?|bugfix|hotfix|patch(es|ed)?|resolve[sd]?|correct(s|ed)?)\b`)

// IsFixCommit は件名が bug-fix コミットらしいかを判定する(SZZ 第 1 段)。
// Revert / Merge は fix 自体ではなく履歴の bookkeeping なので除外する。
func IsFixCommit(subject string) bool {
	s := strings.TrimSpace(strings.ToLower(subject))
	if strings.HasPrefix(s, "revert") || strings.HasPrefix(s, "merge") {
		return false
	}
	return fixRe.MatchString(s)
}

// Report はリポジトリの fix 履歴サマリ。
type Report struct {
	TotalCommits int            `json:"total_commits"`
	FixCommits   int            `json:"fix_commits"`
	FixesByFile  map[string]int `json:"fixes_by_file"`
	MostFixed    string         `json:"most_fixed,omitempty"`
}

// Analyze は commits から fix コミットを特定し、ファイル別 fix 回数を集計する。
func Analyze(commits []churn.Commit) Report {
	rep := Report{FixesByFile: map[string]int{}}
	rep.TotalCommits = len(commits)
	for _, c := range commits {
		if !IsFixCommit(c.Subject) {
			continue
		}
		rep.FixCommits++
		for _, f := range c.Files {
			rep.FixesByFile[f.Path]++
		}
	}
	// MostFixed: fix 数降順、同数タイは path 昇順(Deterministic output ルール)
	best, bestN := "", 0
	paths := make([]string, 0, len(rep.FixesByFile))
	for p := range rep.FixesByFile {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if n := rep.FixesByFile[p]; n > bestN {
			best, bestN = p, n
		}
	}
	rep.MostFixed = best
	return rep
}

// Validation はランキングと fix 履歴の突き合わせ結果。
type Validation struct {
	Valid             bool    `json:"valid"`
	K                 int     `json:"k"`
	Hits              int     `json:"hits"`               // 上位 K 中、fix 履歴のあるファイル数
	PrecisionAtK      float64 `json:"precision_at_k"`     // Hits / K
	BaselinePrecision float64 `json:"baseline_precision"` // ランダム順位の期待値 = fix>0 ファイル比率
	Lift              float64 `json:"lift"`               // precision / baseline(>1 で基準線超え)
	Note              string  `json:"note"`
}

// Validate はリスクランキング(危険な順のファイル path)の上位 K を fix 履歴と
// 突き合わせる。fix データが 1 件も無ければ Valid=false(偽のスコアを出さない)。
func Validate(ranking []string, fixesByFile map[string]int, k int) Validation {
	v := Validation{}
	if len(ranking) == 0 {
		v.Note = "no ranked files to validate"
		return v
	}
	// fix>0 のファイル数(ランキング対象内)
	var everFixed int
	for _, p := range ranking {
		if fixesByFile[p] > 0 {
			everFixed++
		}
	}
	if everFixed == 0 {
		v.Note = "no identifiable fix commits touch the ranked files; " +
			"cannot validate the ranking against fix history (SZZ stage 1 found nothing)"
		return v
	}
	if k > len(ranking) {
		k = len(ranking)
	}
	if k <= 0 {
		v.Note = "k must be positive"
		return v
	}
	v.K = k
	for _, p := range ranking[:k] {
		if fixesByFile[p] > 0 {
			v.Hits++
		}
	}
	v.PrecisionAtK = float64(v.Hits) / float64(k)
	v.BaselinePrecision = float64(everFixed) / float64(len(ranking))
	if v.BaselinePrecision > 0 {
		v.Lift = v.PrecisionAtK / v.BaselinePrecision
	}
	v.Valid = true
	v.Note = "precision@k of the risk ranking against files touched by fix commits " +
		"(SZZ stage 1 approximation; fixes-by-message, not bug-introducing commits)"
	return v
}
