// Package astcheck は Go ソースを go/ast で構造解析し、行 regex では確実に
// 検出できないパターンを決定論的に flag する(Roadmap #6, v0.36)。
//
// 動機:
//
//	qualitycheck / aiverify は line-based regex で、コメント/文字列リテラルの
//	誤検出を完全には避けられず、また「パッケージ文脈」や「ブロック構造」を要する
//	検査はそもそも書けない。本 package は stdlib の go/parser + go/ast のみ
//	(ADR-0001 ゼロ依存)で、構文木に基づく精密な検査を提供する。
//
// 現在のルール(いずれも regex では困難):
//   - os-exit-library : package main 以外(かつ *_test.go 以外)での os.Exit 呼出。
//     ライブラリが os.Exit すると呼び手のプロセスごと落ちる。package 文脈が必要。
//   - panic-in-library: package main 以外(かつ *_test.go 以外)での panic 呼出。
//     ライブラリの panic は呼び手の recover がないとプロセスが落ちる。同上の文脈が必要。
//   - empty-nil-branch: `if x != nil {}`(本体が空)。エラー/分岐のサイレント
//     握り潰し。ブロック構造(空 body)の判定が必要。
//   - defer-in-loop   : ループ内(かつ同一関数スコープ内)の defer。defer は関数
//     return 時にまとめて走るので、毎イテレーションで資源が解放されず蓄積する。
//     ループ/関数スコープを跨いだ文脈判定が必要(closure 内の defer は除外)。
//   - error-string-compare: `err.Error() == "..."` のような err 文字列比較。
//     メッセージは変わりうるので脆い。errors.Is/As か sentinel error を使うべき。
//     `.Error()` 呼出が ==/!= の被演算子であることの判定が必要(logging 用途は除外)。
//   - bare-goroutine  : `go func() { ... }()` という匿名 goroutine で、本体が ctx /
//     context を参照しない場合。ライフサイクル管理の欠如を示すシグナル。
//     FuncLit 本体を AST 走査して ctx 参照を探す文脈判定が必要。
//   - parse-error    : 構文解析に失敗した Go ファイル。黙ってスキップせず surface
//     する(部分スキャンを完全スキャンと誤読する fail-open を防ぐ)。
//
// defect 検出に加え、capability surface 分析(surface.go の Surface)も提供する:
// 「コードが何に触れるか」(exec/network/unsafe/reflect/crypto)を import から
// 静的にプロファイルする least-privilege レンズ。CLI `ast-check --surface`。
//   - blank-error-discard: library コード(main/test 以外)での `_ = call()` パターン。
//     返り値(多くはエラー)を全てブランク識別子に捨てる — エラーの無言の握り潰し。
//     `_, _ = f()` 等 複数ブランクも同様。defer f.Close() での慣習的な書き方も含む。
//     go/types 不要: AssignStmt の LHS が全て `_` で RHS が CallExpr なら flag。
package astcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
)

// Finding は 1 件の構造的検出。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"` // high / medium / low
	Message  string `json:"message"`
}

// Result は ScanFiles の集計。
type Result struct {
	FilesScanned int            `json:"files_scanned"`
	Findings     []Finding      `json:"findings"`
	BySeverity   map[string]int `json:"by_severity"`
	ByRule       map[string]int `json:"by_rule"`
}

// IsGoFile は path が Go ソース(.go)かを返す。
func IsGoFile(path string) bool { return strings.HasSuffix(path, ".go") }

// ScanFile は 1 つの Go ソースを解析して findings を返す。
//
// 構文エラーがあっても parser は部分 AST を返すため、parse-error を 1 件記録した
// うえで、解析できた範囲は引き続き検査する(取りこぼしを最小化)。
func ScanFile(path, src string) []Finding {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.AllErrors)

	var out []Finding
	if err != nil {
		line := 1
		if el, ok := err.(scanner.ErrorList); ok && len(el) > 0 {
			line = el[0].Pos.Line
		}
		out = append(out, Finding{
			File: path, Line: line, Column: 1,
			Rule: "parse-error", Severity: "low",
			Message: "Go source did not parse: " + firstLine(err.Error()),
		})
	}
	if f == nil {
		return out
	}

	pkg := ""
	if f.Name != nil {
		pkg = f.Name.Name
	}
	isTest := strings.HasSuffix(path, "_test.go")

	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if isOsExit(x.Fun) && pkg != "main" && !isTest {
				p := fset.Position(x.Pos())
				out = append(out, Finding{
					File: path, Line: p.Line, Column: p.Column,
					Rule: "os-exit-library", Severity: "high",
					Message: fmt.Sprintf("os.Exit in package %q terminates the caller; return an error instead", pkg),
				})
			}
			if isPanicCall(x) && pkg != "main" && !isTest {
				p := fset.Position(x.Pos())
				out = append(out, Finding{
					File: path, Line: p.Line, Column: p.Column,
					Rule: "panic-in-library", Severity: "high",
					Message: fmt.Sprintf("panic in package %q propagates to caller; return an error instead", pkg),
				})
			}
		case *ast.IfStmt:
			if isNotNilCompare(x.Cond) && x.Body != nil && len(x.Body.List) == 0 {
				p := fset.Position(x.Pos())
				out = append(out, Finding{
					File: path, Line: p.Line, Column: p.Column,
					Rule: "empty-nil-branch", Severity: "medium",
					Message: "empty body for a `!= nil` check silently swallows the error/branch",
				})
			}
		case *ast.BinaryExpr:
			if (x.Op == token.EQL || x.Op == token.NEQ) && (isErrorMethodCall(x.X) || isErrorMethodCall(x.Y)) {
				p := fset.Position(x.Pos())
				out = append(out, Finding{
					File: path, Line: p.Line, Column: p.Column,
					Rule: "error-string-compare", Severity: "medium",
					Message: "comparing err.Error() by string is fragile (messages change); use errors.Is/errors.As or a sentinel error",
				})
			}
		case *ast.GoStmt:
			// bare-goroutine: 匿名 goroutine がライフサイクル管理なしで起動されている。
			// 除外: test file / ctx 参照あり / 明示パラメータあり(型付きクロージャ) /
			//         WaitGroup・channel による同期あり。
			if fl, ok := x.Call.Fun.(*ast.FuncLit); ok &&
				!isTest &&
				!funcLitHasLifecycle(fl) {
				p := fset.Position(x.Pos())
				out = append(out, Finding{
					File: path, Line: p.Line, Column: p.Column,
					Rule: "bare-goroutine", Severity: "medium",
					Message: "anonymous goroutine without context — add a ctx parameter or use a named worker to make the lifecycle visible",
				})
			}
		case *ast.AssignStmt:
			if pkg != "main" && !isTest && isAllBlankLHS(x.Lhs) && len(x.Rhs) == 1 {
				if _, ok := x.Rhs[0].(*ast.CallExpr); ok {
					p := fset.Position(x.Pos())
					out = append(out, Finding{
						File: path, Line: p.Line, Column: p.Column,
						Rule: "blank-error-discard", Severity: "medium",
						Message: "_ = call() discards the return value silently; handle or wrap the error, or document why discarding is safe",
					})
				}
			}
		}
		return true
	})

	// defer-in-loop は loop/func スコープを跨いだ文脈が要るので ast.Walk で
	// inLoop 状態を持って別途検出する(FuncLit 境界で reset)。
	ast.Walk(&deferLoopVisitor{inLoop: false, emit: func(n ast.Node) {
		p := fset.Position(n.Pos())
		out = append(out, Finding{
			File: path, Line: p.Line, Column: p.Column,
			Rule: "defer-in-loop", Severity: "medium",
			Message: "defer inside a loop runs only at function return, not per iteration — resources accumulate; close explicitly or wrap the body in a helper/closure",
		})
	}}, f)
	return out
}

// deferLoopVisitor は ast.Walk で関数本体を辿り、ループ内 (かつ同一関数スコープ内)
// の defer を検出する。FuncLit に入ると inLoop=false にリセットする
// (毎イテレーション呼ばれる closure 内の defer は正しい使い方なので flag しない)。
type deferLoopVisitor struct {
	inLoop bool
	emit   func(ast.Node)
}

func (v *deferLoopVisitor) Visit(n ast.Node) ast.Visitor {
	switch n.(type) {
	case *ast.ForStmt, *ast.RangeStmt:
		return &deferLoopVisitor{inLoop: true, emit: v.emit}
	case *ast.FuncLit:
		return &deferLoopVisitor{inLoop: false, emit: v.emit}
	case *ast.DeferStmt:
		if v.inLoop {
			v.emit(n)
		}
		return v
	}
	return v
}

// ScanFiles は files map の Go ファイルのみを解析し、決定論的に整列して返す。
func ScanFiles(files map[string]string) Result {
	res := Result{BySeverity: map[string]int{}, ByRule: map[string]int{}}
	for path, src := range files {
		if !IsGoFile(path) {
			continue
		}
		res.FilesScanned++
		res.Findings = append(res.Findings, ScanFile(path, src)...)
	}
	sortFindings(res.Findings)
	for _, f := range res.Findings {
		res.BySeverity[f.Severity]++
		res.ByRule[f.Rule]++
	}
	return res
}

// sortFindings は全順序(File→Line→Column→Rule)で並べる。
// findings は map 走査順で集まるため、tie-break が全順序でないと出力が run ごとに
// ブレる(unstable sort + 非全順序)。同一位置に複数ルールが当たっても確定させる。
func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.Rule < b.Rule
	})
}

// isAllBlankLHS は AssignStmt の LHS が全てブランク識別子 `_` かを返す。
// `_ = f()` も `_, _ = f()` も含む。LHS 要素がゼロの場合は false。
func isAllBlankLHS(lhs []ast.Expr) bool {
	if len(lhs) == 0 {
		return false
	}
	for _, e := range lhs {
		id, ok := e.(*ast.Ident)
		if !ok || id.Name != "_" {
			return false
		}
	}
	return true
}

// isPanicCall は call が組み込みの `panic(...)` かを返す。
func isPanicCall(call *ast.CallExpr) bool {
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "panic"
}

// funcLitHasLifecycle は FuncLit が lifecycle 管理の証拠を持つかを返す。
// 以下のいずれかで true:
//   - 明示パラメータあり(型付きクロージャ — 変数キャプチャが意図的)
//   - 本体が "ctx" / "context" を参照(context.Context 管理下)
//   - 本体が WaitGroup メソッド(Done/Wait/Add)を呼出(同期あり)
//   - 本体が channel 操作(<- / close)を含む(同期あり)
func funcLitHasLifecycle(fl *ast.FuncLit) bool {
	if fl.Type.Params != nil && len(fl.Type.Params.List) > 0 {
		return true
	}
	found := false
	ast.Inspect(fl.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *ast.Ident:
			if x.Name == "ctx" || x.Name == "context" ||
				x.Name == "Done" || x.Name == "Wait" || x.Name == "Add" || x.Name == "close" {
				found = true
				return false
			}
		case *ast.SendStmt:
			found = true
			return false
		case *ast.UnaryExpr:
			if x.Op.String() == "<-" {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// isOsExit は fun が `os.Exit` セレクタかを返す。
func isOsExit(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Exit" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "os"
}

// isNotNilCompare は式が `X != nil`(どちらかの辺が nil)かを返す。
func isNotNilCompare(e ast.Expr) bool {
	be, ok := e.(*ast.BinaryExpr)
	if !ok || be.Op != token.NEQ {
		return false
	}
	return isNilIdent(be.X) || isNilIdent(be.Y)
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// isErrorMethodCall は式が `x.Error()`(引数なしの Error メソッド呼出)かを返す。
// err.Error() を文字列比較する典型的なアンチパターンの検出に使う。
func isErrorMethodCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == "Error"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
