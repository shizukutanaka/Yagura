// Package recvcheck は型のメソッドレシーバの自己一貫性を go/ast で検査する
// (ソクラテス新視点 v0.36)。
//
// 動機:
//
//	これまでのレンズは code unit を *絶対基準* で測ってきた(複雑度・結合・文書化・
//	到達可能性)。本レンズは unit を *自分自身の他の部分* と照らす――自己一貫性の軸。
//	一貫性は読み手の予測可能性そのもの。
//
// 検査するルール(いずれも golint/govet 隣接の認知された慣習):
//   - inconsistent-receiver-name: 同一型のメソッドがレシーバ変数名を不揃いに使う
//     (`func (s *Server)` と `func (srv *Server)`)。読み手は毎メソッドで anchor し直す。
//   - mixed-receiver-type: 同一型が値レシーバとポインタレシーバを混在
//     (`func (t T)` と `func (t *T)`)。満たす interface(method set)が変わる実害ある gotcha。
//   - bad-receiver-name: this/self/me 等 Go 的でないレシーバ名。
//
// stdlib の go/parser + go/ast のみ(ADR-0001)。型情報不要・決定論的。
package recvcheck

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

// Finding は 1 件のレシーバ一貫性の問題(または parse error)。
type Finding struct {
	Package  string `json:"package,omitempty"`
	Type     string `json:"type,omitempty"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned     int       `json:"files_scanned"`
	TypesWithMethods int       `json:"types_with_methods"`
	Findings         []Finding `json:"findings"`
}

// badNames は Go では避けるべきレシーバ名(OOP 言語からの輸入)。
var badNames = map[string]bool{"this": true, "self": true, "me": true}

// method は 1 メソッドのレシーバ情報。
type method struct {
	recvType string
	recvName string // 無名レシーバは ""
	pointer  bool
	file     string
	line     int
}

// Scan は files(path→content)のメソッドレシーバ一貫性を検査する。決定論的。
//
// 解析は package(= ディレクトリ)単位。型名は package を跨いで衝突しうる
// (aiverify.Result と qualitycheck.Result は別物)ため、(package, 型名) で
// グルーピングしないと別 package の同名型を誤って混同してしまう。
func Scan(files map[string]string) Report {
	r := Report{}
	byPkg := map[string][]string{}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		r.FilesScanned++
		dir := filepath.ToSlash(filepath.Dir(path))
		byPkg[dir] = append(byPkg[dir], path)
	}

	dirs := make([]string, 0, len(byPkg))
	for d := range byPkg {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		byType := map[string][]method{}
		for _, path := range byPkg[dir] {
			collectMethods(path, files[path], byType, &r)
		}

		types := make([]string, 0, len(byType))
		for t := range byType {
			types = append(types, t)
		}
		sort.Strings(types)
		r.TypesWithMethods += len(types)

		for _, typ := range types {
			methods := byType[typ]
			// 出現順(file→line)に安定ソートして「最初の宣言」を決定論的にする。
			sort.SliceStable(methods, func(i, j int) bool {
				if methods[i].file != methods[j].file {
					return methods[i].file < methods[j].file
				}
				return methods[i].line < methods[j].line
			})
			analyzeType(pkgName(dir), typ, methods, &r)
		}
	}

	sortFindings(r.Findings)
	return r
}

func pkgName(dir string) string {
	if dir == "." || dir == "" {
		return "."
	}
	return dir
}

func collectMethods(path, src string, byType map[string][]method, r *Report) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
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
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		field := fd.Recv.List[0]
		typeName, pointer := recvType(field.Type)
		if typeName == "" {
			continue
		}
		name := ""
		if len(field.Names) > 0 {
			name = field.Names[0].Name
			if name == "_" {
				name = ""
			}
		}
		pos := fset.Position(fd.Pos())
		byType[typeName] = append(byType[typeName], method{
			recvType: typeName, recvName: name, pointer: pointer,
			file: pos.Filename, line: pos.Line,
		})
	}
}

func analyzeType(pkg, typ string, methods []method, r *Report) {
	first := methods[0]

	// 1) レシーバ名の不揃い(無名は対象外)。
	nameSet := map[string]bool{}
	var names []string
	for _, m := range methods {
		if m.recvName == "" {
			continue
		}
		if !nameSet[m.recvName] {
			nameSet[m.recvName] = true
			names = append(names, m.recvName)
		}
	}
	if len(names) > 1 {
		sort.Strings(names)
		r.Findings = append(r.Findings, Finding{
			Package: pkg, Type: typ, File: first.file, Line: first.line,
			Rule: "inconsistent-receiver-name", Severity: "medium",
			Message: "type " + typ + " uses inconsistent receiver names {" +
				strings.Join(names, ", ") + "} — pick one and use it for every method",
		})
	}

	// 2) 値/ポインタの混在。
	var hasVal, hasPtr bool
	for _, m := range methods {
		if m.pointer {
			hasPtr = true
		} else {
			hasVal = true
		}
	}
	if hasVal && hasPtr {
		r.Findings = append(r.Findings, Finding{
			Package: pkg, Type: typ, File: first.file, Line: first.line,
			Rule: "mixed-receiver-type", Severity: "medium",
			Message: "type " + typ + " mixes value and pointer receivers — " +
				"this changes which interfaces the value vs pointer satisfies; use one consistently",
		})
	}

	// 3) Go 的でないレシーバ名(this/self/me)。各メソッド単位で報告。
	for _, m := range methods {
		if badNames[m.recvName] {
			r.Findings = append(r.Findings, Finding{
				Package: pkg, Type: typ, File: m.file, Line: m.line,
				Rule: "bad-receiver-name", Severity: "low",
				Message: "receiver name " + m.recvName + " on " + typ +
					" is un-idiomatic — use a short abbreviation of the type",
			})
		}
	}
}

// recvType は receiver の型式から型名とポインタ性を返す。
func recvType(e ast.Expr) (name string, pointer bool) {
	switch t := e.(type) {
	case *ast.StarExpr:
		n, _ := recvType(t.X)
		return n, true
	case *ast.Ident:
		return t.Name, false
	case *ast.IndexExpr: // generic: T[E]
		n, _ := recvType(t.X)
		return n, false
	case *ast.IndexListExpr: // generic: T[E1, E2]
		n, _ := recvType(t.X)
		return n, false
	default:
		return "", false
	}
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
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		return a.Type < b.Type
	})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
