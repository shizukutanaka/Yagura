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
	P25                float64 `json:"p25"`
	P75                float64 `json:"p75"`
	P90                float64 `json:"p90"`
	P95                float64 `json:"p95"`
	P99                float64 `json:"p99"`
	UpperFence         float64 `json:"upper_fence"` // Q3 + 3*IQR (Tukey far-out)
	CurrentDefault     int     `json:"current_default"`
	OverCurrentDefault int     `json:"over_current_default"`
	SuggestedThreshold int     `json:"suggested_threshold"` // ceil(P95)
}

// Outlier は あるメトリクスで Tukey 外側フェンスを超えた 1 関数(極端値)。
type Outlier struct {
	File   string  `json:"file"`
	Line   int     `json:"line"`
	Func   string  `json:"func"`
	Metric string  `json:"metric"`
	Value  int     `json:"value"`
	Fence  float64 `json:"fence"` // 超えた外側フェンス Q3+3*IQR
}

// Report は Scan の集計。
type Report struct {
	FilesScanned  int            `json:"files_scanned"`
	FuncsScanned  int            `json:"funcs_scanned"`
	Distributions []Distribution `json:"distributions"`
	Outliers      []Outlier      `json:"outliers"`
}

// FuncMetric は 1 関数(named FuncDecl)の 4 メトリクス + 位置。
// regress(時系列回帰検出)など他レンズと共有する公開型。
type FuncMetric struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Func       string `json:"func"`
	Complexity int    `json:"complexity"`
	Params     int    `json:"params"`
	Returns    int    `json:"returns"`
	Lines      int    `json:"lines"`
}

// Value は metric 名(complexity/params/returns/func_lines)に対応する値を返す。
func (fm FuncMetric) Value(metric string) int {
	switch metric {
	case "complexity":
		return fm.Complexity
	case "params":
		return fm.Params
	case "returns":
		return fm.Returns
	case "func_lines":
		return fm.Lines
	}
	return 0
}

// MetricDefault は metric 名に対応する Yagura レンズの既定しきい値を返す(0=不明)。
func MetricDefault(metric string) int {
	for _, md := range metricDefaults {
		if md.Name == metric {
			return md.Default
		}
	}
	return 0
}

// MetricNames は calibrate が扱う 4 メトリクス名を宣言順で返す。
func MetricNames() []string {
	out := make([]string, len(metricDefaults))
	for i, md := range metricDefaults {
		out[i] = md.Name
	}
	return out
}

// FuncMetrics は files 内の named function ごとの FuncMetric を返す(_test.go と
// TestXxx 等は除外)。regress 等が old/new 双方に適用して差分を取るための公開 API。
func FuncMetrics(files map[string]string) []FuncMetric {
	var all []FuncMetric
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		all = append(all, metricsOfFile(path, src)...)
	}
	return all
}

// Scan は files を解析し、4 メトリクスの分布を返す。出力は決定論的。
func Scan(files map[string]string) Report {
	r := Report{}
	for path := range files {
		if strings.HasSuffix(path, ".go") {
			r.FilesScanned++
		}
	}
	all := FuncMetrics(files)
	r.FuncsScanned = len(all)

	// メトリクス別に値列を抽出し、分布を算出 + Tukey 上側フェンス超過を outlier に。
	for _, md := range metricDefaults {
		vals := make([]int, 0, len(all))
		for _, fm := range all {
			vals = append(vals, fm.Value(md.Name))
		}
		d := distribution(md.Name, vals, md.Default)
		r.Distributions = append(r.Distributions, d)
		if d.Count == 0 {
			continue
		}
		// outlier = 統計的極端値(> 外側フェンス)*かつ* 慣習しきい値超過
		// (> CurrentDefault)。前者だけだと returns/params のような低カーディナリティ
		// メトリクスで `(T, error)` 等の慣用コードを拾い過ぎるため、両シグナルの
		// 積を「実際に直すべき極端関数」とする。
		for _, fm := range all {
			v := fm.Value(md.Name)
			if float64(v) > d.UpperFence && v > md.Default {
				r.Outliers = append(r.Outliers, Outlier{
					File: fm.File, Line: fm.Line, Func: fm.Func,
					Metric: md.Name, Value: v, Fence: d.UpperFence,
				})
			}
		}
	}
	sortOutliers(r.Outliers)
	return r
}

// sortOutliers は決定論的順序: Metric → Value desc → File → Line → Func。
func sortOutliers(os []Outlier) {
	sort.Slice(os, func(i, j int) bool {
		a, b := os[i], os[j]
		if a.Metric != b.Metric {
			return a.Metric < b.Metric
		}
		if a.Value != b.Value {
			return a.Value > b.Value
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Func < b.Func
	})
}

func metricsOfFile(path, src string) []FuncMetric {
	if strings.HasSuffix(path, "_test.go") {
		return nil
	}
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, path, src, parser.AllErrors)
	if f == nil {
		return nil
	}
	var out []FuncMetric
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
		out = append(out, FuncMetric{
			File:       path,
			Line:       fset.Position(fn.Pos()).Line,
			Func:       funcDeclName(fn),
			Complexity: complexityOf(fn.Body),
			Params:     countFields(fn.Type.Params),
			Returns:    countFields(fn.Type.Results),
			Lines:      end - start + 1,
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
	d.P25 = percentile(vals, 25)
	d.Median = percentile(vals, 50)
	d.P75 = percentile(vals, 75)
	d.P90 = percentile(vals, 90)
	d.P95 = percentile(vals, 95)
	d.P99 = percentile(vals, 99)
	// Tukey の外側(far-out)フェンス: Q3 + 3*IQR。1.5*IQR の内側フェンスは
	// 大規模コーパスで上側 4 分位の大半を拾い過ぎるため、極端値のみを surface する
	// 外側フェンスを採用(「直すべき monster」の実用シグナル)。
	iqr := d.P75 - d.P25
	d.UpperFence = d.P75 + 3*iqr
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

// funcDeclName は メソッドを `(Recv).Method`、関数を `Func` で表す(他レンズと統一)。
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
