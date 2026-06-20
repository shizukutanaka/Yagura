// Package returncheck は Go 関数の戻り値の数を go/ast で計測する
// (ソクラテス新視点)。
//
// 動機:
//
//	paramcheck は関数の入口の広さ(引数の数)を測り、flagarg は引数の意味的制御結合
//	(bool 旗引数)を測る。しかし「関数が何を返すか」の広さ——出口の幅——は別の軸である。
//	Go の慣用句 `(T, error)` は 2 戻り値で問題ない。`(T1, T2, error)` も許容範囲。
//	しかし `(T1, T2, T3, error)` は「この関数がやりすぎている」臭いになりうる:
//	呼び出し元で 4 つの変数を受け取り、一部だけを使うならそれは設計の問題である。
//
//	paramcheck が入口(引数)を測るなら、returncheck は出口(戻り値)を測る:
//	「関数のシグネチャ全体像」を input/output/semantics の三軸で把握できる。
//
//	計数規約: 戻り値は *名前または型単位* で数える(`a, b string` は 2)。
//	FuncDecl のみ対象で、FuncLit は除外。TestXxx/BenchmarkXxx/ExampleXxx はスキップ。
//	_test.go ファイルはすべてスキップ。
//
// stdlib の go/ast のみ(ADR-0001 ゼロ依存)。型情報不要・決定論的。
package returncheck

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// defaultThreshold は既定のしきい値。
// Go 慣用句 `(T1, T2, error)` = 3 は許容範囲とみなし、4 以上を flag する。
const defaultThreshold = 3

// Finding は 1 件の many-returns 指摘またはパースエラー。
type Finding struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Func        string `json:"func,omitempty"`
	ReturnCount int    `json:"return_count,omitempty"`
	Rule        string `json:"rule"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned   int       `json:"files_scanned"`
	FuncsScanned   int       `json:"funcs_scanned"`
	Threshold      int       `json:"threshold"`
	TooManyReturns int       `json:"too_many_returns"`
	MaxReturns     int       `json:"max_returns"`
	AvgReturns     float64   `json:"avg_returns"`
	Findings       []Finding `json:"findings"`
}

// Scan は files(path→content)を解析し、戻り値が多すぎる Go 関数を報告する。
// threshold<=0 は defaultThreshold(3)を使用。出力は決定論的(File→Line→Func)。
func Scan(files map[string]string, threshold int) Report {
	if threshold <= 0 {
		threshold = defaultThreshold
	}
	r := Report{Threshold: threshold}
	var totalReturns, funcCount int
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		r.FilesScanned++
		fr := scanFile(path, src, threshold)
		r.Findings = append(r.Findings, fr.Findings...)
		r.FuncsScanned += fr.FuncsScanned
		r.TooManyReturns += fr.TooManyReturns
		if fr.MaxReturns > r.MaxReturns {
			r.MaxReturns = fr.MaxReturns
		}
		totalReturns += fr.TotalReturns
		funcCount += fr.FuncCount
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
	if funcCount > 0 {
		r.AvgReturns = float64(totalReturns) / float64(funcCount)
	}
	return r
}

type fileScanResult struct {
	Findings       []Finding
	FuncsScanned   int
	TooManyReturns int
	MaxReturns     int
	TotalReturns   int
	FuncCount      int
}

func scanFile(path, src string, threshold int) fileScanResult {
	var res fileScanResult
	if strings.HasSuffix(path, "_test.go") {
		return res
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.AllErrors)
	if err != nil {
		line := 1
		if el, ok := err.(scanner.ErrorList); ok && len(el) > 0 {
			line = el[0].Pos.Line
		}
		res.Findings = append(res.Findings, Finding{
			File: path, Line: line,
			Rule: "parse-error", Severity: "low",
			Message: "Go source did not parse: " + firstLine(err.Error()),
		})
	}
	if f == nil {
		return res
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Type == nil {
			continue
		}
		name := fn.Name.Name
		if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") || strings.HasPrefix(name, "Example") {
			continue
		}
		res.FuncsScanned++
		n := countReturns(fn.Type)
		res.FuncCount++
		res.TotalReturns += n
		if n > res.MaxReturns {
			res.MaxReturns = n
		}
		if n <= threshold {
			continue
		}
		res.TooManyReturns++
		pos := fset.Position(fn.Pos())
		qualName := funcDeclName(fn)
		res.Findings = append(res.Findings, Finding{
			File:        path,
			Line:        pos.Line,
			Func:        qualName,
			ReturnCount: n,
			Rule:        "many-returns",
			Severity:    returnSeverity(n),
			Message:     buildMessage(qualName, n, threshold),
		})
	}
	return res
}

// countReturns は FuncType の Results フィールドの戻り値を名前または型単位で数える。
// `a, b string` は 2、`string` (匿名) は 1。
func countReturns(ft *ast.FuncType) int {
	if ft.Results == nil {
		return 0
	}
	n := 0
	for _, field := range ft.Results.List {
		if len(field.Names) == 0 {
			n++ // 型のみ(匿名): 1
		} else {
			n += len(field.Names) // `a, b string` = 2
		}
	}
	return n
}

func returnSeverity(n int) string {
	if n >= 6 {
		return "medium"
	}
	return "low"
}

func buildMessage(funcName string, count, threshold int) string {
	sb := strings.Builder{}
	sb.WriteString("function ")
	sb.WriteString(funcName)
	sb.WriteString(" has ")
	sb.WriteString(strconv.Itoa(count))
	sb.WriteString(" return value")
	if count != 1 {
		sb.WriteString("s")
	}
	sb.WriteString(" (threshold ")
	sb.WriteString(strconv.Itoa(threshold))
	sb.WriteString(") — many returns may indicate the function does too much; consider splitting or grouping into a struct")
	return sb.String()
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
