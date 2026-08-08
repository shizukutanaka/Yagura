// Package thelper はテストヘルパー関数が `t.Helper()` を呼んでいるかを go/ast で
// 検査する *テスト品質軸* のレンズ(ソクラテス新視点 XX、Qiita/Zenn 調査、
// kulti/thelper 由来)。
//
// 動機:
//
//	assertcheck は「テストが何かを主張しているか」(assertion 密度)を測るが、
//	テストヘルパー自身の衛生は未計測だった。`*testing.T` を受け取るヘルパーが
//	`t.Helper()` を呼ばないと、テスト失敗時の行番号が *ヘルパー内部* を指し、
//	どのテストが落ちたのか分からなくなる——実デバッグを確実に遅くする欠陥。
//	`t.Helper()` を冒頭で呼べば、失敗箇所は呼び出し側(= 本物のテスト)を指す。
//	これは認識された静的解析ツール thelper が機械化するベストプラクティス。
//
// 規約(保守的・偽陽性最小):
//   - 対象 = `*testing.T` / `*testing.B` / `testing.TB` / `*testing.F` を引数に取る
//     名前付き関数(method 含む)。リテラルな `testing.X` セレクタのみ照合
//     (別名 import は型解決要のため対象外。ctxcheck と同じ流儀)。
//   - 除外 = テストランナーのエントリポイント(`Test`/`Benchmark`/`Fuzz`/`Example`
//     の後に大文字/数字/`_`/末尾。Go の命名規則準拠。TestMain も含む)。
//   - 除外 = 引数が `_`(参照不能)/ 無名(同上)。
//   - flag 条件は `t.Helper()` の *完全な不在* のみ(位置は問わない)。これにより
//     「Helper を呼んでいるが先頭でない」程度の style ノイズを出さない。
//   - FuncLit(`t.Run` のサブテストクロージャ等)は対象外(named FuncDecl のみ)。
//
// テストが主題のレンズなので、production lens の「_test.go を除外」規約(L4)とは
// 逆に *_test.go を含む全 .go を走査する(testutil 等の本体側ヘルパーも捕捉)。
// stdlib の go/ast のみ(ADR-0001 ゼロ依存)。型情報不要・決定論的(File→Line→Func)。
package thelper

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
	"unicode"
)

// Finding は 1 件の helper 衛生指摘またはパースエラー。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Func     string `json:"func,omitempty"`
	Name     string `json:"name,omitempty"` // testing 引数名(t/b/tb…)
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned int       `json:"files_scanned"`
	FuncsScanned int       `json:"funcs_scanned"` // testing 引数を持つヘルパー候補数
	Flagged      int       `json:"flagged"`
	Findings     []Finding `json:"findings"`
}

// Scan は files(path→content)を解析し、t.Helper() を欠くヘルパーを報告する。
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
	sort.Slice(r.Findings, func(i, j int) bool { return lessFinding(r.Findings[i], r.Findings[j]) })
	for _, f := range r.Findings {
		if f.Rule == "missing-t-helper" {
			r.Flagged++
		}
	}
	return r
}

func scanFile(path, src string, r *Report) {
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
		if isEntryPoint(fn.Name.Name) {
			continue
		}
		params := testingParamNames(fn.Type.Params)
		if len(params) == 0 {
			continue // testing 引数を持たない / 参照不能 → ヘルパーではない
		}
		r.FuncsScanned++
		if callsHelper(fn.Body, params) {
			continue
		}
		pos := fset.Position(fn.Pos())
		r.Findings = append(r.Findings, Finding{
			File: path, Line: pos.Line, Func: funcDeclName(fn), Name: params[0],
			Rule: "missing-t-helper", Severity: "medium",
			Message: "test helper takes *testing.T/B/TB but never calls " + params[0] +
				".Helper() — failures will point inside the helper, not the calling test; add " +
				params[0] + ".Helper() as the first statement",
		})
	}
}

// testingParamNames は引数のうち *testing.T/B/F / testing.TB 型のものの
// 参照可能な名前を返す(`_`・無名は除外)。
func testingParamNames(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var names []string
	for _, field := range fl.List {
		if !isTestingType(field.Type) {
			continue
		}
		for _, nm := range field.Names {
			if nm.Name != "_" {
				names = append(names, nm.Name)
			}
		}
	}
	return names
}

// isTestingType は(ポインタを剥がした上で)型が testing.{T,B,TB,F} か判定する。
func isTestingType(e ast.Expr) bool {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok || x.Name != "testing" {
		return false
	}
	switch sel.Sel.Name {
	case "T", "B", "TB", "F":
		return true
	}
	return false
}

// callsHelper は body 内で names のいずれかに対する `<name>.Helper()` 呼び出しが
// あるか(位置不問)を返す。
func callsHelper(body *ast.BlockStmt, names []string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Helper" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && set[id.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}

// isEntryPoint は Go テストランナーのエントリポイント命名(Test/Benchmark/Fuzz/
// Example の後に大文字・数字・`_`・末尾)に一致するか判定する。
func isEntryPoint(name string) bool {
	for _, p := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		rest, ok := strings.CutPrefix(name, p)
		if !ok {
			continue
		}
		if rest == "" {
			return true
		}
		r := rune(rest[0])
		if unicode.IsUpper(r) || unicode.IsDigit(r) || r == '_' {
			return true
		}
	}
	return false
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
	return a.Func < b.Func
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
