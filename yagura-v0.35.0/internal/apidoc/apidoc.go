// Package apidoc は exported API のドキュメント規律を go/ast で計測する
// (ソクラテス新視点 v0.36)。
//
// 動機:
//
//	coupling は package *同士* の依存を測る。では package は依存する側に何を
//	*約束* するのか――それは exported API。exported された名前はすべて importer
//	との契約。doc コメントの無い exported シンボルは「仕様の無い契約」であり、
//	利用側は実装を読まねば約束を知れず、変更は明文化されていない期待を黙って壊す。
//
//	これまでのレンズはすべてコードの *内部*(構造/エラー/テスト/結合)を見てきた。
//	本 package は初めて *公開契約面* とその文書化を見る encapsulation/contract の軸。
//	godoc 規律(golint の "exported X should have comment")を機械化する。
//
// 公開 API の定義:
//   - exported な関数 / 型 / const / var
//   - exported な型に対する exported method(非公開型の method は API ではない)
//   - test ファイル(*_test.go)は公開契約ではないので除外
//
// stdlib の go/parser + go/ast のみ(ADR-0001)。型情報不要・決定論的。
package apidoc

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
	"unicode"
)

// Symbol は 1 つの exported シンボル。
type Symbol struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Name       string `json:"name"` // method は "Type.Method"
	Kind       string `json:"kind"` // func / method / type / const / var
	Documented bool   `json:"documented"`
}

// Finding は 1 件のドキュメント規律違反(または parse error)。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned    int            `json:"files_scanned"`
	ExportedTotal   int            `json:"exported_total"`
	Documented      int            `json:"documented"`
	DocumentedRatio float64        `json:"documented_ratio"`
	ByKind          map[string]int `json:"by_kind"`
	Symbols         []Symbol       `json:"symbols"`
	Findings        []Finding      `json:"findings"`
}

// Scan は files(path→content)の exported API を解析する。決定論的。
func Scan(files map[string]string) Report {
	r := Report{ByKind: map[string]int{}}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		r.FilesScanned++
		scanFile(path, files[path], &r)
	}

	sort.Slice(r.Symbols, func(i, j int) bool {
		a, b := r.Symbols[i], r.Symbols[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Name < b.Name
	})
	sortFindings(r.Findings)

	if r.ExportedTotal > 0 {
		r.DocumentedRatio = float64(r.Documented) / float64(r.ExportedTotal)
	}
	return r
}

func scanFile(path, src string, r *Report) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		line := 1
		if el, ok := err.(scanner.ErrorList); ok && len(el) > 0 {
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

	record := func(kind, name string, pos token.Pos, doc *ast.CommentGroup) {
		documented := doc != nil && len(doc.List) > 0
		p := fset.Position(pos)
		r.Symbols = append(r.Symbols, Symbol{
			File: path, Line: p.Line, Name: name, Kind: kind, Documented: documented,
		})
		r.ExportedTotal++
		r.ByKind[kind]++
		if documented {
			r.Documented++
			return
		}
		r.Findings = append(r.Findings, Finding{
			File: path, Line: p.Line, Name: name, Kind: kind,
			Rule: "exported-undocumented", Severity: "low",
			Message: "exported " + kind + " " + name + " has no doc comment — undocumented public contract",
		})
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil && len(d.Recv.List) > 0 {
				// method: 受け手の型が exported かつ method 名が exported のときだけ公開 API。
				recv := typeName(d.Recv.List[0].Type)
				if isExported(recv) && isExported(d.Name.Name) {
					record("method", recv+"."+d.Name.Name, d.Pos(), d.Doc)
				}
				continue
			}
			if isExported(d.Name.Name) {
				record("func", d.Name.Name, d.Pos(), d.Doc)
			}
		case *ast.GenDecl:
			single := len(d.Specs) == 1
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if !isExported(s.Name.Name) {
						continue
					}
					doc := s.Doc
					if doc == nil && single {
						doc = d.Doc
					}
					record("type", s.Name.Name, s.Pos(), doc)
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, nm := range s.Names {
						if !isExported(nm.Name) {
							continue
						}
						doc := s.Doc
						if doc == nil && single {
							doc = d.Doc
						}
						record(kind, nm.Name, nm.Pos(), doc)
					}
				}
			}
		}
	}
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return typeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver T[P]
		return typeName(t.X)
	case *ast.IndexListExpr:
		return typeName(t.X)
	default:
		return ""
	}
}

func isExported(name string) bool {
	if name == "" {
		return false
	}
	return unicode.IsUpper([]rune(name)[0])
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
		if a.Name != b.Name {
			return a.Name < b.Name
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
