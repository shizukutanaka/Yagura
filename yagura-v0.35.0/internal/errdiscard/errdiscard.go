// Package errdiscard は Go のコールサイトで返された error が
// 無視されている箇所を go/ast で検出する(ソクラテス新視点)。
//
// 動機:
//
//	paramcheck は関数の入口の広さ(引数の数)を測り、flagarg は引数の意味的制御結合
//	(bool 旗引数)を測り、returncheck は出口の幅(戻り値の数)を測る——
//	三軸で関数のシグネチャ全体像が揃った。しかし、この三つは
//	*定義側* のプロファイルであり、*呼び出し側* の規律を見ていない。
//
//	コールサイト規律の盲点: error を返す関数が ExprStmt として呼ばれると、
//	その error は暗黙的に捨てられる。`os.Remove(path)` を式文で呼ぶ、
//	`json.Unmarshal(b, &v)` の戻り値を無視する——こうした箇所は
//	コンパイラが弾かず、go vet も(一部を除き)素通りする。
//
//	本パッケージはソクラテス的ブラインドスポット IV として「コールサイト規律」
//	の第一形態を可視化する: 同一ファイルセット内で error を返すと判明している
//	関数が ExprStmt として呼ばれている箇所を列挙する。
//
//	二パス AST 走査(型情報不要・zero-dep):
//	  Pass 1: 全 FuncDecl を走査し「最終戻り値が error 型」の関数名を収集。
//	  Pass 2: 全 ExprStmt を走査し、そのコール先が Pass 1 の集合に含まれれば flag。
//
//	これは *同一パッケージ内* のコールにのみ効く(外部パッケージ呼び出しは型情報が
//	ないと解決できない)。しかし最も即効性の高い自己観察——自分のコードが
//	自分で書いた error を捨てていないか——を、zero-dep で機械的に検出できる。
//
// stdlib の go/ast のみ(ADR-0001 ゼロ依存)。型情報不要・決定論的。
package errdiscard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
)

// Finding は 1 件の error-discard 指摘。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Caller   string `json:"caller,omitempty"` // 呼び出し元 FuncDecl 名(トップレベルなら空)
	Callee   string `json:"callee"`           // error を捨てられた関数名
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned    int       `json:"files_scanned"`
	CallsScanned    int       `json:"calls_scanned"`    // ExprStmt CallExpr 合計
	ErrorsDiscarded int       `json:"errors_discarded"` // findings 件数
	Findings        []Finding `json:"findings"`
}

// Scan は files(relpath→content)を二パス走査し、error を返す関数が
// ExprStmt として呼ばれている(= error を捨てている)箇所を報告する。
// _test.go はスキップ。非 .go ファイルはスキップ。
// 出力は決定論的(File→Line)。
func Scan(files map[string]string) Report {
	var r Report

	// Pass 1: error を返す関数名の集合を構築
	errorFuncs := make(map[string]bool)
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		collectErrorFuncs(path, src, errorFuncs)
	}

	// Pass 2: ExprStmt の CallExpr を走査し、コール先が errorFuncs に含まれれば flag
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		r.FilesScanned++
		scanFile(path, src, errorFuncs, &r)
	}

	sort.Slice(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return r
}

// collectErrorFuncs は src を parse し、最終戻り値が "error" 型の
// FuncDecl 名を errorFuncs に追加する。parse エラーは無視。
func collectErrorFuncs(path, src string, errorFuncs map[string]bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Type == nil {
			continue
		}
		if lastReturnIsError(fn.Type) {
			errorFuncs[fn.Name.Name] = true
		}
	}
}

// lastReturnIsError は FuncType の最後の結果フィールドが
// error 型 (*ast.Ident{Name:"error"}) かどうかを返す。
func lastReturnIsError(ft *ast.FuncType) bool {
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return false
	}
	last := ft.Results.List[len(ft.Results.List)-1]
	id, ok := last.Type.(*ast.Ident)
	return ok && id.Name == "error"
}

// scanFile は src を parse し ExprStmt の CallExpr を走査する。
// FuncDecl の span(開始行〜終了行)を使って Caller を確定する。
func scanFile(path, src string, errorFuncs map[string]bool, r *Report) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return
	}

	// FuncDecl の行範囲を収集
	type funcSpan struct {
		name  string
		start int
		end   int
	}
	var spans []funcSpan
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		start := fset.Position(fn.Pos()).Line
		end := fset.Position(fn.End()).Line
		spans = append(spans, funcSpan{fn.Name.Name, start, end})
	}

	// callerAt は line 番号を受け取り、その行を含む FuncDecl 名を返す。
	// どの FuncDecl にも属さない場合は "" を返す。
	callerAt := func(line int) string {
		for _, sp := range spans {
			if line >= sp.start && line <= sp.end {
				return sp.name
			}
		}
		return ""
	}

	// ExprStmt を走査
	ast.Inspect(f, func(n ast.Node) bool {
		stmt, ok := n.(*ast.ExprStmt)
		if !ok {
			return true
		}
		call, ok := stmt.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		r.CallsScanned++
		callee := calleeName(call)
		if callee != "" && errorFuncs[callee] {
			pos := fset.Position(stmt.Pos())
			r.ErrorsDiscarded++
			r.Findings = append(r.Findings, Finding{
				File:     path,
				Line:     pos.Line,
				Caller:   callerAt(pos.Line),
				Callee:   callee,
				Rule:     "errdiscard",
				Severity: "medium",
				Message:  "error return from " + callee + " discarded at call site",
			})
		}
		return true
	})
}

// calleeName は CallExpr のコール先の単純名を返す。
// plain call `f()` → "f"
// method/pkg call `x.F()` → "F"
// 判定できない場合は ""。
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		// 裸の呼び出し = 同一パッケージの関数。名前で解決できる **唯一** の形。
		return fn.Name
	case *ast.SelectorExpr:
		// `x.Foo(...)` のレシーバ型は型情報なしでは決定できない。名前だけで
		// 同名の error 返し関数に結び付けると、まったく無関係な
		// `w.Header().Set(...)`(戻り値なし)まで「error を捨てた」と報告してしまう
		// ——自リポジトリで実際に 107 件中の大半がこれだった(v1.3.0 で修正)。
		// 決定不能なものは **報告しない**(このリポジトリの保守的規約)。
		return ""
	}
	return ""
}
