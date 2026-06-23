// Package typeassert は「panic しうる単一値の型アサーション」を go/ast で検出する
// (ソクラテス新視点 XVII — 暗黙の panic ハザード軸; forcetypeassert 由来)。
//
// 動機:
//
//	astcheck は library 内の *明示的* な panic() 呼出を flag する。しかし「一見無害
//	なのに実行時に panic する」*暗黙の* panic 源はどのレンズも見ていなかった。典型は
//	`v := x.(T)` ——単一値の型アサーションは x が T でなければ panic する。安全形は
//	`v, ok := x.(T)`。errwrap は *error* 型のアサーション(errors.As 推奨)を見るが、
//	本レンズは型を問わず *panic 安全性* の軸を見る(comma-ok 形は安全なので対象外)。
//	これは recognized linter forcetypeassert に相当する。
//
// 検出(go/ast、型情報なし、決定論的):
//   - unchecked-type-assert: 単一値コンテキストの `x.(T)`(comma-ok でも `x.(type)`
//     switch でもないもの)。comma-ok 形(2-LHS 代入 / 2-name var spec の RHS)は安全
//     として除外する。
//
// _test.go と TestXxx 等は対象外(テストでの強制アサーションは意図的、panic は単に
// テスト失敗になる)。stdlib の go/ast のみ(ADR-0001)。決定論的(File→Line→Func)。
package typeassert

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
)

// Finding は 1 件の unchecked アサーションまたはパースエラー。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Func     string `json:"func,omitempty"`
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

// Scan は files を解析し、panic しうる単一値型アサーションを報告する。
// 出力は決定論的(File→Line→Func)。
func Scan(files map[string]string) Report {
	r := Report{}
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		r.FilesScanned++
		scanFile(path, src, &r)
	}
	sort.Slice(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Func < b.Func
	})
	for _, f := range r.Findings {
		if f.Rule != "parse-error" {
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
			File: path, Line: line,
			Rule: "parse-error", Severity: "low",
			Message: "Go source did not parse: " + firstLine(err.Error()),
		})
	}
	if f == nil {
		return
	}

	// Pass 1: comma-ok 形のアサーション位置(= 安全)を集める。
	safe := map[token.Pos]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Lhs) == 2 && len(x.Rhs) == 1 {
				markIfAssert(x.Rhs[0], safe)
			}
		case *ast.ValueSpec:
			if len(x.Names) == 2 && len(x.Values) == 1 {
				markIfAssert(x.Values[0], safe)
			}
		}
		return true
	})

	// Pass 2: FuncDecl ごとに単一値アサーションを flag(関数名で attribution)。
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") ||
			strings.HasPrefix(name, "Example") || strings.HasPrefix(name, "Fuzz") {
			continue
		}
		fname := funcDeclName(fn)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ta, ok := n.(*ast.TypeAssertExpr)
			if !ok || ta.Type == nil { // Type==nil は x.(type) switch
				return true
			}
			if safe[ta.Pos()] {
				return true
			}
			pos := fset.Position(ta.Pos())
			r.Findings = append(r.Findings, Finding{
				File: path, Line: pos.Line, Func: fname,
				Rule: "unchecked-type-assert", Severity: "medium",
				Message: "single-value type assertion panics on mismatch — use the comma-ok form `v, ok := x.(T)` (or a type switch) to handle the failure",
			})
			return true
		})
	}
}

func markIfAssert(e ast.Expr, safe map[token.Pos]bool) {
	if ta, ok := e.(*ast.TypeAssertExpr); ok && ta.Type != nil {
		safe[ta.Pos()] = true
	}
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

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
