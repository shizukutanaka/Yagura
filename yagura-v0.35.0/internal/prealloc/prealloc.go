// Package prealloc は range ループ内で事前確保なしに append されるスライスを
// go/ast で検出する *パフォーマンス軸* のレンズ(ソクラテス新視点 XIX、Qiita/Zenn 調査、
// alexkohler/prealloc 由来)。
//
// 動機:
//
//	既存の ~19 レンズはすべて correctness / readability / safety / architecture を
//	測る——だが「このコードは *無駄に遅くないか*」を問うレンズは 1 つも無かった。
//	Go で最も広く知られた性能アンチパターンが、長さの分かっているコレクションを
//	range しながら、空スライスへ append し続ける形:
//
//	    var out []int                  // 容量 0
//	    for _, x := range xs {
//	        out = append(out, x)       // 容量超過のたび再確保 + コピー
//	    }
//
//	xs の長さは既知なので `out := make([]int, 0, len(xs))` で 1 度だけ確保すれば、
//	再確保とコピー(と GC 圧)を消せる。本レンズはこの機会を可視化する。
//
// 偽陽性を避けるため保守的に振る舞う(alexkohler/prealloc の既定と同じ):
//   - *range* ループのみ(プレーン `for` は反復数が静的に不明なので対象外)。
//   - append はループ本体の *トップレベル* 文のみ(条件分岐の中の append は
//     回数不定なので対象外)。
//   - 対象スライスはループ *より前* に空(`var s []T` / `s := []T{}` /
//     `make([]T, 0)`)で宣言されたもの限定。`make([]T, 0, n)` や `make([]T, n)` は
//     既に確保済みなので flag しない。
//
// _test.go と TestXxx/BenchmarkXxx/ExampleXxx/FuzzXxx は除外。
// stdlib の go/ast のみ(ADR-0001 ゼロ依存)。型情報不要・決定論的(File→Line→Func→Name)。
package prealloc

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
)

// Finding は 1 件の prealloc 機会またはパースエラー。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Func     string `json:"func,omitempty"`
	Name     string `json:"name,omitempty"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned int       `json:"files_scanned"`
	Flagged      int       `json:"flagged"`
	Findings     []Finding `json:"findings"`
}

// Scan は files(path→content)を解析し、prealloc できるスライスを報告する。
// 出力は決定論的(File→Line→Func→Name)。
func Scan(files map[string]string) Report {
	r := Report{}
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		r.FilesScanned++
		scanFile(path, src, &r)
	}
	sort.Slice(r.Findings, func(i, j int) bool { return lessFinding(r.Findings[i], r.Findings[j]) })
	for _, f := range r.Findings {
		if f.Rule == "prealloc-candidate" {
			r.Flagged++
		}
	}
	return r
}

func scanFile(path, src string, r *Report) {
	if strings.HasSuffix(path, "_test.go") {
		return
	}
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
		if !ok || fn.Body == nil {
			continue
		}
		if isTestFunc(fn.Name.Name) {
			continue
		}
		scanFunc(path, fset, fn, r)
	}
}

func scanFunc(path string, fset *token.FileSet, fn *ast.FuncDecl, r *Report) {
	// pass 1: 関数内で空スライス宣言された名前 → 最初の宣言位置。
	empties := collectEmptySlices(fn.Body)
	if len(empties) == 0 {
		return
	}
	// pass 2: range ループのトップレベル append を検査。
	name := funcDeclName(fn)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		rng, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		for _, slice := range topLevelAppendTargets(rng.Body) {
			declPos, isEmpty := empties[slice]
			if !isEmpty || declPos >= rng.Pos() {
				continue // ループより後の宣言 / 非空スライスは対象外
			}
			pos := fset.Position(rng.Pos())
			r.Findings = append(r.Findings, Finding{
				File: path, Line: pos.Line, Func: name, Name: slice,
				Rule: "prealloc-candidate", Severity: "medium",
				Message: "slice '" + slice + "' is grown by append inside a range loop without preallocation" +
					" — declare it as make([]T, 0, len(<range>)) before the loop to avoid repeated reallocation/copy",
			})
		}
		return true
	})
}

// collectEmptySlices は関数本体から空(容量 0)スライス宣言を集める。
// var s []T / s := []T{} / s := make([]T, 0) を対象とし、make([]T, 0, n) や
// make([]T, n) のような確保済みは除外する。返り値は 名前→最初の宣言位置。
func collectEmptySlices(body *ast.BlockStmt) map[string]token.Pos {
	out := map[string]token.Pos{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.DeclStmt:
			gd, ok := s.Decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Values) != 0 {
					continue // 初期化子があるものは別形(var s = ...)で扱う
				}
				if !isSliceType(vs.Type) {
					continue
				}
				for _, nm := range vs.Names {
					recordEmpty(out, nm)
				}
			}
		case *ast.AssignStmt:
			if s.Tok != token.DEFINE || len(s.Lhs) != len(s.Rhs) {
				return true
			}
			for i, lhs := range s.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if isEmptySliceExpr(s.Rhs[i]) {
					recordEmpty(out, id)
				}
			}
		}
		return true
	})
	return out
}

func recordEmpty(out map[string]token.Pos, id *ast.Ident) {
	if id.Name == "_" {
		return
	}
	if _, seen := out[id.Name]; !seen {
		out[id.Name] = id.Pos()
	}
}

// isEmptySliceExpr は []T{}(空)または make([]T, 0)(cap 指定なし)を判定する。
func isEmptySliceExpr(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.CompositeLit:
		return isSliceType(v.Type) && len(v.Elts) == 0
	case *ast.CallExpr:
		id, ok := v.Fun.(*ast.Ident)
		if !ok || id.Name != "make" || len(v.Args) == 0 {
			return false
		}
		if !isSliceType(v.Args[0]) {
			return false
		}
		// make([]T) は不正だが、引数 1 のみは len 未指定 → 空扱い。
		if len(v.Args) == 1 {
			return true
		}
		// make([]T, 0) のみ空。len!=0 や cap 指定(3 引数)は確保済みとみなす。
		return len(v.Args) == 2 && isZeroLiteral(v.Args[1])
	}
	return false
}

func isSliceType(e ast.Expr) bool {
	at, ok := e.(*ast.ArrayType)
	return ok && at.Len == nil // [N]T(配列)は除外、[]T(スライス)のみ
}

func isZeroLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.INT && lit.Value == "0"
}

// topLevelAppendTargets はループ本体のトップレベル文 `s = append(s, ...)` から
// スライス名 s を集める(条件分岐の中など、ネストした append は対象外)。
func topLevelAppendTargets(body *ast.BlockStmt) []string {
	var out []string
	seen := map[string]bool{}
	for _, stmt := range body.List {
		as, ok := stmt.(*ast.AssignStmt)
		if !ok || as.Tok != token.ASSIGN || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			continue
		}
		lhs, ok := as.Lhs[0].(*ast.Ident)
		if !ok {
			continue
		}
		if name := appendTarget(as.Rhs[0]); name == lhs.Name && name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// appendTarget は call が append(x, ...) のとき x の識別子名を返す(でなければ "")。
func appendTarget(e ast.Expr) string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return ""
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok || id.Name != "append" || len(call.Args) == 0 {
		return ""
	}
	arg, ok := call.Args[0].(*ast.Ident)
	if !ok {
		return ""
	}
	return arg.Name
}

func isTestFunc(name string) bool {
	return strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") ||
		strings.HasPrefix(name, "Example") || strings.HasPrefix(name, "Fuzz")
}

func funcDeclName(fd *ast.FuncDecl) string {
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		return "(" + recvTypeName(fd.Recv.List[0].Type) + ")." + fd.Name.Name
	}
	return fd.Name.Name
}

func recvTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return "*" + recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvTypeName(t.X)
	case *ast.IndexListExpr:
		return recvTypeName(t.X)
	default:
		return "?"
	}
}

func lessFinding(a, b Finding) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	if a.Func != b.Func {
		return a.Func < b.Func
	}
	return a.Name < b.Name
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
