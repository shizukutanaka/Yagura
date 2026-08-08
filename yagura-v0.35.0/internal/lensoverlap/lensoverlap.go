// Package lensoverlap は関数キー付きレンズ(hotspot が束ねる 12 レンズ)同士の
// 「指摘対象の重なり」を Jaccard 係数で計測する *メタ軸* のレンズ(ソクラテス式
// 自己監査、v0.99.1 での問答の直接の帰結)。
//
// 動機:
//
//	internal/selfimprove は Darwin Gödel Machine の「produce → trial → select」を
//	明示的に引用し、skill には retire 提案(harness の skill-audit)がある。しかし
//	quality lens 自身にはその "select"(淘汰・統合)の仕組みが一つもない。
//	complexity(McCabe)/cognit(認知的複雑度)/nestdepth(ネスト深度)は、いずれも
//	「関数がどれだけ理解しにくいか」の異なる切り口だと謳うが、実際にどれだけ
//	相関しているか(≒本当に独立した軸として機能しているか)を検証する手段が
//	無いまま増え続けていた——これは v0.95 で hotspot 自身の陳腐化を発見したのと
//	同じ構造の盲点: 合成(synthesis)の仕組み自体が経験的検証を経ていない。
//
// アプローチ: 各レンズが指摘した (file, func) の集合を求め、レンズ対ごとに
// Jaccard 係数(交差 / 和集合)を計算する。1.0 に近いほど「常に同じ関数を
// 一緒に指摘する」= 統合候補、0 に近いほど「独立した軸として機能している」
// ことの実証になる。統合すべきか否かの判断は本レンズの役目ではなく、
// 判断材料(相関の実測値)を提供するだけ(observability、pass/fail gate ではない)。
//
// hotspot と同じ 12 レンズ(complexity/paramcheck/flagarg/returncheck/cognit/
// nestdepth/typeassert/namecheck/ctxcheck/errwrap/nakedret/prealloc)・同じ
// スコープ規約(非テストかつパース可能な .go のみ)を再利用し、ロジックを
// 再実装しない(ADR-0001 / zero-dep)。
package lensoverlap

import (
	"go/parser"
	"go/token"
	"math"
	"sort"
	"strings"

	"github.com/shizukutanaka/yagura/internal/cognit"
	"github.com/shizukutanaka/yagura/internal/complexity"
	"github.com/shizukutanaka/yagura/internal/ctxcheck"
	"github.com/shizukutanaka/yagura/internal/errwrap"
	"github.com/shizukutanaka/yagura/internal/flagarg"
	"github.com/shizukutanaka/yagura/internal/nakedret"
	"github.com/shizukutanaka/yagura/internal/namecheck"
	"github.com/shizukutanaka/yagura/internal/nestdepth"
	"github.com/shizukutanaka/yagura/internal/paramcheck"
	"github.com/shizukutanaka/yagura/internal/prealloc"
	"github.com/shizukutanaka/yagura/internal/returncheck"
	"github.com/shizukutanaka/yagura/internal/typeassert"
)

// highThreshold/mediumThreshold は Jaccard 係数の severity 分類(慣習値、
// interfacebloat の閾値 10 などと同様に「規約であって導出値ではない」——
// calibrate 的な corpus-derived 校正は将来の改善候補)。
const (
	highThreshold   = 0.7
	mediumThreshold = 0.4
)

// lensNames は比較対象の 12 レンズ(hotspot が束ねるものと同一)。
var lensNames = []string{
	"complexity", "paramcheck", "flagarg", "returncheck",
	"cognit", "nestdepth", "typeassert", "namecheck",
	"ctxcheck", "errwrap", "nakedret", "prealloc",
}

// funcKey は (file, func) で関数を一意化する(hotspot と同じ規約)。
type funcKey struct {
	file string
	fn   string
}

// Pair は 2 レンズ間の重なり計測結果。
type Pair struct {
	LensA        string  `json:"lens_a"`
	LensB        string  `json:"lens_b"`
	FlaggedA     int     `json:"flagged_a"`
	FlaggedB     int     `json:"flagged_b"`
	Intersection int     `json:"intersection"`
	Union        int     `json:"union"`
	Jaccard      float64 `json:"jaccard"`
	Severity     string  `json:"severity,omitempty"` // "" / medium / high(統合候補シグナル)
}

// Report は lensoverlap 解析の結果。
type Report struct {
	FilesScanned   int    `json:"files_scanned"`
	LensesCompared int    `json:"lenses_compared"`
	Pairs          []Pair `json:"pairs"` // Jaccard 降順、同値は LensA→LensB 昇順
	HighOverlap    int    `json:"high_overlap"`
	MediumOverlap  int    `json:"medium_overlap"`
}

// scopeFiles は非テストかつパース可能な .go のみを残す(hotspot と同一規約:
// 各サブレンズの _test.go 扱いの違いに引きずられないよう、ここで揃える)。
func scopeFiles(files map[string]string) map[string]string {
	out := make(map[string]string, len(files))
	for name, src := range files {
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, err := parser.ParseFile(token.NewFileSet(), name, src, 0); err != nil {
			continue
		}
		out[name] = src
	}
	return out
}

// Scan は 12 レンズを同じ file set に対して既定しきい値で実行し、レンズ対ごとの
// Jaccard 重なりを報告する。決定論的(Jaccard 降順 → LensA → LensB)。
func Scan(files map[string]string) Report {
	scoped := scopeFiles(files)

	cRep := complexity.Scan(scoped, 0)
	pRep := paramcheck.Scan(scoped, 0)
	fRep := flagarg.Scan(scoped, 0)
	rRep := returncheck.Scan(scoped, 0)
	cogRep := cognit.Scan(scoped, 0)
	ndRep := nestdepth.Scan(scoped, 0)
	taRep := typeassert.Scan(scoped)
	ncRep := namecheck.Scan(scoped)
	ccRep := ctxcheck.Scan(scoped)
	ewRep := errwrap.Scan(scoped)
	nrRep := nakedret.Scan(scoped, 0)
	prRep := prealloc.Scan(scoped)

	flagged := map[string]map[funcKey]bool{}
	add := func(lens, file, fn string) {
		if fn == "" {
			return
		}
		if flagged[lens] == nil {
			flagged[lens] = map[funcKey]bool{}
		}
		flagged[lens][funcKey{file: file, fn: fn}] = true
	}
	for _, f := range cRep.Findings {
		add("complexity", f.File, f.Func)
	}
	for _, f := range pRep.Findings {
		add("paramcheck", f.File, f.Func)
	}
	for _, f := range fRep.Findings {
		add("flagarg", f.File, f.Func)
	}
	for _, f := range rRep.Findings {
		add("returncheck", f.File, f.Func)
	}
	for _, f := range cogRep.Findings {
		add("cognit", f.File, f.Func)
	}
	for _, f := range ndRep.Findings {
		add("nestdepth", f.File, f.Func)
	}
	for _, f := range taRep.Findings {
		add("typeassert", f.File, f.Func)
	}
	for _, f := range ncRep.Findings {
		add("namecheck", f.File, f.Func)
	}
	for _, f := range ccRep.Findings {
		add("ctxcheck", f.File, f.Func)
	}
	for _, f := range ewRep.Findings {
		add("errwrap", f.File, f.Func)
	}
	for _, f := range nrRep.Findings {
		add("nakedret", f.File, f.Func)
	}
	for _, f := range prRep.Findings {
		add("prealloc", f.File, f.Func)
	}
	// 全 12 レンズを常に登場させる(findings 0 件でも空集合として比較対象に含める)。
	for _, n := range lensNames {
		if flagged[n] == nil {
			flagged[n] = map[funcKey]bool{}
		}
	}

	pairs := overlapStats(flagged)
	rep := Report{
		FilesScanned:   len(scoped),
		LensesCompared: len(lensNames),
		Pairs:          pairs,
	}
	for _, p := range pairs {
		switch p.Severity {
		case "high":
			rep.HighOverlap++
		case "medium":
			rep.MediumOverlap++
		}
	}
	return rep
}

// overlapStats はレンズ名→指摘関数集合から全レンズ対の Jaccard 係数を計算する
// (純粋関数、go/ast 非依存)。決定論的: レンズ名をソートしてから総当たりし、
// 結果は Jaccard 降順 → LensA 昇順 → LensB 昇順。
func overlapStats(flagged map[string]map[funcKey]bool) []Pair {
	names := make([]string, 0, len(flagged))
	for n := range flagged {
		names = append(names, n)
	}
	sort.Strings(names)

	var pairs []Pair
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			a, b := names[i], names[j]
			setA, setB := flagged[a], flagged[b]
			inter := 0
			for k := range setA {
				if setB[k] {
					inter++
				}
			}
			union := len(setA) + len(setB) - inter
			var jac float64
			if union > 0 {
				jac = round2(float64(inter) / float64(union))
			}
			pairs = append(pairs, Pair{
				LensA: a, LensB: b,
				FlaggedA: len(setA), FlaggedB: len(setB),
				Intersection: inter, Union: union,
				Jaccard:  jac,
				Severity: severityFor(jac),
			})
		}
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Jaccard != pairs[j].Jaccard {
			return pairs[i].Jaccard > pairs[j].Jaccard
		}
		if pairs[i].LensA != pairs[j].LensA {
			return pairs[i].LensA < pairs[j].LensA
		}
		return pairs[i].LensB < pairs[j].LensB
	})
	return pairs
}

func severityFor(jac float64) string {
	switch {
	case jac >= highThreshold:
		return "high"
	case jac >= mediumThreshold:
		return "medium"
	default:
		return ""
	}
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
