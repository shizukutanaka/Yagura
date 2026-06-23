// Package globalcheck は package-level の *可変* グローバル変数を go/ast で検出する
// (ソクラテス新視点 XVI — 共有可変状態の軸)。
//
// 動機:
//
//	synccheck はロックのコピーを、ctxcheck は context 伝播を見る。しかし並行性ハザード
//	とテスト不能性の *最大の源* —— 共有可変グローバル状態 —— はどのレンズも見ていな
//	かった。coupling は import を、deadcode は未使用宣言を、deprank は fan-in を見るが、
//	「誰がグローバル状態を書き換えうるか」は誰も測っていない。
//
//	ただしグローバル var が全て危険なわけではない。読み取り専用の lookup table / config は
//	実質不変で無害。const と error sentinel(`var ErrX = errors.New(...)`、再代入されない)
//	も自動的に対象外。*実際に mutate される* グローバルだけがハザードである。
//
// 検出(go/ast、型情報なし、決定論的。ファイルは *ディレクトリ単位* = package 単位で束ねる):
//   - mutable-global: package-level の `var`(const 除く)で、同 package 内のどこかで
//     代入・インクリメント・index 書込・フィールド書込のいずれかの対象になっているもの。
//   - 保守性: 名前が同 package 内のどこかで *ローカル宣言*(`:=` / `var` / 引数 / range 変数)
//     されている場合、その代入がグローバルかローカルか型情報なしには断定できないため、
//     誤検出を避けて *スキップ* する(型情報なしでの false positive を出さない方針)。
//
// severity: exported は high(他 package も書込可)、unexported は medium。
// _test.go は対象外。stdlib の go/ast のみ(ADR-0001)。
package globalcheck

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path"
	"sort"
	"strings"
	"unicode"
)

// Finding は 1 件の可変グローバル指摘またはパースエラー。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Name     string `json:"name,omitempty"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned int       `json:"files_scanned"`
	Globals      int       `json:"globals"`
	Flagged      int       `json:"flagged"`
	Findings     []Finding `json:"findings"`
}

// globalVar は 1 つの package-level var 宣言。
type globalVar struct {
	file     string
	line     int
	name     string
	exported bool
}

// Scan は files を解析し、可変グローバルを報告する。出力は決定論的(File→Line→Name)。
func Scan(files map[string]string) Report {
	r := Report{}
	// ファイルを directory(= package)単位に束ねる。
	byDir := map[string][]string{}
	dirs := []string{}
	for p := range files {
		if !strings.HasSuffix(p, ".go") {
			continue
		}
		r.FilesScanned++
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		d := path.Dir(p)
		if _, ok := byDir[d]; !ok {
			dirs = append(dirs, d)
		}
		byDir[d] = append(byDir[d], p)
	}
	sort.Strings(dirs)
	fset := token.NewFileSet()
	for _, d := range dirs {
		paths := byDir[d]
		sort.Strings(paths)
		scanPackage(fset, paths, files, &r)
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
	return r
}

// scanPackage は 1 ディレクトリ分のファイル群を 3 パスで解析する。
func scanPackage(fset *token.FileSet, paths []string, files map[string]string, r *Report) {
	var globals []globalVar
	locals := map[string]bool{}  // ローカル宣言された名前(保守的スキップ用)
	mutated := map[string]bool{} // 代入/mutate の対象になった名前

	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, files[p], parser.AllErrors)
		if err != nil {
			line := 1
			var el scanner.ErrorList
			if errors.As(err, &el) && len(el) > 0 {
				line = el[0].Pos.Line
			}
			r.Findings = append(r.Findings, Finding{
				File: p, Line: line,
				Rule: "parse-error", Severity: "low",
				Message: "Go source did not parse: " + firstLine(err.Error()),
			})
		}
		if f == nil {
			continue
		}
		collectGlobals(fset, p, f, &globals)
		collectLocalsAndMutations(f, locals, mutated)
	}

	r.Globals += len(globals)
	for _, g := range globals {
		if !mutated[g.name] || locals[g.name] {
			continue
		}
		sev := "medium"
		if g.exported {
			sev = "high"
		}
		r.Flagged++
		r.Findings = append(r.Findings, Finding{
			File: g.file, Line: g.line, Name: g.name,
			Rule: "mutable-global", Severity: sev,
			Message: "package-level var \"" + g.name + "\" is mutated — shared mutable global state hinders testability and is a data-race hazard; prefer passing it explicitly or guarding with a sync primitive",
		})
	}
}

// collectGlobals は f のトップレベル `var` 宣言名を集める(const は除外)。
func collectGlobals(fset *token.FileSet, path string, f *ast.File, out *[]globalVar) {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				if n.Name == "_" {
					continue
				}
				*out = append(*out, globalVar{
					file: path, line: fset.Position(n.Pos()).Line,
					name: n.Name, exported: isExported(n.Name),
				})
			}
		}
	}
}

// collectLocalsAndMutations は f 内の全関数本体を走査し、ローカル宣言名と mutate 対象名を集める。
func collectLocalsAndMutations(f *ast.File, locals, mutated map[string]bool) {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		// receiver / params / results の名前はローカル。
		addFieldNames(fn.Recv, locals)
		if fn.Type != nil {
			addFieldNames(fn.Type.Params, locals)
			addFieldNames(fn.Type.Results, locals)
		}
		if fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				if x.Type != nil {
					addFieldNames(x.Type.Params, locals)
					addFieldNames(x.Type.Results, locals)
				}
			case *ast.AssignStmt:
				if x.Tok == token.DEFINE {
					for _, lhs := range x.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
							locals[id.Name] = true
						}
					}
				} else { // =, +=, -=, etc.
					for _, lhs := range x.Lhs {
						if name := rootIdent(lhs); name != "" {
							mutated[name] = true
						}
					}
				}
			case *ast.IncDecStmt:
				if name := rootIdent(x.X); name != "" {
					mutated[name] = true
				}
			case *ast.DeclStmt:
				if gd, ok := x.Decl.(*ast.GenDecl); ok && (gd.Tok == token.VAR || gd.Tok == token.CONST) {
					for _, spec := range gd.Specs {
						if vs, ok := spec.(*ast.ValueSpec); ok {
							for _, nm := range vs.Names {
								if nm.Name != "_" {
									locals[nm.Name] = true
								}
							}
						}
					}
				}
			case *ast.RangeStmt:
				if id, ok := x.Key.(*ast.Ident); ok && id.Name != "_" {
					locals[id.Name] = true
				}
				if id, ok := x.Value.(*ast.Ident); ok && id.Name != "_" {
					locals[id.Name] = true
				}
			}
			return true
		})
	}
}

func addFieldNames(fl *ast.FieldList, set map[string]bool) {
	if fl == nil {
		return
	}
	for _, field := range fl.List {
		for _, n := range field.Names {
			if n.Name != "_" {
				set[n.Name] = true
			}
		}
	}
}

// rootIdent は代入 LHS の式から根の識別子名を返す。
//   - `x`        → "x"
//   - `x[k]`     → "x"        (map/slice index 書込)
//   - `x.f`      → "x"        (フィールド書込)
//   - `x.f[i].g` → "x"
//   - `*x`       → ""(ポインタ経由は保守的に対象外)
func rootIdent(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return rootIdent(x.X)
	case *ast.SelectorExpr:
		return rootIdent(x.X)
	}
	return ""
}

func isExported(name string) bool {
	if name == "" {
		return false
	}
	return unicode.IsUpper([]rune(name)[0])
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
