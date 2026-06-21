// Package ctxcheck は context.Context の取り扱い規律を go/ast で検査する
// (ソクラテス新視点 VIII — Qiita/Zenn 調査 v0.75)。
//
// 動機:
//
//	Go コミュニティには context.Context について 2 つの確立した規約がある——
//	どちらも canonical linter(containedctx / 関数引数順チェック)で機械化されている
//	が、Yagura のレンズ群はこの「並行性・キャンセル伝播の契約」軸を一切測っていな
//	かった。namecheck が *名前* の約束を、errpolicy が *エラー* の診断可能性を見る
//	ように、ctxcheck は *context 伝播* の規律を見る。
//
// 検査ルール(いずれも go/ast、型解決なし、決定論的):
//
//   - context-not-first: 関数/メソッドが context.Context 引数を持つのに第一引数で
//     ない。context は「この呼び出しはキャンセル/タイムアウト可能」という signal で
//     あり、慣習上必ず先頭に置く(可読性・予測可能性)。
//     例外: 第一引数が *testing.T/B/F の test helper パターンは許容(canonical 例外)。
//
//   - contained-ctx: struct が context.Context フィールド(名前付き or 埋め込み)を
//     持つ。Go 公式ブログ "Contexts and structs" が非推奨とする手法で、context は
//     struct に溜めず関数/メソッドの第一引数で渡すべき。
//
// 検出は保守的: `context.Context` という literal selector(標準 import 名)のみ
// 照合する。別名 import (`import ctxpkg "context"`)は型解決を要するため対象外。
//
// stdlib の go/ast のみ(ADR-0001 ゼロ依存)。
package ctxcheck

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
)

// Finding は 1 件の context 規律違反またはパースエラー。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Func     string `json:"func,omitempty"` // 関数/メソッド名 or 型名
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

// Scan は files(path→content)を解析し、context.Context 規律違反を報告する。
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
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if find := checkCtxFirst(fset, path, d); find != nil {
				r.Findings = append(r.Findings, *find)
			}
		case *ast.GenDecl:
			if d.Tok == token.TYPE {
				scanTypeDecl(fset, path, d, r)
			}
		}
	}
}

// checkCtxFirst は FuncDecl の引数列に context.Context があるのに先頭でない場合に
// Finding を返す。*testing.T/B/F が先頭の test helper は例外。
func checkCtxFirst(fset *token.FileSet, path string, fn *ast.FuncDecl) *Finding {
	if fn.Type == nil || fn.Type.Params == nil {
		return nil
	}
	// 引数を「名前単位」で平坦化(`a, b context.Context` のような複数名にも対応)。
	types := flattenParamTypes(fn.Type.Params)
	if len(types) == 0 {
		return nil
	}
	ctxIdx := -1
	for i, t := range types {
		if isContextContext(t) {
			ctxIdx = i
			break
		}
	}
	if ctxIdx <= 0 {
		return nil // ctx 無し、または既に先頭
	}
	// 例外: 先頭が *testing.T/B/F の test helper
	if isTestingPtr(types[0]) {
		return nil
	}
	pos := fset.Position(fn.Pos())
	return &Finding{
		File: path, Line: pos.Line, Func: funcDeclName(fn),
		Rule: "context-not-first", Severity: "medium",
		Message: "context.Context should be the first parameter (Go convention) — it signals the call is cancelable/timeout-bound and aids readability",
	}
}

// flattenParamTypes は FieldList を「引数 1 つにつき 1 型」へ平坦化する。
func flattenParamTypes(fl *ast.FieldList) []ast.Expr {
	var out []ast.Expr
	for _, field := range fl.List {
		n := len(field.Names)
		if n == 0 {
			n = 1 // 無名引数
		}
		for i := 0; i < n; i++ {
			out = append(out, field.Type)
		}
	}
	return out
}

// isContextContext は式が literal `context.Context` か判定する。
func isContextContext(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "context" && sel.Sel.Name == "Context"
}

// isTestingPtr は式が *testing.T / *testing.B / *testing.F か判定する。
func isTestingPtr(e ast.Expr) bool {
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "testing" {
		return false
	}
	switch sel.Sel.Name {
	case "T", "B", "F":
		return true
	}
	return false
}

// scanTypeDecl は TYPE 宣言の各 struct に context.Context フィールドが無いか検査する。
func scanTypeDecl(fset *token.FileSet, path string, gd *ast.GenDecl, r *Report) {
	for _, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			continue
		}
		if !structHasCtxField(st) {
			continue
		}
		pos := fset.Position(ts.Pos())
		r.Findings = append(r.Findings, Finding{
			File: path, Line: pos.Line, Func: ts.Name.Name,
			Rule: "contained-ctx", Severity: "low",
			Message: "struct \"" + ts.Name.Name + "\" contains a context.Context field — store context per-call (first arg), not in a struct (Go blog: Contexts and structs)",
		})
	}
}

func structHasCtxField(st *ast.StructType) bool {
	for _, field := range st.Fields.List {
		if isContextContext(field.Type) {
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

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
