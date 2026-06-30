// Package ifacebloat はインターフェースのメソッド数を go/ast で計測し、肥大した
// インターフェースを検出する *インターフェース設計軸* のレンズ(ソクラテス新視点 XXI、
// Qiita/Zenn 調査、sashamelentyev/interfacebloat 由来)。
//
// 動機:
//
//	既存レンズは関数・パッケージ・テストの軸を測るが、*インターフェースの粒度* は
//	未計測だった。Rob Pike の格言「The bigger the interface, the weaker the
//	abstraction(インターフェースは大きいほど抽象が弱い)」は Go コミュニティの
//	設計原則(Qiita/Zenn で繰り返し説かれる「インターフェースは小さく保て」)であり、
//	メソッドの多いインターフェースは: ① モック作成が困難、② 利用側に未使用メソッドを
//	強制(Interface Segregation 違反)、③ 抽象が漠然として責務が曖昧、になる。
//	大きなインターフェースは責務ごとに小さく分割すべき——その機会を可視化する。
//
// 計数規約(go/ast、型情報不要):
//   - メソッド宣言 1 つ = 1。埋め込みインターフェース(`io.Reader` 等)= 1
//     (型解決なしに展開できないため要素として 1 計上)。型ユニオン項
//     (`~int | ~string`)= 1(項ごとには数えない)。
//   - 既定しきい値 10(interfacebloat 既定)。超過で flag、medium、2 倍超で high。
//
// _test.go は除外(L4; テスト内のモック用大インターフェースは意図的なことが多い)。
// stdlib の go/ast のみ(ADR-0001 ゼロ依存)。決定論的(File→Line→Name)。
package ifacebloat

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

// defaultThreshold は既定のメソッド数しきい値(interfacebloat 慣習)。
const defaultThreshold = 10

// IfaceInfo は 1 つのインターフェースのメソッド数。
type IfaceInfo struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Name    string `json:"name"`
	Methods int    `json:"methods"`
}

// Finding はしきい値超過(または parse error)の 1 件。
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Name     string `json:"name,omitempty"`
	Methods  int    `json:"methods,omitempty"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	FilesScanned      int         `json:"files_scanned"`
	InterfacesScanned int         `json:"interfaces_scanned"`
	Threshold         int         `json:"threshold"`
	Interfaces        []IfaceInfo `json:"interfaces"`
	MaxMethods        int         `json:"max_methods"`
	OverThreshold     int         `json:"over_threshold"`
	Findings          []Finding   `json:"findings"`
}

// Scan は files(path→content)の名前付きインターフェースを解析する。threshold<=0 は
// 既定値 10。出力は決定論的(File→Line→Name)。
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
	sort.Slice(r.Interfaces, func(i, j int) bool { return lessIface(r.Interfaces[i], r.Interfaces[j]) })
	sortFindings(r.Findings)
	for _, it := range r.Interfaces {
		if it.Methods > r.MaxMethods {
			r.MaxMethods = it.Methods
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
			File: path, Line: line, Rule: "parse-error", Severity: "low",
			Message: "Go source did not parse: " + firstLine(err.Error()),
		})
	}
	if f == nil {
		return
	}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		it, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}
		count := countMethods(it)
		pos := fset.Position(ts.Pos())
		name := ts.Name.Name
		r.InterfacesScanned++
		r.Interfaces = append(r.Interfaces, IfaceInfo{File: path, Line: pos.Line, Name: name, Methods: count})
		if count > threshold {
			r.OverThreshold++
			r.Findings = append(r.Findings, Finding{
				File: path, Line: pos.Line, Name: name, Methods: count,
				Rule: "interface-bloat", Severity: severityFor(count, threshold),
				Message: "interface " + name + " has " + strconv.Itoa(count) + " methods (threshold " +
					strconv.Itoa(threshold) + ") — \"the bigger the interface, the weaker the abstraction\"; " +
					"split into smaller, role-focused interfaces (Interface Segregation)",
			})
		}
		return true
	})
}

// countMethods はインターフェースの要素数を返す。メソッド = 名前ごとに 1、
// 埋め込みインターフェース / 型ユニオン項 = 1。
func countMethods(it *ast.InterfaceType) int {
	if it.Methods == nil {
		return 0
	}
	n := 0
	for _, field := range it.Methods.List {
		if len(field.Names) > 0 {
			n += len(field.Names) // method(s); interface syntax allows 1 name per field
		} else {
			n++ // embedded interface or type-set term
		}
	}
	return n
}

func severityFor(count, threshold int) string {
	if count > 2*threshold {
		return "high"
	}
	return "medium"
}

func lessIface(a, b IfaceInfo) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Name < b.Name
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
