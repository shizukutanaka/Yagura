// Package nakedret は「長い関数内の naked return」を go/ast で検出する
// (ソクラテス新視点 XI — Qiita/Zenn 調査 v0.78; alexkohler/nakedret 由来)。
//
// 動機:
//
//	returncheck は signature の戻り値 *数*(出口の幅)を測る。しかし「本体の中で
//	どう返すか」——named result を使った `return`(naked return)——は別の可読性軸
//	であり、どのレンズも見ていなかった。短い関数なら naked return は無害だが、
//	数十行ある関数の末尾の `return` は「今何が返るのか」を読み手がスクロールして
//	named result の現在値を追わねば分からず、バグの温床になる。これは Go コミュニティ
//	で広く認知された規律(nakedret linter)。
//
// 検査ルール(go/ast、型解決なし、決定論的):
//
//   - naked-return-long-func: named result を持ち、かつ本体が閾値(既定 30)行を
//     超える関数/クロージャ内の、引数なし `return` 文。each occurrence を報告。
//
// naked return は named result を持つ関数でしか書けない(無名 result で naked
// return は compile error)ため、named result の判定だけで対象を絞れる。
// クロージャ(FuncLit)内の naked return は最も内側の関数に帰属させる(外側が
// 長くても内側のクロージャが短ければ無害、逆も同様)。
//
// stdlib の go/ast のみ(ADR-0001)。決定論的(File→Line→Func)。
package nakedret

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

// defaultThreshold は既定の関数行数しきい値(nakedret の既定に合わせ 30)。
const defaultThreshold = 30

// Finding は 1 件の naked-return 指摘またはパースエラー。
type Finding struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Func      string `json:"func,omitempty"`
	FuncLines int    `json:"func_lines,omitempty"`
	Rule      string `json:"rule"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned int       `json:"files_scanned"`
	Threshold    int       `json:"threshold"`
	Flagged      int       `json:"flagged"`
	Findings     []Finding `json:"findings"`
}

// Scan は files(path→content)を解析し、長い関数内の naked return を報告する。
// threshold<=0 は defaultThreshold(30)を使用。出力は決定論的(File→Line→Func)。
func Scan(files map[string]string, threshold int) Report {
	if threshold <= 0 {
		threshold = defaultThreshold
	}
	r := Report{Threshold: threshold}
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		r.FilesScanned++
		scanFile(path, src, threshold, &r)
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

func scanFile(path, src string, threshold int, r *Report) {
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
	a := &analyzer{fset: fset, path: path, threshold: threshold, r: r}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		a.analyze(funcDeclName(fn), fn.Type, fn.Body)
	}
}

// analyzer は 1 ファイル走査中の不変状態(fset/path/threshold/report)を束ねる。
// これにより analyze の引数を絞る(param-check / calibrate 由来の整理)。
type analyzer struct {
	fset      *token.FileSet
	path      string
	threshold int
	r         *Report
}

// analyze は 1 つの関数(またはクロージャ)本体を解析する。
// この関数本体に直接属する naked return を判定し、ネストした FuncLit は
// それぞれ独立に再帰解析する(naked return は最も内側の関数に帰属)。
func (a *analyzer) analyze(name string, ftype *ast.FuncType, body *ast.BlockStmt) {
	if body == nil {
		return
	}
	named := hasNamedResults(ftype)
	start := a.fset.Position(body.Lbrace).Line
	end := a.fset.Position(body.Rbrace).Line
	lines := end - start + 1

	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			// ネストしたクロージャは独立に解析(naked return は内側に帰属)。
			a.analyze(name, x.Type, x.Body)
			return false // この本体としては降りない
		case *ast.ReturnStmt:
			if len(x.Results) == 0 && named && lines > a.threshold {
				pos := a.fset.Position(x.Pos())
				a.r.Findings = append(a.r.Findings, Finding{
					File: a.path, Line: pos.Line, Func: name, FuncLines: lines,
					Rule: "naked-return-long-func", Severity: "medium",
					Message: "naked return in a " + strconv.Itoa(lines) + "-line function (threshold " +
						strconv.Itoa(a.threshold) + ") — name the returned values explicitly so readers need not scroll to the signature",
				})
			}
		}
		return true
	})
}

// hasNamedResults は FuncType の result が名前付きかを判定する。
func hasNamedResults(ft *ast.FuncType) bool {
	if ft == nil || ft.Results == nil {
		return false
	}
	for _, field := range ft.Results.List {
		if len(field.Names) > 0 {
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
