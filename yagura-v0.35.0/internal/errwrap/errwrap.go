// Package errwrap は Go 1.13 エラーラッピング規約の違反を go/ast で検査する
// (ソクラテス新視点 IX — Qiita/Zenn 調査 v0.76; polyfloyd/go-errorlint 由来)。
//
// 動機:
//
//	errpolicy は wrap *率*(naked return err / context 付き)という discipline の meta
//	指標を測る。しかし「ラップしようとした時に *正しく* ラップできているか」という
//	error-chain の健全性(errors.Is / errors.As が機能するか)は別軸であり、どのレンズ
//	も見ていなかった。go-errorlint が標準化した 3 つの defect を機械化する。
//
// 検査ルール(いずれも go/ast、型解決なし、決定論的):
//
//   - non-wrapping-verb: fmt.Errorf がエラー値を %w でなく %v/%s で整形している。
//     %w でないと Unwrap 連鎖が切れ、errors.Is/As が wrapped error を辿れない。
//   - err-value-compare: `err == ErrFoo` / `err != io.EOF` のような sentinel との
//     直接比較。wrapped error には一致しないため errors.Is を使うべき。
//     (`err == nil` / `err != nil` は慣用なので除外)
//   - err-type-assert: `err.(T)` 型アサーション。wrapped error には一致しないため
//     errors.As を使うべき(`err.(type)` switch は除外)。
//
// 型情報を使わないため、「エラー値らしさ」は命名規約で判定する: ident `err`、
// `*Err`/`*err` 接尾辞(例 readErr)、または `.Err` selector。errpolicy と同じ
// type-free 方針(ADR-0001 ゼロ依存)。
//
// stdlib の go/ast のみ。決定論的。
package errwrap

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

// Finding は 1 件のラッピング規約違反またはパースエラー。
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

// Scan は files(path→content)を解析し、errorlint 系の違反を報告する。
// 出力は決定論的(File→Line→Rule)。
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
		return a.Rule < b.Rule
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
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if fn.Body != nil {
				inspectNode(fset, path, fn.Name.Name, fn.Body, r)
			}
			continue
		}
		inspectNode(fset, path, "", decl, r)
	}
}

// inspectNode は node 配下を走査し、3 ルールに該当する式を Finding 化する。
func inspectNode(fset *token.FileSet, path, fn string, node ast.Node, r *Report) {
	if node == nil {
		return
	}
	ast.Inspect(node, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.CallExpr:
			if find := checkErrorf(e); find != nil {
				emit(fset, path, fn, e.Pos(), find, r)
			}
		case *ast.BinaryExpr:
			if find := checkValueCompare(e); find != nil {
				emit(fset, path, fn, e.Pos(), find, r)
			}
		case *ast.TypeAssertExpr:
			if find := checkTypeAssert(e); find != nil {
				emit(fset, path, fn, e.Pos(), find, r)
			}
		}
		return true
	})
}

func emit(fset *token.FileSet, path, fn string, pos token.Pos, f *Finding, r *Report) {
	f.File = path
	f.Line = fset.Position(pos).Line
	f.Func = fn
	r.Findings = append(r.Findings, *f)
}

// checkErrorf は fmt.Errorf 呼出が %w なしでエラー値を整形していれば Finding を返す。
func checkErrorf(call *ast.CallExpr) *Finding {
	if !isFmtErrorf(call.Fun) {
		return nil
	}
	if len(call.Args) < 2 {
		return nil // 整形対象なし
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil // format が非リテラル → 解析不能、保守的に skip
	}
	format := unquote(lit.Value)
	if strings.Contains(format, "%w") {
		return nil // 既に少なくとも 1 つラップしている
	}
	// 可変長引数のどれかがエラー値なら flag
	for _, a := range call.Args[1:] {
		if isErrorExpr(a) {
			return &Finding{
				Rule: "non-wrapping-verb", Severity: "medium",
				Message: "error value formatted with %v/%s in fmt.Errorf — use %w to preserve the error chain (errors.Is/As)",
			}
		}
	}
	return nil
}

// checkValueCompare は err と sentinel の == / != 比較を Finding 化する。
func checkValueCompare(be *ast.BinaryExpr) *Finding {
	if be.Op != token.EQL && be.Op != token.NEQ {
		return nil
	}
	var errSide, otherSide ast.Expr
	switch {
	case isErrorExpr(be.X):
		errSide, otherSide = be.X, be.Y
	case isErrorExpr(be.Y):
		errSide, otherSide = be.Y, be.X
	default:
		return nil
	}
	_ = errSide
	if isNilIdent(otherSide) {
		return nil // err == nil は慣用
	}
	if !isSentinelExpr(otherSide) {
		return nil // 相手が sentinel error らしくなければ保守的に skip
	}
	return &Finding{
		Rule: "err-value-compare", Severity: "medium",
		Message: "error compared with ==/!= against a sentinel — use errors.Is, which matches wrapped errors",
	}
}

// checkTypeAssert は err.(T) を Finding 化する(err.(type) switch は Type==nil で除外)。
func checkTypeAssert(ta *ast.TypeAssertExpr) *Finding {
	if ta.Type == nil {
		return nil // x.(type) switch
	}
	if !isErrorExpr(ta.X) {
		return nil
	}
	return &Finding{
		Rule: "err-type-assert", Severity: "medium",
		Message: "type assertion on an error — use errors.As, which matches wrapped errors",
	}
}

// isFmtErrorf は式が fmt.Errorf か判定する。
func isFmtErrorf(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "fmt" && sel.Sel.Name == "Errorf"
}

// isErrorExpr は式が「エラー値らしい」かを命名規約で判定する(type-free)。
//   - ident `err`、または `*Err`/`*err` 接尾辞(readErr, parseErr 等)
//   - `.Err` selector(構造体フィールド慣習)
func isErrorExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return isErrName(x.Name)
	case *ast.SelectorExpr:
		return isErrName(x.Sel.Name)
	}
	return false
}

func isErrName(name string) bool {
	if name == "err" || name == "Err" {
		return true
	}
	if len(name) > 3 && (strings.HasSuffix(name, "Err") || strings.HasSuffix(name, "err")) {
		return true
	}
	return false
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// isSentinelExpr は式が sentinel error らしい識別子かを判定する。
//   - Ident: Err… 接頭辞(ErrFoo)、または Err/EOF を含む
//   - SelectorExpr: pkg.ErrFoo / io.EOF 等、末尾が Err…/EOF
func isSentinelExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return looksSentinel(x.Name)
	case *ast.SelectorExpr:
		return looksSentinel(x.Sel.Name)
	}
	return false
}

func looksSentinel(name string) bool {
	return name == "EOF" || strings.HasPrefix(name, "Err") || strings.Contains(name, "EOF")
}

func unquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
