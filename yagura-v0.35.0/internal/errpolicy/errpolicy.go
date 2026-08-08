// Package errpolicy は Go ソースの「エラー診断可能性」を go/ast で静的に計測する
// (ソクラテス新視点 v0.36)。
//
// 動機:
//
//	yagura の hardening テーマは一貫して「サイレントな fail-open を loud に」だった。
//	flowrisk は操作の *順序* を、astcheck は構造的アンチパターンを見る。しかし
//	「エラーが発生して伝播するとき、私たちは *どこで・なぜ* 失敗したか分かるか?」
//	という診断可能性(diagnosability)の軸はどのレンズも測っていなかった。
//
//	  - naked propagation: `return err` は何が失敗したかは伝えるが、呼び出し連鎖の
//	    *どこ* で失敗したかを失う。context 付き(`fmt.Errorf("...: %w", err)`)なら
//	    スタックを言葉で辿れる。
//	  - blank discard: `_ = doThing()` は戻り値(error かもしれない)を明示的に捨てる。
//	    失敗はログ行にすらならない――hardening が戦ってきた fail-open そのもの。
//
//	本 package は wrap 率(context 付き返却 / (context 付き + naked))を headline 指標と
//	し、blank-discard を actionable finding として surface する。defect 検出ではなく
//	*規律(discipline)* の meta レンズ。
//
// 型情報不要・go/parser + go/ast のみ(ADR-0001 ゼロ依存)。Go の慣習
// (error 戻り値型は `error`、伝播変数は小文字 `err`、sentinel は `ErrXxx`)に
// 依拠して type-free に判定する。
package errpolicy

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
	"unicode"
)

// Finding は 1 件の診断可能性に関する検出。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned   int       `json:"files_scanned"`
	WrappedReturns int       `json:"wrapped_returns"` // fmt.Errorf(...%w...) で context を付けた返却
	NakedReturns   int       `json:"naked_returns"`   // `return ..., err`(context 無しの素通し)
	BlankDiscards  int       `json:"blank_discards"`  // `_ = call()`(戻り値を明示的に捨てる)
	WrapRatio      float64   `json:"wrap_ratio"`      // Wrapped / (Wrapped + Naked); 分母 0 → 0
	Findings       []Finding `json:"findings"`
}

// Scan は files(path→content)を解析して Report を返す。出力は決定論的。
func Scan(files map[string]string) Report {
	r := Report{FilesScanned: len(files)}
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			r.FilesScanned-- // 非 Go は対象外(カウントから除く)
			continue
		}
		scanFile(path, src, &r)
	}
	if d := r.WrappedReturns + r.NakedReturns; d > 0 {
		r.WrapRatio = float64(r.WrappedReturns) / float64(d)
	}
	sortFindings(r.Findings)
	return r
}

func scanFile(path, src string, r *Report) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.AllErrors)
	if err != nil {
		line := 1
		var el scanner.ErrorList
		if errors.As(err, &el) && len(el) > 0 {
			line = el[0].Pos.Line
		}
		r.Findings = append(r.Findings, Finding{
			File: path, Line: line, Column: 1,
			Rule: "parse-error", Severity: "low",
			Message: "Go source did not parse: " + firstLine(err.Error()),
		})
	}
	if f == nil {
		return
	}

	// 1) error を返す全関数本体(FuncDecl/FuncLit)を収集し、return を分類する。
	//    nested FuncLit は独自エントリとして別途処理するため、各 body の走査では
	//    入れ子の FuncLit に降りない(二重計上・誤帰属を防ぐ)。
	ast.Inspect(f, func(n ast.Node) bool {
		var body *ast.BlockStmt
		var ft *ast.FuncType
		switch fn := n.(type) {
		case *ast.FuncDecl:
			body, ft = fn.Body, fn.Type
		case *ast.FuncLit:
			body, ft = fn.Body, fn.Type
		default:
			return true
		}
		if body == nil || !returnsError(ft) {
			return true
		}
		classifyReturns(path, body, fset, r)
		return true
	})

	// 2) blank-discard は関数文脈に依らずファイル全体で検出する。
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if isPureBlankDiscard(as) {
			r.BlankDiscards++
			pos := fset.Position(as.Pos())
			r.Findings = append(r.Findings, Finding{
				File: path, Line: pos.Line, Column: pos.Column,
				Rule: "error-discarded", Severity: "medium",
				Message: "call result discarded via `_ =` — if it returns an error, the failure is silently dropped",
			})
		}
		return true
	})
}

// classifyReturns は body 内の return を分類する(nested FuncLit には降りない)。
func classifyReturns(path string, body *ast.BlockStmt, fset *token.FileSet, r *Report) {
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false // nested closure は独自エントリで処理
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return true
		}
		last := ret.Results[len(ret.Results)-1]
		switch e := last.(type) {
		case *ast.Ident:
			// nil / 大文字始まり(sentinel ErrXxx)は naked 伝播ではない
			if e.Name == "nil" || isExported(e.Name) {
				return true
			}
			// naked return は集計指標(NakedReturns / wrap_ratio)に畳み込む。
			// `return err` は Go では idiomatic で、per-site finding にすると
			// 大量の低価値ノイズが actionable な error-discarded を埋もれさせる
			// (package doc: blank-discard が actionable finding、naked は metric)。
			r.NakedReturns++
		case *ast.CallExpr:
			if isErrorfWrap(e) {
				r.WrappedReturns++
			}
			// errors.New / 自前コンストラクタ / %w 無し Errorf は fresh 扱い(無計上)
		}
		return true
	})
}

// returnsError は関数の結果リストに error 型が含まれるかを返す(type-free)。
func returnsError(ft *ast.FuncType) bool {
	if ft == nil || ft.Results == nil {
		return false
	}
	for _, fld := range ft.Results.List {
		if id, ok := fld.Type.(*ast.Ident); ok && id.Name == "error" {
			return true
		}
	}
	return false
}

// isErrorfWrap は fmt.Errorf(..., %w を含む format, ...) を判定する。
func isErrorfWrap(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Errorf" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "fmt" {
		return false
	}
	if len(call.Args) == 0 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	return strings.Contains(lit.Value, "%w")
}

// isPureBlankDiscard は `_ = call()`(LHS 全て blank、RHS 単一 CallExpr)を判定する。
// `x, _ := f()` のような部分 discard は対象外(意図的な binding を含むため)。
func isPureBlankDiscard(as *ast.AssignStmt) bool {
	if len(as.Rhs) != 1 {
		return false
	}
	if _, ok := as.Rhs[0].(*ast.CallExpr); !ok {
		return false
	}
	for _, lhs := range as.Lhs {
		id, ok := lhs.(*ast.Ident)
		if !ok || id.Name != "_" {
			return false
		}
	}
	return len(as.Lhs) > 0
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

// sortFindings は File→Line→Column→Rule の全順序で並べ替える(決定論的)。
func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.Rule < b.Rule
	})
}
