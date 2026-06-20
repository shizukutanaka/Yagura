// Package flagarg は Go 関数の「ブール旗引数」臭を go/ast で検出する
// (ソクラテス新視点)。
//
// 動機:
//
//	complexity は分岐パスの多さ(垂直)を測り、paramcheck は引数の総数(水平)を測る。
//	しかし bool 型の引数は「どれだけ多いか」に関わらず、それ自体が設計の臭いになりうる:
//	呼び出し元で `process(data, true)` と書かれたとき、"true" が何を意味するか
//	コードを読まなければわからない(Martin Fowler 「Flag Argument」smell)。
//
//	検出ロジック:
//	  ・FuncDecl のみ対象(FuncLit = コールバック署名は除外)。
//	  ・TestXxx / BenchmarkXxx / ExampleXxx 関数はスキップ。
//	  ・_test.go ファイルの関数はすべてスキップ。
//	  ・*bool はポインタ型のため対象外(bool そのものを渡す場合のみ)。
//	  ・レシーバは計上しない。
//	  ・threshold 個以上の bool 引数で finding 生成(default 1)。
//	  ・severity: 1 bool → low / 2+ bool → medium。
//
// stdlib の go/ast のみ(ADR-0001 ゼロ依存)。型情報不要・決定論的。
package flagarg

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// defaultThreshold は bool パラメータ数の既定しきい値。
const defaultThreshold = 1

// Finding は 1 件の flag-arg 指摘またはパースエラー。
type Finding struct {
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Func       string   `json:"func,omitempty"`
	BoolParams []string `json:"bool_params,omitempty"`
	BoolCount  int      `json:"bool_count,omitempty"`
	Rule       string   `json:"rule"`
	Severity   string   `json:"severity"`
	Message    string   `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned int       `json:"files_scanned"`
	FuncsScanned int       `json:"funcs_scanned"`
	Threshold    int       `json:"threshold"`
	FlagsFound   int       `json:"flags_found"`
	Findings     []Finding `json:"findings"`
}

// Scan は files(path→content)を解析し、bool 型引数を持つ Go 関数を報告する。
// threshold<=0 は defaultThreshold(1)を使用。出力は決定論的(File→Line→Func)。
func Scan(files map[string]string, threshold int) Report {
	if threshold <= 0 {
		threshold = defaultThreshold
	}
	r := Report{Threshold: threshold}
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		r.FilesScanned++
		scanFile(path, src, threshold, &r)
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
	return r
}

func scanFile(path, src string, threshold int, r *Report) {
	// _test.go ファイルは全スキップ
	if strings.HasSuffix(path, "_test.go") {
		return
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.AllErrors)
	if err != nil {
		line := 1
		if el, ok := err.(scanner.ErrorList); ok && len(el) > 0 {
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
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Type == nil {
			continue
		}
		// TestXxx / BenchmarkXxx / ExampleXxx はスキップ
		name := fn.Name.Name
		if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") || strings.HasPrefix(name, "Example") {
			continue
		}
		r.FuncsScanned++
		boolCount, boolNames := countBoolParams(fn.Type)
		if boolCount < threshold {
			continue
		}
		r.FlagsFound++
		pos := fset.Position(fn.Pos())
		qualName := funcDeclName(fn)
		r.Findings = append(r.Findings, Finding{
			File:       path,
			Line:       pos.Line,
			Func:       qualName,
			BoolParams: boolNames,
			BoolCount:  boolCount,
			Rule:       "flag-arg",
			Severity:   flagSeverity(boolCount),
			Message:    buildMessage(qualName, boolNames, boolCount, threshold),
		})
	}
}

// countBoolParams は FuncType の引数のうち bool 型のもの(名前単位)を数える。
// *bool はポインタ型のため対象外。レシーバは含まない(FuncType.Params のみ)。
func countBoolParams(ft *ast.FuncType) (int, []string) {
	if ft.Params == nil {
		return 0, nil
	}
	var count int
	var names []string
	for _, field := range ft.Params.List {
		if !isBoolIdent(field.Type) {
			continue
		}
		if len(field.Names) == 0 {
			count++
			names = append(names, "_")
		} else {
			for _, n := range field.Names {
				count++
				names = append(names, n.Name)
			}
		}
	}
	return count, names
}

// isBoolIdent は式が裸の bool 識別子かどうかを返す(*bool は false)。
func isBoolIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "bool"
}

func flagSeverity(n int) string {
	if n >= 2 {
		return "medium"
	}
	return "low"
}

func buildMessage(funcName string, boolParams []string, count, threshold int) string {
	sb := strings.Builder{}
	sb.WriteString("function ")
	sb.WriteString(funcName)
	sb.WriteString(" has ")
	sb.WriteString(strconv.Itoa(count))
	sb.WriteString(" bool param")
	if count != 1 {
		sb.WriteString("s")
	}
	sb.WriteString(" [")
	sb.WriteString(strings.Join(boolParams, ", "))
	sb.WriteString("] (threshold ")
	sb.WriteString(strconv.Itoa(threshold))
	sb.WriteString(") — bool params encode hidden control-flow branches; consider splitting into separate functions (Fowler 'Flag Argument')")
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
