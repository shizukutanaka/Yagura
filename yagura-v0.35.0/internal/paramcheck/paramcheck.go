// Package paramcheck は Go 関数のパラメータ数を go/ast で計測する
// (ソクラテス新視点)。
//
// 動機:
//
//	complexity は「関数の中の分岐パスがいくつあるか」(垂直方向の複雑さ)を測る。
//	しかし「関数の入口がどれだけ広いか」(水平方向 = 引数の数)は別の軸であり、
//	どのレンズも測っていなかった。Fowler の "Long Parameter List" smell:
//	引数が多い関数は (1) 呼び出し側が順序・型を取り違えやすく、(2) 関連データが
//	構造体に括られていない設計の臭いで、(3) cyclomatic を下げる「関数抽出」が
//	実は引数列にツケを回しているだけ、という退行を隠す。
//
//	この最後の点が重要: complexity だけを gate にすると、巨大関数をヘルパに割って
//	複雑度を下げつつ、6 個 7 個と引数を引き回すヘルパを量産できてしまう。paramcheck は
//	その盲点を塞ぐ「complexity の水平方向の対」である。
//
//	計数規約: 引数は *名前単位* で数える(`a, b int` は 2)。可変長 `...T` は 1。
//	レシーバは除外(メソッド本来の引数だけを見る)。blank `_` も 1 として数える
//	(呼び出し側の位置は埋まるため)。本体を持つ FuncDecl のみ対象で、入れ子 FuncLit
//	(コールバック署名 = 外部都合で決まり設計の臭いではない)は計上しない。
//
// stdlib の go/parser + go/ast のみ(ADR-0001 ゼロ依存)。型情報不要・決定論的。
package paramcheck

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// defaultThreshold は findings/gate の既定のパラメータ数しきい値。
// 6 個以上を「長い」とみなす(threshold=5 で c>5 を flag、exclusive)。
const defaultThreshold = 5

// FuncParams は 1 関数のパラメータ数。
type FuncParams struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Func   string `json:"func"`
	Params int    `json:"params"`
}

// Finding はしきい値超過(または parse error)の 1 件。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Func     string `json:"func,omitempty"`
	Params   int    `json:"params,omitempty"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned  int          `json:"files_scanned"`
	Threshold     int          `json:"threshold"`
	Functions     []FuncParams `json:"functions"`
	MaxParams     int          `json:"max_params"`
	AvgParams     float64      `json:"avg_params"`
	OverThreshold int          `json:"over_threshold"`
	Findings      []Finding    `json:"findings"`
}

// Scan は files(path→content)の Go 関数を解析する。threshold<=0 は既定値 5。
// 出力は決定論的(File→Line→Func でソート)。
func Scan(files map[string]string, threshold int) Report {
	if threshold <= 0 {
		threshold = defaultThreshold
	}
	r := Report{FilesScanned: len(files), Threshold: threshold}
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			r.FilesScanned--
			continue
		}
		scanFile(path, src, threshold, &r)
	}
	sortFuncs(r.Functions)
	sortFindings(r.Findings)
	var sum int
	for _, f := range r.Functions {
		sum += f.Params
		if f.Params > r.MaxParams {
			r.MaxParams = f.Params
		}
	}
	if n := len(r.Functions); n > 0 {
		r.AvgParams = float64(sum) / float64(n)
	}
	return r
}

func scanFile(path, src string, threshold int, r *Report) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.AllErrors)
	if err != nil {
		line := 1
		var el scanner.ErrorList
		if errors.As(err, &el) && len(el) > 0 {
			line = el[0].Pos.Line
		}
		r.Findings = append(r.Findings, Finding{
			File: path, Line: line, Rule: "parse-error", Severity: "low",
			Message: "Go source did not parse: " + firstLine(err.Error()),
		})
	}
	if f == nil {
		return
	}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Type == nil {
			continue
		}
		n := countParams(fn.Type)
		p := fset.Position(fn.Pos())
		r.Functions = append(r.Functions, FuncParams{
			File: path, Line: p.Line, Func: funcDeclName(fn), Params: n,
		})
		if n > threshold {
			r.OverThreshold++
			r.Findings = append(r.Findings, Finding{
				File: path, Line: p.Line, Func: funcDeclName(fn), Params: n,
				Rule: "long-param-list", Severity: paramSeverity(n),
				Message: "function takes " + strconv.Itoa(n) + " parameters (threshold " + strconv.Itoa(threshold) +
					") — group related params into a struct (Fowler 'Long Parameter List'); " +
					"watch for complexity refactors that merely shift load onto the argument list",
			})
		}
	}
}

// countParams は FuncType の引数を名前単位で数える。可変長は 1。
// 名前なし引数(interface 風 `func(int, string)`)は 1 group=1 個として数える。
func countParams(ft *ast.FuncType) int {
	if ft.Params == nil {
		return 0
	}
	n := 0
	for _, field := range ft.Params.List {
		if len(field.Names) == 0 {
			n++ // 名前なし: 1 group = 1 引数
			continue
		}
		n += len(field.Names) // `a, b int` = 2
	}
	return n
}

func paramSeverity(n int) string {
	if n >= 8 {
		return "high"
	}
	return "medium"
}

func funcDeclName(fd *ast.FuncDecl) string {
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		return "(" + typeName(fd.Recv.List[0].Type) + ")." + fd.Name.Name
	}
	return fd.Name.Name
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return "*" + typeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver T[P]
		return typeName(t.X)
	case *ast.IndexListExpr:
		return typeName(t.X)
	default:
		return "?"
	}
}

func sortFuncs(fs []FuncParams) {
	sort.Slice(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Func < b.Func
	})
}

func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Func != b.Func {
			return a.Func < b.Func
		}
		return a.Rule < b.Rule
	})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
