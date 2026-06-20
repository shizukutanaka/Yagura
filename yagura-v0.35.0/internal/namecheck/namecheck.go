// Package namecheck は関数名とシグネチャの整合性を go/ast で検査する
// (ソクラテス新視点 v0.73 — 意味軸の最初のレンズ)。
//
// 動機:
//
//	これまでのレンズはすべてコードの *構造* を測ってきた——複雑度・引数幅・戻り値幅・
//	結合・文書化・到達可能性・一貫性。しかしどのレンズも「名前が振る舞いの約束を
//	守っているか」は見ていなかった。`isReady` が int を返し、`GetName` が何も返さ
//	なければ、それは全レンズを素通りしつつ読み手を能動的に誤導する。名前は計測され
//	ていない契約だった(quality-lens-spec.md の弱点 W2)。
//
//	paramcheck が入口を、returncheck が出口を、flagarg が制御結合を測る。namecheck は
//	*名前がそれら全体について立てる約束* を測り、シグネチャ三部作を意味軸で締める。
//
// 決定論的ルール(すべて go/ast、型解決なし):
//   - predicate-not-bool: is/has/can/should/must 述語なのに第一戻り値が bool でない
//   - getter-no-return:    Get/get 接頭辞なのに戻り値が無い
//   - constructor-no-return: New/new 接頭辞なのに戻り値が無い
//
// 語境界: 接頭辞の次の文字が大文字(または名前の終端)の時のみ接頭辞とみなす。
// よって "Hash" は "has" 述語ではない。bare な "Get"/"New"(接尾辞なし)は除外。
//
// 型情報を使わないため、第一戻り値の型は構文的に読む(`*ast.Ident{Name:"bool"}`)。
// bool を別名定義した named type は保守的に flag しない(型情報無しでの誤検出回避)。
//
// stdlib の go/ast のみ(ADR-0001 ゼロ依存)。決定論的。
package namecheck

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
	"unicode"
)

// Finding は 1 件の name↔signature 不整合またはパースエラー。
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
	FuncsScanned int       `json:"funcs_scanned"`
	Flagged      int       `json:"flagged"`
	Findings     []Finding `json:"findings"`
}

// predicatePrefixes は bool を約束する述語接頭辞。
var predicatePrefixes = []string{"is", "has", "can", "should", "must"}

// Scan は files(path→content)を解析し、名前と署名が食い違う関数を報告する。
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
	r.Flagged = countRealFindings(r.Findings)
	return r
}

func countRealFindings(fs []Finding) int {
	n := 0
	for _, f := range fs {
		if f.Rule != "parse-error" {
			n++
		}
	}
	return n
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
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Type == nil {
			continue
		}
		name := fn.Name.Name
		if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") ||
			strings.HasPrefix(name, "Example") || strings.HasPrefix(name, "Fuzz") {
			continue
		}
		r.FuncsScanned++
		if find := checkName(fn, name); find != nil {
			pos := fset.Position(fn.Pos())
			find.File = path
			find.Line = pos.Line
			find.Func = funcDeclName(fn)
			r.Findings = append(r.Findings, *find)
		}
	}
}

// checkName は 1 つの FuncDecl を 3 ルールで検査し、最初の違反を返す(無ければ nil)。
func checkName(fn *ast.FuncDecl, name string) *Finding {
	results := resultTypes(fn.Type)

	if pre, ok := matchPredicate(name); ok {
		if len(results) == 0 || results[0] != "bool" {
			return &Finding{
				Rule: "predicate-not-bool", Severity: "medium",
				Message: "name uses predicate prefix \"" + pre + "\" but does not return bool first — readers expect a boolean question to answer true/false",
			}
		}
		return nil
	}
	if hasWordPrefix(name, "Get") || hasWordPrefix(name, "get") {
		if len(results) == 0 {
			return &Finding{
				Rule: "getter-no-return", Severity: "medium",
				Message: "name uses getter prefix \"Get\" but returns nothing — a getter that returns no value misleads callers",
			}
		}
		return nil
	}
	if hasWordPrefix(name, "New") || hasWordPrefix(name, "new") {
		if len(results) == 0 {
			return &Finding{
				Rule: "constructor-no-return", Severity: "low",
				Message: "name uses constructor prefix \"New\" but returns nothing — a constructor is expected to produce a value",
			}
		}
		return nil
	}
	return nil
}

// matchPredicate は name が述語接頭辞(語境界つき)で始まれば接頭辞を返す。
func matchPredicate(name string) (string, bool) {
	for _, p := range predicatePrefixes {
		if hasWordPrefix(name, p) {
			return p, true
		}
	}
	return "", false
}

// hasWordPrefix は name が prefix で始まり、かつ次の文字が大文字または終端
// (=語境界)である時 true。"Hash" は "has" 接頭辞にマッチしない。bare な
// prefix(接尾辞なし)も語が無いので false。
func hasWordPrefix(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	rest := name[len(prefix):]
	if rest == "" {
		return false // bare prefix は語境界を成さない
	}
	return unicode.IsUpper([]rune(rest)[0])
}

// resultTypes は戻り値の型を構文的に列挙する(`a, b string` → ["string","string"])。
// 名前付き複合や型のみのどちらにも対応。型は ast.Ident のみ literal 名を返し、
// それ以外(*T, []T, map[..] 等)は "" で表現(bool ではないことだけが重要)。
func resultTypes(ft *ast.FuncType) []string {
	if ft.Results == nil {
		return nil
	}
	var out []string
	for _, field := range ft.Results.List {
		tn := typeName(field.Type)
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, tn)
		}
	}
	return out
}

func typeName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
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
