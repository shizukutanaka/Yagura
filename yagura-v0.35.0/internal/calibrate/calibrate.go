// Package calibrate は数値系レンズ(complexity/paramcheck/returncheck/nakedret)の
// しきい値を *コーパス由来* に校正するためのメタレンズ(ソクラテス新視点 XIII —
// quality-lens-spec.md の弱点 W3「threshold arbitrariness」への直接対応)。
//
// 動機:
//
//	complexity(--max 10)・param-check(--max 5)・return-check(--max 3)・
//	naked-ret(--max-lines 30)の既定しきい値は Go コミュニティの慣習値であって、
//	*この* コードベースから導かれた値ではない(W3)。calibrate は対象コーパスの
//	関数を 1 パスで走査し、4 つの数値メトリクスの分布(min/median/p75/p90/p95/p99/
//	max/mean)を算出する。これにより「このリポジトリでは p95 complexity が 7 なので
//	--max 10 は緩い」「p95 params が 4 なので --max 5 は妥当」といった *データ駆動* の
//	しきい値設定が可能になる。findings を出すレンズではなく、しきい値そのものを
//	検証/提案する meta レンズ(coverage / hotspot と同じ meta 軸)。
//
// 対象は **named function(FuncDecl)** のみ——トップレベル関数とメソッド。FuncLit
// (クロージャ)は宣言シグネチャを持たないため計上しない。各関数について:
//
//   - complexity: McCabe 循環的複雑度(complexity レンズと同じ decision point 集合:
//     if/for/range/case/comm/&&/||、base 1)。
//   - params:     引数の数(名前単位、可変長=1、レシーバ除外)。
//   - returns:    戻り値の数(名前単位)。
//   - func_lines: 本体の行数(`{` ~ `}`)。
//
// percentile は線形補間(R-7)。_test.go と TestXxx/BenchmarkXxx/ExampleXxx/FuzzXxx
// は除外。stdlib の go/ast のみ(ADR-0001)。決定論的。
package calibrate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"sort"
	"strings"
)

// metricDefault はメトリクス名 → 対応する Yagura レンズの既定しきい値。
var metricDefaults = []struct {
	Name    string
	Default int
}{
	{"complexity", 10}, // complexity --max 10
	{"params", 5},      // param-check --max 5
	{"returns", 3},     // return-check --max 3
	{"func_lines", 30}, // naked-ret --max-lines 30
}

// Distribution は 1 メトリクスの分布サマリ。
type Distribution struct {
	Metric             string  `json:"metric"`
	Count              int     `json:"count"`
	Min                int     `json:"min"`
	Max                int     `json:"max"`
	Mean               float64 `json:"mean"`
	Median             float64 `json:"median"`
	P75                float64 `json:"p75"`
	P90                float64 `json:"p90"`
	P95                float64 `json:"p95"`
	P99                float64 `json:"p99"`
	CurrentDefault     int     `json:"current_default"`
	OverCurrentDefault int     `json:"over_current_default"`
	SuggestedThreshold int     `json:"suggested_threshold"` // ceil(P95)
}

// Report は Scan の集計。
type Report struct {
	FilesScanned  int            `json:"files_scanned"`
	FuncsScanned  int            `json:"funcs_scanned"`
	Distributions []Distribution `json:"distributions"`
}

// funcMetrics は 1 関数の 4 メトリクス。
type funcMetrics struct {
	complexity int
	params     int
	returns    int
	lines      int
}

// Scan は files を解析し、4 メトリクスの分布を返す。出力は決定論的。
func Scan(files map[string]string) Report {
	r := Report{}
	var all []funcMetrics
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		r.FilesScanned++
		all = append(all, scanFile(path, src)...)
	}
	r.FuncsScanned = len(all)

	// メトリクス別に値列を抽出し、分布を算出。
	for _, md := range metricDefaults {
		vals := make([]int, 0, len(all))
		for _, fm := range all {
			vals = append(vals, fm.value(md.Name))
		}
		r.Distributions = append(r.Distributions, distribution(md.Name, vals, md.Default))
	}
	return r
}

func (fm funcMetrics) value(metric string) int {
	switch metric {
	case "complexity":
		return fm.complexity
	case "params":
		return fm.params
	case "returns":
		return fm.returns
	case "func_lines":
		return fm.lines
	}
	return 0
}

func scanFile(path, src string) []funcMetrics {
	if strings.HasSuffix(path, "_test.go") {
		return nil
	}
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, path, src, parser.AllErrors)
	if f == nil {
		return nil
	}
	var out []funcMetrics
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Type == nil {
			continue
		}
		name := fn.Name.Name
		if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") ||
			strings.HasPrefix(name, "Example") || strings.HasPrefix(name, "Fuzz") {
			continue
		}
		start := fset.Position(fn.Body.Lbrace).Line
		end := fset.Position(fn.Body.Rbrace).Line
		out = append(out, funcMetrics{
			complexity: complexityOf(fn.Body),
			params:     countFields(fn.Type.Params),
			returns:    countFields(fn.Type.Results),
			lines:      end - start + 1,
		})
	}
	return out
}

// complexityOf は McCabe 循環的複雑度(complexity レンズと同一定義)。
// nested FuncLit には降りない。
func complexityOf(body *ast.BlockStmt) int {
	c := 1
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.IfStmt:
			c++
		case *ast.ForStmt:
			c++
		case *ast.RangeStmt:
			c++
		case *ast.CaseClause:
			c++
		case *ast.CommClause:
			c++
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				c++
			}
		}
		return true
	})
	return c
}

// countFields は FieldList の要素を名前単位で数える(可変長/匿名=1)。
func countFields(fl *ast.FieldList) int {
	if fl == nil {
		return 0
	}
	n := 0
	for _, field := range fl.List {
		if len(field.Names) == 0 {
			n++
		} else {
			n += len(field.Names)
		}
	}
	return n
}

// distribution は値列から Distribution を算出する。vals は破壊的に sort される。
func distribution(metric string, vals []int, def int) Distribution {
	d := Distribution{Metric: metric, CurrentDefault: def}
	d.Count = len(vals)
	if d.Count == 0 {
		return d
	}
	// over current default(strictly greater)。
	sum := 0
	for _, v := range vals {
		sum += v
		if v > def {
			d.OverCurrentDefault++
		}
	}
	d.Mean = float64(sum) / float64(d.Count)
	sort.Ints(vals)
	d.Min = vals[0]
	d.Max = vals[len(vals)-1]
	d.Median = percentile(vals, 50)
	d.P75 = percentile(vals, 75)
	d.P90 = percentile(vals, 90)
	d.P95 = percentile(vals, 95)
	d.P99 = percentile(vals, 99)
	d.SuggestedThreshold = int(math.Ceil(d.P95))
	return d
}

// percentile は線形補間(R-7)。sorted は昇順前提、p は 0-100。
func percentile(sorted []int, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return float64(sorted[0])
	}
	rank := p / 100 * float64(n-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return float64(sorted[lo])
	}
	frac := rank - float64(lo)
	return float64(sorted[lo]) + frac*float64(sorted[hi]-sorted[lo])
}
