// Package synccheck は sync 型(Mutex/RWMutex/WaitGroup/Once/Cond)を含む型の
// 値コピー誤用を go/ast で検査する(ソクラテス新視点 X — Qiita/Zenn 調査 v0.77;
// go vet copylocks 由来)。
//
// 動機:
//
//	sync.Mutex 等のロック型は値コピーすると別のロックになり、保護不変が壊れる。
//	go vet copylocks がこれを検出するが、Yagura は ADR-0001 で zero-dep を守る
//	ため `go vet` を別ツールとして呼べない。同じ規律を Yagura 自身のレンズで
//	測れるようにする(同じ機械化を、同じ場所で)。
//
// 検査ルール(いずれも go/ast、型解決なし、決定論的):
//
//   - mutex-value-receiver: ロック型を含む struct のメソッドが値レシーバで定義
//     されている(`func (s Server) Do()`)。呼び出しのたびにロックがコピーされ、
//     インスタンスごとの不変が壊れる。
//   - mutex-by-value-param: 関数/メソッドの引数にロック型(を直接、または含む
//     struct を)値で受け取る(`func f(s Server)`)。
//   - mutex-by-value-return: 関数/メソッドがロック型(を含む型を)値で返す。
//
// 検出は保守的: 既知のロック型名(sync.Mutex / sync.RWMutex / sync.WaitGroup /
// sync.Once / sync.Cond)の literal selector、または匿名埋め込み Mutex/RWMutex
// 等のみ照合。別名 import は型解決を要するため対象外。
//
// 1 ホップの推移: 同じファイル集合内で「Inner にロック → Outer に Inner フィー
// ルド」のような 1 段ネストは検出する(2 パス: まず file set 全体から TypeSpec
// を集めて lock-bearing 集合を確定し、固定点反復で 1 hop 伝播 → 次に FuncDecl
// を走査)。多段ネストは保守的に検出しない(誤検出回避)。
//
// stdlib の go/ast のみ(ADR-0001)。決定論的(File→Line→Name)。
package synccheck

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
)

// Finding は 1 件のロックコピー誤用またはパースエラー。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Name     string `json:"name,omitempty"` // 関数/メソッド名(`(Recv).Method` 形式)
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

// knownLockTypes は「これを field に持つ型は値コピー不可」と判定する型名の集合。
// selector 形式("sync.Mutex" 等)と短縮名("Mutex" 等; 埋め込み・dot import 用)。
var knownLockTypes = map[string]bool{
	"sync.Mutex":     true,
	"sync.RWMutex":   true,
	"sync.WaitGroup": true,
	"sync.Once":      true,
	"sync.Cond":      true,
}

// shortLockNames は埋め込み形(`type S struct { sync.Mutex }` のように
// SelectorExpr 全体としてマッチする)と、dot-import 風に Mutex 単独で書かれた
// 場合を救うための名前。後者は誤検出を避けるため struct のフィールド型として
// のみ照合する。
var shortLockNames = map[string]bool{
	"Mutex":     true,
	"RWMutex":   true,
	"WaitGroup": true,
	"Once":      true,
	"Cond":      true,
}

// Scan は files(path→content)を解析し、ロックコピー誤用を報告する。
// 出力は決定論的(File→Line→Name)。
func Scan(files map[string]string) Report {
	r := Report{}
	// Pass 1: ファイル集合全体から TypeSpec を集め、ロック含有型集合を構築。
	parsed := map[string]*ast.File{}
	fset := token.NewFileSet()
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		r.FilesScanned++
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
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
		if f != nil {
			parsed[path] = f
		}
	}
	locky := collectLockyTypes(parsed)

	// Pass 2: FuncDecl を走査して 3 ルールを適用。
	paths := make([]string, 0, len(parsed))
	for p := range parsed {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		f := parsed[path]
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			checkFunc(fset, path, fn, locky, &r)
		}
	}
	sortFindings(r.Findings)
	for _, f := range r.Findings {
		if f.Rule != "parse-error" {
			r.Flagged++
		}
	}
	return r
}

// collectLockyTypes は file set 全体から struct 型を集め、
// 「直接または 1 ホップで sync ロック型を含む」型名の集合を返す。
//
// 固定点反復 1 段で 1 hop の推移を解決(`Outer{ Inner }` で Inner が locky なら
// Outer も locky)。多段は保守的に解決しない(false positive 回避)。
func collectLockyTypes(parsed map[string]*ast.File) map[string]bool {
	// 各 struct 型の field type のリストを収集。
	structFields := map[string][]ast.Expr{}
	for _, f := range parsed {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				var fields []ast.Expr
				for _, fld := range st.Fields.List {
					fields = append(fields, fld.Type)
				}
				structFields[ts.Name.Name] = fields
			}
		}
	}

	locky := map[string]bool{}
	// 初期: 直接ロック型を含むものを locky に。
	for name, fields := range structFields {
		for _, ft := range fields {
			if isKnownLockType(ft) {
				locky[name] = true
				break
			}
		}
	}
	// 1 hop 固定点(1 イテレーション分): フィールド型が locky type を直接指す
	// (Ident or *Ident)構造体も locky に。
	for {
		changed := false
		for name, fields := range structFields {
			if locky[name] {
				continue
			}
			for _, ft := range fields {
				bare := bareTypeIdent(ft)
				if bare != "" && locky[bare] {
					locky[name] = true
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}
	return locky
}

// checkFunc は 1 つの FuncDecl に対し 3 ルールを適用する。
func checkFunc(fset *token.FileSet, path string, fn *ast.FuncDecl, locky map[string]bool, r *Report) {
	if fn.Type == nil {
		return
	}
	pos := fset.Position(fn.Pos())

	// 1) 値レシーバ: receiver が locky 型(値、ポインタでない)。
	if fn.Recv != nil && len(fn.Recv.List) == 1 {
		recvT := fn.Recv.List[0].Type
		if !isPointer(recvT) {
			bare := bareTypeIdent(recvT)
			if bare != "" && locky[bare] {
				r.Findings = append(r.Findings, Finding{
					File: path, Line: pos.Line,
					Name:     "(" + bare + ")." + fn.Name.Name,
					Rule:     "mutex-value-receiver",
					Severity: "high",
					Message:  "method has a value receiver on lock-bearing type \"" + bare + "\" — each call copies the embedded mutex, breaking invariants (use a pointer receiver)",
				})
			}
		}
	}

	// 2) 値パラメータ: param が locky 型 or 直接ロック型(値)。
	if fn.Type.Params != nil {
		for _, p := range fn.Type.Params.List {
			if isPointer(p.Type) {
				continue
			}
			if isKnownLockType(p.Type) || isLockyByName(p.Type, locky) {
				r.Findings = append(r.Findings, Finding{
					File: path, Line: pos.Line,
					Name:     funcDeclName(fn),
					Rule:     "mutex-by-value-param",
					Severity: "high",
					Message:  "parameter passes a lock-bearing type by value — pass a pointer to preserve the mutex's identity",
				})
				break // 関数あたり 1 件で十分
			}
		}
	}

	// 3) 値返却: return が locky 型 or 直接ロック型(値)。
	if fn.Type.Results != nil {
		for _, p := range fn.Type.Results.List {
			if isPointer(p.Type) {
				continue
			}
			if isKnownLockType(p.Type) || isLockyByName(p.Type, locky) {
				r.Findings = append(r.Findings, Finding{
					File: path, Line: pos.Line,
					Name:     funcDeclName(fn),
					Rule:     "mutex-by-value-return",
					Severity: "medium",
					Message:  "returns a lock-bearing type by value — callers receive a fresh mutex; return a pointer instead",
				})
				break
			}
		}
	}
}

// isKnownLockType は式が sync.Mutex 等の literal selector か判定する。
func isKnownLockType(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	full := pkg.Name + "." + sel.Sel.Name
	return knownLockTypes[full]
}

// isLockyByName は式が「同パッケージ内の locky 型 (struct 名)」を指すか判定する。
// `*T` は false(ポインタはコピー安全)。
func isLockyByName(e ast.Expr, locky map[string]bool) bool {
	if isPointer(e) {
		return false
	}
	bare := bareTypeIdent(e)
	return bare != "" && locky[bare]
}

func isPointer(e ast.Expr) bool {
	_, ok := e.(*ast.StarExpr)
	return ok
}

// bareTypeIdent は `T` / `*T` / `pkg.T` から bare 型名を返す。
// 他の形(slice/map/func 等)では "" を返す。
func bareTypeIdent(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return bareTypeIdent(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		// pkg.Type — short name のみマッチさせる(埋め込み等)
		if _, ok := x.X.(*ast.Ident); ok {
			if shortLockNames[x.Sel.Name] {
				return x.Sel.Name
			}
		}
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
