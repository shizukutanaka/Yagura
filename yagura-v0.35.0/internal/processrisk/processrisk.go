// Package processrisk は churn(v0.119.0)と ownership(v0.120.0)の
// **プロセス指標**だけを合成して、ファイル単位のリスクを順位付けする(v0.121.0)。
//
// 研究的根拠 — なぜ複雑度を「点数に入れない」のか:
//
//   - Rahman & Devanbu, "How, and Why, Process Metrics Are Better", ICSE 2013。
//     プロセス指標(変更履歴・寄与者情報など)と製品指標(コード複雑度など)を
//     性能・安定性・可搬性・**stasis(停滞)**の観点で比較し、製品指標は
//     プロセス指標より概して有用でないと結論。製品指標はリリース間でほとんど
//     変化しない(停滞する)ため、同じファイルを繰り返し指し続けてしまう。
//
//   - Majumder, Mody & Menzies, "Revisiting Process versus Product Metrics:
//     a Large Scale Analysis", EMSE 2022(700 プロジェクト / 722,471 コミット)。
//     上記を大規模に再検証し、最良の学習器で
//     **プロセス指標 recall 98% / AUC 95%、製品指標 recall 44% / AUC 54%**。
//     AUC 54% はほぼ偶然(50%)と変わらない。
//
// この結果は本リポジトリ自身の v0.119.0 の設計への反証でもある:
// churn.RiskScore は `相対churn × 複雑度` で、ほぼ偶然と変わらない信号に
// 乗算という同等の重みを与えていた。本パッケージでは複雑度を **表示はするが
// 採点には使わない**(TestScore_ComplexityIsReportedButNotScored が固定)。
// Tornhill の hotspot(複雑度 × 変更頻度)は独立した公表手法なので churn 側に
// 残してあり、両方の数値を見比べられる——一方を隠して都合よく見せない。
//
// 重み付けについての正直な但し書き:
//
//	「どのシグナルを採用するか」は上記研究に基づくが、**シグナル間の重み配分は
//	研究由来ではない**。恣意的なスケール混合を避けるため、各シグナルをリポジトリ内の
//	パーセンタイル順位に正規化して単純平均する。単位の選び方だけで特定シグナルが
//	支配することを防ぐのが狙いで、これ以上の主張はしない。
//
// zero-dep(ADR-0001): stdlib + internal/churn + internal/ownership のみ。
package processrisk

import (
	"fmt"
	"math"
	"sort"

	"github.com/shizukutanaka/yagura/internal/churn"
	"github.com/shizukutanaka/yagura/internal/ownership"
)

// FileRisk は 1 ファイルの合成プロセスリスク。
type FileRisk struct {
	Path string `json:"path"`

	// --- プロセス指標(採点に使う)---
	RelativeChurn float64 `json:"relative_churn"`     // Nagappan & Ball
	ChurnCount    int     `json:"churn_count"`        // 変更回数
	Ownership     float64 `json:"ownership"`          // Bird et al.(低いほど危険)
	Minor         int     `json:"minor_contributors"` // Bird et al.
	Contributors  int     `json:"contributors"`       // Bird Total / Meneely & Williams
	HasOwnership  bool    `json:"has_ownership"`      // ownership データが有ったか
	// SizeLOC は採点の入力(件数系シグナルはこれで割る)。読者が検算できるよう出す。
	SizeLOC int `json:"size_loc"`

	// --- 製品指標(表示のみ。採点に **使わない**)---
	Complexity int `json:"complexity,omitempty"`

	Score   float64  `json:"score"` // 0..1、高いほど危険
	Reasons []string `json:"reasons,omitempty"`
}

// Report は合成結果。Files は Score 降順。
type Report struct {
	Files    []FileRisk `json:"files"`
	Riskiest string     `json:"riskiest,omitempty"`
	Scored   int        `json:"scored"`
	// Note は複雑度を採点に使わない理由を応答自体に埋め込む(利用者が
	// 「複雑度が効いていない」と誤解しないため)。
	Note string `json:"note"`
}

const scoringNote = "Score uses PROCESS metrics only (relative churn, change count, ownership, " +
	"minor contributors, contributor count). Complexity is reported but deliberately excluded: " +
	"Majumder/Mody/Menzies (EMSE 2022, 700 projects) measured product metrics at AUC ~54% vs " +
	"process metrics at ~95%. Signal selection follows that research; the equal weighting across " +
	"signals is our own choice, not a research result. " +
	"COUNT SIGNALS ARE PER-LOC (change count, minor contributors, contributor count are each " +
	"divided by file size before ranking). Raw counts rank big files highly because big files " +
	"have more of everything, which looks good on precision@K and is worthless once you pay for " +
	"the lines you read. Under effort-aware evaluation (20% of LOC as the inspection budget; " +
	"Arisholm, Briand & Johannessen JSS 2010, Mende & Koschke CSMR 2010) across 8 repositories, " +
	"raw change count beat random ordering in 0 of 8, while change count per LOC beat it in 8 of 8. " +
	"HONEST LIMIT: at that budget this ranking is competitive with, but does not beat, the trivial " +
	"ManualUp baseline of simply reading the smallest files first (mean effort lift 1.61 vs 1.68 " +
	"over those 8 repositories). It earns its place by naming WHICH files, not by beating that " +
	"baseline on recall. Re-run the comparison yourself: internal/walkforward/largeapp_test.go."

// Score は churn と ownership の結果を突き合わせ、プロセス指標のみで順位付けする。
// ownership が nil / 該当なしのファイルも churn 側の指標だけで採点する(落とさない)。
func Score(chFiles []churn.FileRisk, ownFiles []ownership.FileOwnership) Report {
	rep := Report{Files: []FileRisk{}, Note: scoringNote}
	if len(chFiles) == 0 && len(ownFiles) == 0 {
		return rep
	}

	byPath := map[string]*FileRisk{}
	order := []string{}
	get := func(path string) *FileRisk {
		f, ok := byPath[path]
		if !ok {
			f = &FileRisk{Path: path}
			byPath[path] = f
			order = append(order, path)
		}
		return f
	}
	for _, c := range chFiles {
		f := get(c.Path)
		f.RelativeChurn = c.RelativeChurn
		f.ChurnCount = c.ChurnCount
		f.Complexity = c.Complexity
		f.SizeLOC = c.SizeLOC
	}
	for _, o := range ownFiles {
		f := get(o.Path)
		f.Ownership = o.Ownership
		f.Minor = o.Minor
		f.Contributors = o.Total
		f.HasOwnership = true
	}

	// 各シグナルをパーセンタイル順位へ正規化する。単位の異なる指標を素の値で
	// 足すと、スケールの大きい指標だけで順位が決まってしまうため。
	//
	// **件数系は必ず 1 行あたりに直す**(v1.85.0)。変更回数も貢献者数も、
	// 大きいファイルなら何でも多い——素で足すと「大きい」を「危険」と取り違える。
	// effort-aware 評価(読む LOC の予算 20%)を 8 リポジトリに当てた実測では、
	// 素の churn_count はランダム順を **0/8** でしか上回らないのに対し、
	// churn_count/LOC と contributors/LOC は **8/8** で上回った。
	// 直すべきは配合ではなく正規化だった。
	churnPct := percentiles(order, func(p string) float64 { return byPath[p].RelativeChurn }, true)
	countPct := percentiles(order, func(p string) float64 {
		return perLOC(float64(byPath[p].ChurnCount), byPath[p].SizeLOC)
	}, true)
	minorPct := percentiles(order, func(p string) float64 {
		return perLOC(float64(byPath[p].Minor), byPath[p].SizeLOC)
	}, true)
	contribPct := percentiles(order, func(p string) float64 {
		return perLOC(float64(byPath[p].Contributors), byPath[p].SizeLOC)
	}, true)
	// ownership は「低いほど危険」なので昇順で percentile を取る
	ownPct := percentiles(order, func(p string) float64 {
		f := byPath[p]
		if !f.HasOwnership {
			return 1.0 // データ無し = 危険側に寄せない(中立に近い最良値)
		}
		return f.Ownership
	}, false)

	for _, p := range order {
		f := byPath[p]
		signals := []float64{churnPct[p], countPct[p]}
		if f.HasOwnership {
			signals = append(signals, ownPct[p], minorPct[p], contribPct[p])
		}
		var sum float64
		for _, s := range signals {
			sum += s
		}
		f.Score = sum / float64(len(signals))
		f.Reasons = reasonsFor(f, churnPct[p], ownPct[p])
		rep.Files = append(rep.Files, *f)
	}

	sort.SliceStable(rep.Files, func(i, j int) bool {
		if rep.Files[i].Score != rep.Files[j].Score {
			return rep.Files[i].Score > rep.Files[j].Score
		}
		return rep.Files[i].Path < rep.Files[j].Path
	})
	rep.Scored = len(rep.Files)
	if len(rep.Files) > 0 {
		rep.Riskiest = rep.Files[0].Path
	}
	return rep
}

// reasonsFor は順位の根拠を人間可読な短文にする(数値だけ返して放置しない)。
func reasonsFor(f *FileRisk, churnPct, ownPct float64) []string {
	var out []string
	if churnPct >= 0.8 {
		// percentile → "top N%"。最上位が "top 0%" にならないよう下限 1 を敷く。
		topPct := math.Max(1, math.Round((1-churnPct)*100))
		out = append(out, fmt.Sprintf("relative churn in the top %.0f%% of this repo (%.2f)",
			topPct, f.RelativeChurn))
	}
	if f.ChurnCount >= 10 {
		out = append(out, fmt.Sprintf("changed %d times in the analyzed window", f.ChurnCount))
	}
	if f.HasOwnership {
		if f.Ownership < 0.5 {
			out = append(out, fmt.Sprintf("no clear owner (top contributor holds %.0f%%)", f.Ownership*100))
		}
		if f.Minor > 0 {
			out = append(out, fmt.Sprintf("%d minor contributor(s) below the 5%% threshold (Bird et al.)", f.Minor))
		}
		if f.Contributors > 9 {
			out = append(out, fmt.Sprintf("%d contributors (Meneely & Williams: >9 associates with vulnerabilities)", f.Contributors))
		}
	}
	return out
}

// percentiles は値をリポジトリ内の順位 [0,1] に写す。desc=true なら大きい値ほど 1。
// 同値は同じ percentile になる(順序の恣意性を排除)。
func percentiles(keys []string, val func(string) float64, desc bool) map[string]float64 {
	n := len(keys)
	out := make(map[string]float64, n)
	if n == 0 {
		return out
	}
	if n == 1 {
		out[keys[0]] = 0.5 // 比較対象が無いので中立
		return out
	}
	sorted := append([]string(nil), keys...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := val(sorted[i]), val(sorted[j])
		if a == b {
			return sorted[i] < sorted[j]
		}
		if desc {
			return a < b // 昇順に並べ、後ろほど高 percentile
		}
		return a > b
	})
	// 同値には同じ percentile を与える
	i := 0
	for i < len(sorted) {
		j := i
		for j+1 < len(sorted) && val(sorted[j+1]) == val(sorted[i]) {
			j++
		}
		pct := float64(i+j) / 2 / float64(n-1)
		for k := i; k <= j; k++ {
			out[sorted[k]] = pct
		}
		i = j + 1
	}
	return out
}

// perLOC は件数を「1 行あたり」に直す。SizeLOC が不明(0)なら 0 を返す——
// 未知を最大にすると測っていないファイルが上位を占め、最小にすると黙って捨てることに
// なる。0 は「密度の証拠が無い」の位置であって、他の証拠(relative_churn や
// ownership)があればそちらで拾われる。
func perLOC(v float64, loc int) float64 {
	if loc <= 0 {
		return 0
	}
	return v / float64(loc)
}
