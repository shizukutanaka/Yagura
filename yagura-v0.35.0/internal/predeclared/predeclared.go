// Package predeclared は Go の組み込み識別子(builtin / predeclared identifiers)を
// シャドウする宣言を go/ast で検出する(ソクラテス新視点 XII — Qiita/Zenn 調査 v0.79;
// nishanths/predeclared 由来)。
//
// 動機:
//
//	Go は `len`, `cap`, `new`, `error`, `string`, `min`, `max`, `clear`(Go 1.21+)等
//	39 個の predeclared identifier を「再宣言可能」にしている。`cap := capacity` の
//	ような shadowing が起きると、そのスコープ内で組み込み `cap(s)` が呼べなくなり、
//	後から「`make([]int, 0, cap(items))` で組み込みを呼んだつもり」が黙って変数を
//	参照する static bug の温床になる。Qiita/Zenn でも繰り返し警鐘が鳴らされる
//	古典的アンチパターン(canonical linter: nishanths/predeclared)。
//
// 検査ルール(go/ast、型解決なし、決定論的):
//
//   - shadow-predeclared: 名前が predeclared identifier と一致する宣言。
//     対象: 関数/メソッドの引数・名前付き戻り値・関数/型/定数/変数宣言・短縮宣言
//     (`x := ...`)・`for range` の key/value。blank identifier `_` は除外。
//     methods (receiver 付き FuncDecl) は receiver で名前空間が分かれるため除外
//     (canonical linter 既定と同じ)。
//
// `--ignore` で許容識別子を列挙可能(`min`,`max`,`cap` 等は変数名としても自然なため)。
//
// stdlib の go/ast のみ(ADR-0001)。決定論的(File→Line→Name)。
package predeclared

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
)

// predeclaredIdents は Go 1.21+ の組み込み識別子 39 個を集めた集合。
// types + constants + zero value + functions(`min`,`max`,`clear` を含む)。
var predeclaredIdents = map[string]bool{
	// types
	"bool": true, "byte": true, "complex64": true, "complex128": true,
	"error": true, "float32": true, "float64": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"rune": true, "string": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "any": true, "comparable": true,
	// constants
	"true": true, "false": true, "iota": true,
	// zero value
	"nil": true,
	// functions
	"append": true, "cap": true, "clear": true, "close": true, "complex": true,
	"copy": true, "delete": true, "imag": true, "len": true, "make": true,
	"max": true, "min": true, "new": true, "panic": true, "print": true,
	"println": true, "real": true, "recover": true,
}

// Finding は 1 件の shadowing またはパースエラー。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind,omitempty"` // variable / parameter / result / function / type / constant
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

// Scan は files(path→content)を解析し、shadowing を報告する。
// ignore に列挙された識別子は flag しない。出力は決定論的(File→Line→Name)。
func Scan(files map[string]string, ignore []string) Report {
	ignored := map[string]bool{}
	for _, n := range ignore {
		ignored[strings.TrimSpace(n)] = true
	}
	r := Report{}
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		r.FilesScanned++
		scanFile(path, src, ignored, &r)
	}
	sort.Slice(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Name < b.Name
	})
	for _, f := range r.Findings {
		if f.Rule != "parse-error" {
			r.Flagged++
		}
	}
	return r
}

func scanFile(path, src string, ignored map[string]bool, r *Report) {
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
	sc := &scanner2{fset: fset, path: path, ignored: ignored, r: r}
	// Top-level declarations.
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			sc.funcDecl(d)
		case *ast.GenDecl:
			sc.genDecl(d)
		}
	}
}

// scanner2 は 1 ファイル走査中の不変状態(fset/path/ignored/report)を束ねる。
// これにより各 scan ヘルパと emit の引数を絞る(param-check / calibrate 由来の整理)。
type scanner2 struct {
	fset    *token.FileSet
	path    string
	ignored map[string]bool
	r       *Report
}

func (sc *scanner2) funcDecl(fn *ast.FuncDecl) {
	// Top-level function name (skip methods — receiver namespaces them).
	if fn.Recv == nil && fn.Name != nil {
		sc.emit(fn.Name, "function", "high")
	}
	if fn.Type == nil {
		return
	}
	// Params & named results.
	sc.emitFieldNames(fn.Type.Params, "parameter")
	sc.emitFieldNames(fn.Type.Results, "result")
	// Body: scan all assignments, var/const decls, range keys/values.
	if fn.Body != nil {
		sc.block(fn.Body)
	}
}

// emitFieldNames は FieldList の名前付きフィールド名を kind で検査する。
func (sc *scanner2) emitFieldNames(fl *ast.FieldList, kind string) {
	if fl == nil {
		return
	}
	for _, field := range fl.List {
		for _, n := range field.Names {
			sc.emit(n, kind, "medium")
		}
	}
}

func (sc *scanner2) block(body ast.Node) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE {
				for _, lhs := range x.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						sc.emit(id, "variable", "medium")
					}
				}
			}
		case *ast.GenDecl:
			sc.genDecl(x)
		case *ast.RangeStmt:
			if id, ok := x.Key.(*ast.Ident); ok {
				sc.emit(id, "variable", "medium")
			}
			if id, ok := x.Value.(*ast.Ident); ok {
				sc.emit(id, "variable", "medium")
			}
		case *ast.FuncLit:
			if x.Type != nil {
				sc.emitFieldNames(x.Type.Params, "parameter")
				sc.emitFieldNames(x.Type.Results, "result")
			}
		}
		return true
	})
}

func (sc *scanner2) genDecl(gd *ast.GenDecl) {
	for _, spec := range gd.Specs {
		switch s := spec.(type) {
		case *ast.ValueSpec:
			kind, sev := "variable", "medium"
			if gd.Tok == token.CONST {
				kind, sev = "constant", "high"
			}
			for _, n := range s.Names {
				sc.emit(n, kind, sev)
			}
		case *ast.TypeSpec:
			sc.emit(s.Name, "type", "high")
		}
	}
}

func (sc *scanner2) emit(id *ast.Ident, kind, severity string) {
	if id == nil {
		return
	}
	name := id.Name
	if name == "_" || !predeclaredIdents[name] || sc.ignored[name] {
		return
	}
	pos := sc.fset.Position(id.Pos())
	sc.r.Findings = append(sc.r.Findings, Finding{
		File: sc.path, Line: pos.Line, Name: name, Kind: kind,
		Rule: "shadow-predeclared", Severity: severity,
		Message: "declaration of " + kind + " \"" + name +
			"\" shadows a Go predeclared identifier — readers may expect the builtin and the shadowed identifier silently takes precedence",
	})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
