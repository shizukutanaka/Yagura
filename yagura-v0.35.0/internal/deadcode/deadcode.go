// Package deadcode は package 内で誰からも参照されない unexported 宣言
// (dead code)を go/ast で検出する(ソクラテス新視点 v0.36)。
//
// 動機:
//
//	apidoc は未文書化の *公開* 契約を見た。その双対が *非公開* 側――unexported な
//	func/type/const/var で、自 package 内のどこからも参照されないもの。Go コンパイラは
//	未使用の *ローカル変数* と *import* は弾くが、package レベルの未使用識別子は弾か
//	ない。よって dead な unexported 宣言はコードベースの一生を通じて静かに溜まる。
//
//	unexported を対象にするのは安全だから: unexported シンボルは自 package 内からのみ
//	可視 = 閉じた世界。package 内のどのファイルも参照しなければ、どこからも到達不能と
//	断定できる(whole-program 解析も import alias の曖昧さも不要)。解析は保守的
//	(参照ありに倒す)に振り、それでも本物の dead code を見つける。
//
// 対象外(false positive 回避):
//   - exported シンボル(外部から参照されうる。unused-export は別問題で危険)
//   - unexported method(interface 満足で間接呼出されうる)
//   - init / main / 空白識別子
//   - *_test.go の宣言(テスト足場。ただし参照側としては test も数える)
//
// stdlib の go/parser + go/ast のみ(ADR-0001)。型情報不要・決定論的。
package deadcode

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Finding は 1 件の dead code(または parse error)。
type Finding struct {
	Package  string `json:"package"`
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
	FilesScanned       int       `json:"files_scanned"`
	PackagesScanned    int       `json:"packages_scanned"`
	DeclaredUnexported int       `json:"declared_unexported"`
	Dead               int       `json:"dead"`
	Findings           []Finding `json:"findings"`
}

// candidate は 1 つの unexported 宣言とその宣言位置集合。
type candidate struct {
	pkg     string
	file    string
	line    int
	name    string
	kind    string
	declPos map[token.Pos]bool
}

// Scan は files(path→content)を package 単位に dead code 解析する。決定論的。
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
		r.PackagesScanned++
		scanPackage(dir, byPkg[dir], files, &r)
	}

	sort.Slice(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Package != b.Package {
			return a.Package < b.Package
		}
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

// fileAST は 1 ファイルのパース結果と test ファイル判定を束ねる。
type fileAST struct {
	f      *ast.File
	isTest bool
}

func scanPackage(dir string, pkgPaths []string, files map[string]string, r *Report) {
	fset := token.NewFileSet()
	asts := parsePackageFiles(dir, pkgPaths, files, fset, r)

	// 1) 候補(非 test ファイルの unexported func/type/const/var)を収集。
	cands := collectPackageCandidates(asts, fset, dir)
	r.DeclaredUnexported += len(cands)
	if len(cands) == 0 {
		return
	}

	// 2) 全ファイル(test 含む)を走査し、宣言位置以外の出現を「参照」とみなす。
	referenced := markReferences(asts, cands)

	// 3) 未参照の候補を dead として報告(name 昇順で決定論的)。
	reportDead(cands, referenced, r)
}

// parsePackageFiles は pkgPaths を同一 fset でパースし、parse-error を r に記録する。
func parsePackageFiles(dir string, pkgPaths []string, files map[string]string, fset *token.FileSet, r *Report) []fileAST {
	asts := make([]fileAST, 0, len(pkgPaths))
	for _, path := range pkgPaths {
		f, err := parser.ParseFile(fset, path, files[path], 0)
		if err != nil {
			line := 1
			var el scanner.ErrorList
			if errors.As(err, &el) && len(el) > 0 {
				line = el[0].Pos.Line
			}
			r.Findings = append(r.Findings, Finding{
				Package: pkgName(dir), File: path, Line: line,
				Rule: "parse-error", Severity: "low",
				Message: "Go source did not parse: " + firstLine(err.Error()),
			})
		}
		if f == nil {
			continue
		}
		asts = append(asts, fileAST{f: f, isTest: strings.HasSuffix(path, "_test.go")})
	}
	return asts
}

// collectPackageCandidates は非 test ファイルの unexported 宣言候補を集める。
func collectPackageCandidates(asts []fileAST, fset *token.FileSet, dir string) map[string]*candidate {
	cands := map[string]*candidate{}
	for _, fa := range asts {
		if fa.isTest {
			continue
		}
		collectCandidates(fa.f, fset, dir, cands)
	}
	return cands
}

// markReferences は全ファイル(test 含む)を走査し、宣言位置以外での出現を参照とみなす。
func markReferences(asts []fileAST, cands map[string]*candidate) map[string]bool {
	referenced := map[string]bool{}
	for _, fa := range asts {
		ast.Inspect(fa.f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			c, ok := cands[id.Name]
			if !ok {
				return true
			}
			if !c.declPos[id.Pos()] {
				referenced[id.Name] = true
			}
			return true
		})
	}
	return referenced
}

// reportDead は未参照候補を Findings へ name 昇順(決定論的)に追加する。
func reportDead(cands map[string]*candidate, referenced map[string]bool, r *Report) {
	names := make([]string, 0, len(cands))
	for n := range cands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if referenced[n] {
			continue
		}
		c := cands[n]
		r.Dead++
		r.Findings = append(r.Findings, Finding{
			Package: c.pkg, File: c.file, Line: c.line, Name: c.name, Kind: c.kind,
			Rule: "dead-unexported", Severity: "low",
			Message: "unexported " + c.kind + " " + c.name +
				" is never referenced within its package — candidate for removal",
		})
	}
}

func collectCandidates(f *ast.File, fset *token.FileSet, dir string, cands map[string]*candidate) {
	add := func(kind string, ident *ast.Ident) {
		name := ident.Name
		if name == "_" || name == "init" || name == "main" || isExported(name) {
			return
		}
		pos := fset.Position(ident.Pos())
		c, ok := cands[name]
		if !ok {
			c = &candidate{
				pkg: pkgName(dir), file: pos.Filename, line: pos.Line,
				name: name, kind: kind, declPos: map[token.Pos]bool{},
			}
			cands[name] = c
		}
		c.declPos[ident.Pos()] = true
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil {
				continue // method は除外(interface 満足で間接呼出されうる)
			}
			add("func", d.Name)
		case *ast.GenDecl:
			collectGenDecl(d, add)
		}
	}
}

// addFunc は collectCandidates が候補識別子を登録するコールバック。
type addFunc func(kind string, ident *ast.Ident)

// collectGenDecl は type/const/var 宣言群の各 spec を候補に登録する。
func collectGenDecl(d *ast.GenDecl, add addFunc) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			add("type", s.Name)
		case *ast.ValueSpec:
			collectValueSpec(s, d.Tok, add)
		}
	}
}

// collectValueSpec は 1 つの const/var spec の各名前を候補に登録する。
func collectValueSpec(s *ast.ValueSpec, tok token.Token, add addFunc) {
	kind := "var"
	if tok == token.CONST {
		kind = "const"
	}
	for _, nm := range s.Names {
		add(kind, nm)
	}
}

func pkgName(dir string) string {
	if dir == "." || dir == "" {
		return "."
	}
	return dir
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
