// Package hotspot は関数レベルの各レンズの findings を関数単位で重ね合わせ、
// 複数のレンズが独立して同じ関数を指摘した箇所を「ホットスポット」として報告する。
//
// ソクラテス新視点 VI(収束シグナル):個々のレンズはそれぞれ偽陽性を持つ——
// 引数 6 個の関数が妥当なこともあり、戻り値 4 個の関数が妥当なこともある。
// しかし *複数の独立したレンズが同時に* 指摘する関数は、ほぼ確実に本物の
// リファクタ対象である。独立シグナルの収束は、単一シグナルより高信頼。
//
// v0.70 時点は signature 系 4 レンズ(complexity/paramcheck/flagarg/returncheck)
// のみを束ねていたが、その後 cognit/nestdepth/typeassert/namecheck/ctxcheck/
// errwrap/nakedret/prealloc など「(Recv).Method」規約で File/Line/Func を報告
// する関数キー付きレンズが 8 つ追加され、収束母数が古いまま取り残されていた
// (v0.94 時点で hotspot は 21 レンズ中 4 つしか見ていない = 収束シグナルの
// 有効性が発足時から目減りし続けていた)。本改修でその 8 レンズを合流し、
// 収束母数を 12 レンズへ拡張する(ソクラテス新視点、hotspot 自身の陳腐化)。
// thelper は主題がテストファイルであり hotspot の非テストスコープと相容れない
// ため対象外。errdiscard/synccheck/predeclared/errpolicy は Func と同型の
// 関数キーを持たない(Caller が空になりうる/Name が識別子や型を指す)ため、
// 誤収束を避けて対象外とする。
//
// 既存レンズを再利用するだけでロジックを再実装しない(ADR-0001 / zero-dep)。
package hotspot

import (
	"fmt"
	"go/parser"
	"go/token"
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

// defaultMinLenses は「ホットスポット」と見なす最小レンズ数。
// 1 では単一レンズの findings がすべてホットスポット扱いになり無意味なので、
// 収束(2 つ以上のレンズの一致)を要求する。
const defaultMinLenses = 2

// Hotspot は複数レンズが指摘した 1 関数を表す。
type Hotspot struct {
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Func     string   `json:"func"`
	Lenses   []string `json:"lenses"` // 指摘したレンズ名(ソート済)
	Count    int      `json:"count"`  // len(Lenses)
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
}

// Report は hotspot 解析の結果。
type Report struct {
	FilesScanned int       `json:"files_scanned"`
	FuncsFlagged int       `json:"funcs_flagged"` // >=1 レンズに指摘された関数の総数
	MinLenses    int       `json:"min_lenses"`
	Hotspots     []Hotspot `json:"hotspots"` // >= MinLenses のもの、count 降順
}

// scopeFiles は hotspot 解析の対象を確定する: 拡張子 .go かつ _test.go でなく、
// go/parser でエラーなくパースできるファイルのみを残す。
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

// funcKey は (file, func) で関数を一意化する。12 レンズはいずれもメソッドを
// "(Recv).Method" 形式で命名し、FuncDecl の位置行を報告するため衝突しない。
type funcKey struct {
	file string
	fn   string
}

type acc struct {
	line   int
	lenses map[string]bool
}

// Scan は 12 個の関数キー付きレンズを同じ file set に対して既定しきい値で実行し、
// minLenses 個以上のレンズが指摘した関数をホットスポットとして返す。
// minLenses<=1 は defaultMinLenses(2) に丸める。
func Scan(files map[string]string, minLenses int) Report {
	if minLenses <= 1 {
		minLenses = defaultMinLenses
	}

	// hotspot 自身でスコープを確定する: 非テストかつパース可能な .go のみ。
	// 各サブレンズは _test.go / parse-error の扱いが微妙に異なる(complexity と
	// paramcheck は _test.go を走査する)ため、収束判定が下流レンズの個別挙動に
	// 引きずられないよう、ここで同一の file set に揃えてから委譲する。
	scoped := scopeFiles(files)

	// 各レンズを既定しきい値(threshold=0、または閾値パラメータなし)で実行。
	// すべて同じ file set を走査。
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

	by := map[funcKey]*acc{}
	record := func(file, fn string, line int, lens string) {
		if fn == "" {
			return
		}
		k := funcKey{file: file, fn: fn}
		a := by[k]
		if a == nil {
			a = &acc{line: line, lenses: map[string]bool{}}
			by[k] = a
		}
		if line < a.line || a.line == 0 {
			a.line = line
		}
		a.lenses[lens] = true
	}

	for _, f := range cRep.Findings {
		record(f.File, f.Func, f.Line, "complexity")
	}
	for _, f := range pRep.Findings {
		record(f.File, f.Func, f.Line, "paramcheck")
	}
	for _, f := range fRep.Findings {
		record(f.File, f.Func, f.Line, "flagarg")
	}
	for _, f := range rRep.Findings {
		record(f.File, f.Func, f.Line, "returncheck")
	}
	for _, f := range cogRep.Findings {
		record(f.File, f.Func, f.Line, "cognit")
	}
	for _, f := range ndRep.Findings {
		record(f.File, f.Func, f.Line, "nestdepth")
	}
	for _, f := range taRep.Findings {
		record(f.File, f.Func, f.Line, "typeassert")
	}
	for _, f := range ncRep.Findings {
		record(f.File, f.Func, f.Line, "namecheck")
	}
	for _, f := range ccRep.Findings {
		record(f.File, f.Func, f.Line, "ctxcheck")
	}
	for _, f := range ewRep.Findings {
		record(f.File, f.Func, f.Line, "errwrap")
	}
	for _, f := range nrRep.Findings {
		record(f.File, f.Func, f.Line, "nakedret")
	}
	for _, f := range prRep.Findings {
		record(f.File, f.Func, f.Line, "prealloc")
	}

	rep := Report{
		FilesScanned: len(scoped),
		FuncsFlagged: len(by),
		MinLenses:    minLenses,
	}

	for k, a := range by {
		if len(a.lenses) < minLenses {
			continue
		}
		lenses := make([]string, 0, len(a.lenses))
		for l := range a.lenses {
			lenses = append(lenses, l)
		}
		sort.Strings(lenses)
		rep.Hotspots = append(rep.Hotspots, Hotspot{
			File:     k.file,
			Line:     a.line,
			Func:     k.fn,
			Lenses:   lenses,
			Count:    len(lenses),
			Severity: severityFor(len(lenses)),
			Message:  buildMessage(k.fn, lenses),
		})
	}

	// 決定論的: count 降順 → File 昇順 → Line 昇順 → Func 昇順。
	sort.Slice(rep.Hotspots, func(i, j int) bool {
		a, b := rep.Hotspots[i], rep.Hotspots[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Func < b.Func
	})

	return rep
}

// severityFor は収束レンズ数から重大度を決める。
// 2 = medium(注目に値する収束)、3+ = high(強いリファクタ対象)。
func severityFor(count int) string {
	if count >= 3 {
		return "high"
	}
	return "medium"
}

func buildMessage(fn string, lenses []string) string {
	return fmt.Sprintf("%s flagged by %d independent lenses (%v) — convergent signal, high-confidence refactor target",
		fn, len(lenses), lenses)
}
