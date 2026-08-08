// Package cognit は Go 関数の認知的複雑度(Cognitive Complexity, Sonar / gocognit
// 互換)を go/ast で計測する(ソクラテス新視点 XVIII、Qiita/Zenn 調査)。
//
// 動機:
//
//	complexity(McCabe)は分岐パスの *数* を、nestdepth は最深ネストの *深さ* を測る
//	——いずれも構造の一面。だが「人間がこの関数を読んで *どれだけ理解しづらいか*」は
//	どちらの単独指標でも捉えきれない。McCabe は case を 1 つずつ +1 する(flat な
//	switch を過大評価)一方、深いネストには鈍感。nestdepth は深さだけを見る。
//
//	認知的複雑度はこの 2 軸を *人間の直感* に合わせて統合する:
//	  - 制御フローを折る構造(if/for/switch/select/論理演算子列/ラベル付き分岐)に基本 +1。
//	  - ただし *ネストするほど* ペナルティが増える(nesting increment): ネスト n 段の
//	    if/for/switch/select は +1 + n。深いピラミッドは指数的でなく線形に重くなる。
//	  - switch は case 数に依らず +1(flat な多分岐は人間に優しい — McCabe との決定的差)。
//	  - else / else if は構造増分 +1 のみ(ネストペナルティなし)。
//	  - 関数リテラル(クロージャ)はネスト段を 1 増やすが、それ自体に基本増分は無い。
//	    複雑度は外側関数に畳み込む(McCabe complexity が別関数計上するのと対照的)。
//	  - 直接再帰(自分自身を呼ぶ)は関数あたり +1。
//
//	既定しきい値 15(golangci-lint の gocognit 推奨域 10-20 の中央)。McCabe の 10 とは
//	別軸の gate であり、両方を併用すると「広いが浅い」関数と「狭いが深い」関数の双方を
//	捕捉できる。
//
// _test.go と TestXxx/BenchmarkXxx/ExampleXxx/FuzzXxx は除外。
// stdlib の go/ast のみ(ADR-0001 ゼロ依存)。型情報不要・決定論的(File→Line→Func)。
package cognit

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

// defaultThreshold は findings/gate の既定の認知的複雑度しきい値。
const defaultThreshold = 15

// FuncCognit は 1 関数の認知的複雑度。
type FuncCognit struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Func   string `json:"func"`
	Cognit int    `json:"cognit"`
}

// Finding はしきい値超過(または parse error)の 1 件。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Func     string `json:"func,omitempty"`
	Cognit   int    `json:"cognit,omitempty"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned  int          `json:"files_scanned"`
	FuncsScanned  int          `json:"funcs_scanned"`
	Threshold     int          `json:"threshold"`
	Functions     []FuncCognit `json:"functions"`
	MaxCognit     int          `json:"max_cognit"`
	AvgCognit     float64      `json:"avg_cognit"`
	OverThreshold int          `json:"over_threshold"`
	Findings      []Finding    `json:"findings"`
}

// Scan は files(path→content)の Go 関数を解析する。threshold<=0 は既定値 15。
// 出力は決定論的(File→Line→Func でソート)。
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
	sort.Slice(r.Functions, func(i, j int) bool { return lessFunc(r.Functions[i], r.Functions[j]) })
	sortFindings(r.Findings)
	var sum int
	for _, f := range r.Functions {
		sum += f.Cognit
		if f.Cognit > r.MaxCognit {
			r.MaxCognit = f.Cognit
		}
	}
	if n := len(r.Functions); n > 0 {
		r.AvgCognit = float64(sum) / float64(n)
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
			File: path, Line: line, Rule: "parse-error", Severity: "low",
			Message: "Go source did not parse: " + firstLine(err.Error()),
		})
	}
	if f == nil {
		return
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if isTestFunc(fn.Name.Name) {
			continue
		}
		r.FuncsScanned++
		c := funcCognit(fn)
		pos := fset.Position(fn.Pos())
		name := funcDeclName(fn)
		r.Functions = append(r.Functions, FuncCognit{File: path, Line: pos.Line, Func: name, Cognit: c})
		if c > threshold {
			r.OverThreshold++
			r.Findings = append(r.Findings, Finding{
				File: path, Line: pos.Line, Func: name, Cognit: c,
				Rule: "high-cognitive-complexity", Severity: severityFor(c),
				Message: "cognitive complexity " + strconv.Itoa(c) + " exceeds threshold " + strconv.Itoa(threshold) +
					" — hard to follow; flatten nesting / extract helpers to lower the human reading cost",
			})
		}
	}
}

// funcCognit は 1 つの関数(method 含む)の認知的複雑度を返す。
func funcCognit(fn *ast.FuncDecl) int {
	st := &state{name: fn.Name.Name}
	if fn.Recv != nil && len(fn.Recv.List) > 0 && len(fn.Recv.List[0].Names) > 0 {
		st.recv = fn.Recv.List[0].Names[0].Name
	}
	st.walkStmt(fn.Body, 0)
	return st.complexity
}

// state は 1 関数走査中の累積状態。
type state struct {
	complexity    int
	name          string // 囲み関数名(直接再帰判定用)
	recv          string // method receiver 変数名("" = 非 method)
	recursionSeen bool   // 再帰は関数あたり 1 回だけ計上
}

// walkStmt は文 s を nesting 段で評価し、制御構文の基本/ネスト増分を加算する。
func (s *state) walkStmt(stmt ast.Stmt, nesting int) {
	switch n := stmt.(type) {
	case *ast.BlockStmt:
		for _, st := range n.List {
			s.walkStmt(st, nesting)
		}
	case *ast.IfStmt:
		s.walkIf(n, nesting)
	case *ast.ForStmt:
		s.complexity += 1 + nesting
		if n.Cond != nil {
			s.walkExpr(n.Cond, nesting)
		}
		s.walkStmt(n.Body, nesting+1)
	case *ast.RangeStmt:
		s.complexity += 1 + nesting
		s.walkExpr(n.X, nesting)
		s.walkStmt(n.Body, nesting+1)
	case *ast.SwitchStmt:
		s.complexity += 1 + nesting
		if n.Tag != nil {
			s.walkExpr(n.Tag, nesting)
		}
		s.walkClauses(n.Body, nesting+1)
	case *ast.TypeSwitchStmt:
		s.complexity += 1 + nesting
		s.walkClauses(n.Body, nesting+1)
	case *ast.SelectStmt:
		s.complexity += 1 + nesting
		s.walkClauses(n.Body, nesting+1)
	case *ast.BranchStmt:
		if n.Label != nil { // labeled break / continue / goto
			s.complexity++
		}
	case *ast.LabeledStmt:
		s.walkStmt(n.Stmt, nesting)
	case *ast.ExprStmt:
		s.walkExpr(n.X, nesting)
	case *ast.AssignStmt:
		for _, e := range n.Rhs {
			s.walkExpr(e, nesting)
		}
	case *ast.ReturnStmt:
		for _, e := range n.Results {
			s.walkExpr(e, nesting)
		}
	case *ast.DeferStmt:
		s.walkExpr(n.Call, nesting)
	case *ast.GoStmt:
		s.walkExpr(n.Call, nesting)
	case *ast.SendStmt:
		s.walkExpr(n.Value, nesting)
	case *ast.DeclStmt:
		s.walkDecl(n.Decl, nesting)
	}
}

// walkIf は if / else if / else を Sonar 規約で評価する。
func (s *state) walkIf(n *ast.IfStmt, nesting int) {
	s.complexity += 1 + nesting
	s.walkExpr(n.Cond, nesting)
	s.walkStmt(n.Body, nesting+1)
	s.walkElse(n.Else, nesting)
}

// walkElse は else 連鎖を評価する。else / else if は構造増分 +1 のみ
// (ネストペナルティなし)。else if の本体は同一チェーン段 +1 で潜る。
func (s *state) walkElse(els ast.Stmt, nesting int) {
	switch e := els.(type) {
	case *ast.IfStmt: // else if
		s.complexity++
		s.walkExpr(e.Cond, nesting)
		s.walkStmt(e.Body, nesting+1)
		s.walkElse(e.Else, nesting)
	case *ast.BlockStmt: // else
		s.complexity++
		s.walkStmt(e, nesting+1)
	}
}

// walkClauses は switch/type-switch/select の case/comm 節の本体を nesting 段で評価する。
func (s *state) walkClauses(body *ast.BlockStmt, nesting int) {
	for _, cl := range body.List {
		switch c := cl.(type) {
		case *ast.CaseClause:
			for _, e := range c.List {
				s.walkExpr(e, nesting)
			}
			for _, st := range c.Body {
				s.walkStmt(st, nesting)
			}
		case *ast.CommClause:
			if c.Comm != nil {
				s.walkStmt(c.Comm, nesting)
			}
			for _, st := range c.Body {
				s.walkStmt(st, nesting)
			}
		}
	}
}

// walkDecl は局所 var/const 宣言の初期化子(クロージャ等)へ潜る。
func (s *state) walkDecl(d ast.Decl, nesting int) {
	gd, ok := d.(*ast.GenDecl)
	if !ok {
		return
	}
	for _, spec := range gd.Specs {
		if vs, ok := spec.(*ast.ValueSpec); ok {
			for _, v := range vs.Values {
				s.walkExpr(v, nesting)
			}
		}
	}
}

// walkExpr は式 e を nesting 段で評価する。論理演算子列の計数、クロージャの
// ネスト増分、直接再帰呼び出しを担う。
func (s *state) walkExpr(e ast.Expr, nesting int) {
	switch n := e.(type) {
	case *ast.BinaryExpr:
		if n.Op == token.LAND || n.Op == token.LOR {
			s.walkLogical(n, token.ILLEGAL, nesting)
			return
		}
		s.walkExpr(n.X, nesting)
		s.walkExpr(n.Y, nesting)
	case *ast.ParenExpr:
		s.walkExpr(n.X, nesting)
	case *ast.CallExpr:
		s.checkRecursion(n)
		s.walkExpr(n.Fun, nesting)
		for _, a := range n.Args {
			s.walkExpr(a, nesting)
		}
	case *ast.FuncLit:
		// クロージャはネスト段を 1 増やすが、それ自体に基本増分は無い。
		if n.Body != nil {
			s.walkStmt(n.Body, nesting+1)
		}
	case *ast.UnaryExpr:
		s.walkExpr(n.X, nesting)
	case *ast.StarExpr:
		s.walkExpr(n.X, nesting)
	case *ast.IndexExpr:
		s.walkExpr(n.X, nesting)
		s.walkExpr(n.Index, nesting)
	case *ast.SelectorExpr:
		s.walkExpr(n.X, nesting)
	case *ast.CompositeLit:
		for _, el := range n.Elts {
			s.walkExpr(el, nesting)
		}
	case *ast.KeyValueExpr:
		s.walkExpr(n.Value, nesting)
	case *ast.SliceExpr:
		s.walkExpr(n.X, nesting)
	}
}

// walkLogical は &&/|| の列を Sonar 規約で計数する。演算子が直前の列と変わるたびに +1。
// (`a && b && c` = 1, `a && b || c` = 2。括弧は列を分断しない。)
func (s *state) walkLogical(e ast.Expr, last token.Token, nesting int) {
	switch n := e.(type) {
	case *ast.ParenExpr:
		s.walkLogical(n.X, last, nesting)
	case *ast.BinaryExpr:
		if n.Op == token.LAND || n.Op == token.LOR {
			if n.Op != last {
				s.complexity++
			}
			s.walkLogical(n.X, n.Op, nesting)
			s.walkLogical(n.Y, n.Op, nesting)
			return
		}
		// 非論理 binary: 列を切り、通常走査に戻す。
		s.walkExpr(n.X, nesting)
		s.walkExpr(n.Y, nesting)
	default:
		// leaf やその他の式: クロージャ/呼び出し/入れ子の論理式を拾うため通常走査へ。
		s.walkExpr(e, nesting)
	}
}

// checkRecursion は直接自己再帰(関数/method が自分自身を呼ぶ)を関数あたり 1 回 +1。
func (s *state) checkRecursion(call *ast.CallExpr) {
	if s.recursionSeen {
		return
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if s.recv == "" && fn.Name == s.name {
			s.complexity++
			s.recursionSeen = true
		}
	case *ast.SelectorExpr:
		if s.recv != "" && fn.Sel.Name == s.name {
			if x, ok := fn.X.(*ast.Ident); ok && x.Name == s.recv {
				s.complexity++
				s.recursionSeen = true
			}
		}
	}
}

func isTestFunc(name string) bool {
	return strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") ||
		strings.HasPrefix(name, "Example") || strings.HasPrefix(name, "Fuzz")
}

func severityFor(c int) string {
	if c > 30 {
		return "high"
	}
	return "medium"
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

func lessFunc(a, b FuncCognit) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Func < b.Func
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
		if a.Func != b.Func {
			return a.Func < b.Func
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
