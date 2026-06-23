// Package nestdepth は関数の最大ネスト深度(制御構文の入れ子の深さ)を go/ast で
// 計測する(ソクラテス新視点 XV)。
//
// 動機:
//
//	complexity(McCabe)は分岐パスの *数*(breadth)を測る。しかし complexity が
//	同じ「4」でも、4 つの flat な guard clause と、4 段に入れ子になった
//	`if{ for{ if{ if{}}}}`(pyramid of doom)では可読性がまるで違う。complexity は
//	両者を区別しない。ネストの *深さ*(depth)は complexity と直交する軸であり、
//	guard-clause / early-return リファクタは complexity を保ったまま深さだけを下げる
//	——その価値は complexity だけを gate にしても見えない。本レンズはその盲点を塞ぐ。
//
// 規約(cognitive-complexity 流):
//   - 深さは「最深の文に到達するまでに入る制御構文ブロックの数」。関数本体 = 深さ 0。
//   - 深さを増やす制御構文: if / for / range / switch / type-switch / select。
//   - `else if` 連鎖は *同一深度*(else-if は継続であって入れ子ではない)。
//   - bare block `{}`・FuncLit(クロージャ)は深さを増やさない。クロージャ内部の
//     ネストは外側関数に算入しない(別スコープ)。
//
// _test.go と TestXxx/BenchmarkXxx/ExampleXxx/FuzzXxx は除外。
// stdlib の go/ast のみ(ADR-0001 ゼロ依存)。決定論的(File→Line→Func)。
package nestdepth

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

// defaultThreshold は既定のネスト深度しきい値。これを *超える* 関数を flag。
const defaultThreshold = 4

// Finding は 1 件の deep-nesting 指摘またはパースエラー。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Func     string `json:"func,omitempty"`
	Depth    int    `json:"depth,omitempty"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned  int       `json:"files_scanned"`
	FuncsScanned  int       `json:"funcs_scanned"`
	Threshold     int       `json:"threshold"`
	MaxDepth      int       `json:"max_depth"`
	OverThreshold int       `json:"over_threshold"`
	Findings      []Finding `json:"findings"`
}

// Scan は files を解析し、ネスト深度がしきい値を超える関数を報告する。
// threshold<=0 は defaultThreshold(4)。出力は決定論的(File→Line→Func)。
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
		r.FuncsScanned++
		depth := maxStmtListDepth(fn.Body.List, 0)
		if depth > r.MaxDepth {
			r.MaxDepth = depth
		}
		if depth > threshold {
			r.OverThreshold++
			pos := fset.Position(fn.Pos())
			r.Findings = append(r.Findings, Finding{
				File: path, Line: pos.Line, Func: funcDeclName(fn), Depth: depth,
				Rule: "deep-nesting", Severity: severityFor(depth),
				Message: "max control-flow nesting depth " + strconv.Itoa(depth) +
					" exceeds threshold " + strconv.Itoa(threshold) +
					" — flatten with guard clauses / early returns or extract helpers",
			})
		}
	}
}

// maxStmtListDepth は文リストを cur 深度で評価し、到達しうる最大深度を返す。
func maxStmtListDepth(stmts []ast.Stmt, cur int) int {
	best := cur
	for _, s := range stmts {
		if d := stmtDepth(s, cur); d > best {
			best = d
		}
	}
	return best
}

// stmtDepth は文 s を「s 自身の深度 = cur」で評価し、s を通じて到達しうる最大深度を返す。
// 制御構文の本体は cur+1。else if 連鎖は同一深度。FuncLit へは降りない。
func stmtDepth(s ast.Stmt, cur int) int {
	switch x := s.(type) {
	case *ast.IfStmt:
		best := maxStmtListDepth(x.Body.List, cur+1)
		switch e := x.Else.(type) {
		case *ast.BlockStmt:
			if d := maxStmtListDepth(e.List, cur+1); d > best {
				best = d
			}
		case *ast.IfStmt:
			// else if: 継続(同一深度)
			if d := stmtDepth(e, cur); d > best {
				best = d
			}
		}
		return best
	case *ast.ForStmt:
		return maxStmtListDepth(x.Body.List, cur+1)
	case *ast.RangeStmt:
		return maxStmtListDepth(x.Body.List, cur+1)
	case *ast.SwitchStmt:
		return caseClausesDepth(x.Body.List, cur+1)
	case *ast.TypeSwitchStmt:
		return caseClausesDepth(x.Body.List, cur+1)
	case *ast.SelectStmt:
		return commClausesDepth(x.Body.List, cur+1)
	case *ast.BlockStmt:
		return maxStmtListDepth(x.List, cur) // bare block: 深度増えず
	case *ast.LabeledStmt:
		return stmtDepth(x.Stmt, cur)
	}
	return cur
}

func caseClausesDepth(clauses []ast.Stmt, depth int) int {
	best := depth
	for _, c := range clauses {
		if cc, ok := c.(*ast.CaseClause); ok {
			if d := maxStmtListDepth(cc.Body, depth); d > best {
				best = d
			}
		}
	}
	return best
}

func commClausesDepth(clauses []ast.Stmt, depth int) int {
	best := depth
	for _, c := range clauses {
		if cc, ok := c.(*ast.CommClause); ok {
			if d := maxStmtListDepth(cc.Body, depth); d > best {
				best = d
			}
		}
	}
	return best
}

func severityFor(depth int) string {
	if depth >= 6 {
		return "high"
	}
	return "medium"
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
