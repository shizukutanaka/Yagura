// Package complexity は Go 関数の循環的複雑度(McCabe)を go/ast で計測する
// (ソクラテス新視点 v0.36)。
//
// 動機:
//
//	assertcheck は「テストが何かを主張しているか」を、coverage は「どれだけ見たか」を
//	測る――いずれも検証の *結果* の軸。しかし「そもそもこのコードは推論し、完全に
//	テストできるか?」という *前提条件* の軸はどのレンズも測っていない。
//
//	循環的複雑度 = 関数を通る線形独立なパスの最小数 = 全パスを網羅するのに必要な
//	テストケースの下限。複雑度がテスト数を大きく上回る関数は、assertion がいくら
//	あってもパスが構造的に未踏のまま残る潜在バグの貯水池。complexity は testability の
//	前提条件を数値化する。
//
//	計数は gocyclo 互換: base 1 + {if, for, range, case, comm-clause, && , ||} 各 +1。
//	nested FuncLit は別関数として独立に計上(errpolicy と同じ流儀)。
//
// stdlib の go/parser + go/ast のみ(ADR-0001 ゼロ依存)。型情報不要・決定論的。
package complexity

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

// defaultThreshold は findings/gate の既定の複雑度しきい値(gocyclo 慣習)。
const defaultThreshold = 10

// FuncComplexity は 1 関数の複雑度。
type FuncComplexity struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Func       string `json:"func"`
	Complexity int    `json:"complexity"`
}

// Finding はしきい値超過(または parse error)の 1 件。
type Finding struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Func       string `json:"func,omitempty"`
	Complexity int    `json:"complexity,omitempty"`
	Rule       string `json:"rule"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned  int              `json:"files_scanned"`
	Threshold     int              `json:"threshold"`
	Functions     []FuncComplexity `json:"functions"`
	MaxComplexity int              `json:"max_complexity"`
	AvgComplexity float64          `json:"avg_complexity"`
	OverThreshold int              `json:"over_threshold"`
	Findings      []Finding        `json:"findings"`
}

// Scan は files(path→content)の Go 関数を解析する。threshold<=0 は既定値 10。
// 出力は決定論的(File→Line→Func でソート)。
func Scan(files map[string]string, threshold int) Report {
	if threshold <= 0 {
		threshold = defaultThreshold
	}
	r := Report{FilesScanned: len(files), Threshold: threshold}
	var sum int
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			r.FilesScanned--
			continue
		}
		scanFile(path, src, threshold, &r)
	}
	sort.Slice(r.Functions, func(i, j int) bool {
		a, b := r.Functions[i], r.Functions[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Func < b.Func
	})
	sortFindings(r.Findings)
	for _, f := range r.Functions {
		sum += f.Complexity
		if f.Complexity > r.MaxComplexity {
			r.MaxComplexity = f.Complexity
		}
	}
	if n := len(r.Functions); n > 0 {
		r.AvgComplexity = float64(sum) / float64(n)
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

	ast.Inspect(f, func(n ast.Node) bool {
		var body *ast.BlockStmt
		var name string
		var pos token.Pos
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body == nil {
				return true
			}
			body, name, pos = fn.Body, funcDeclName(fn), fn.Pos()
		case *ast.FuncLit:
			body, name, pos = fn.Body, "func@"+lineStr(fset, fn.Pos()), fn.Pos()
		default:
			return true
		}
		c := complexityOf(body)
		p := fset.Position(pos)
		fc := FuncComplexity{File: path, Line: p.Line, Func: name, Complexity: c}
		r.Functions = append(r.Functions, fc)
		if c > threshold {
			r.OverThreshold++
			r.Findings = append(r.Findings, Finding{
				File: path, Line: p.Line, Func: name, Complexity: c,
				Rule: "high-complexity", Severity: severityFor(c),
				Message: "cyclomatic complexity " + strconv.Itoa(c) + " exceeds threshold " + strconv.Itoa(threshold) +
					" — needs >=" + strconv.Itoa(c) + " test cases for full path coverage; consider decomposing",
			})
		}
		return true
	})
}

// complexityOf は body の循環的複雑度を返す(nested FuncLit には降りない)。
func complexityOf(body *ast.BlockStmt) int {
	c := 1
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return false // 入れ子クロージャは別関数として計上
		case *ast.IfStmt:
			c++
		case *ast.ForStmt:
			c++
		case *ast.RangeStmt:
			c++
		case *ast.CaseClause:
			c++
		case *ast.CommClause:
			c++
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				c++
			}
		}
		return true
	})
	return c
}

func severityFor(c int) string {
	if c > 20 {
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

func lineStr(fset *token.FileSet, pos token.Pos) string {
	return strconv.Itoa(fset.Position(pos).Line)
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
