// Package coupling は Go の import グラフから package 間結合度を計測する
// (ソクラテス新視点 v0.36)。
//
// 動機:
//
//	complexity は関数 *内部* の絡まりを、errpolicy は関数内のエラーフローを測る――
//	いずれも code unit を単独で見るレンズ。しかし unit *同士* の絡まり(アーキテクチャ)
//	はどのレンズも測っていない。Go は import cycle をコンパイル時に禁じるが、結合の
//	*形* は誰も強制しない。
//
//	各 package には fan-in(Ca: 誰が自分に依存するか)と fan-out(Ce: 自分が誰に
//	依存するか)があり、その比が instability I = Ce/(Ca+Ce)(0=安定, 1=不安定)。
//	Stable Dependencies Principle: 依存は安定方向に向くべき。安定で広く依存される
//	package(低 I)が volatile な package(高 I)に依存すると、volatile 側の変更が
//	「安定なはず」のハブを通って下流全体に波及する――コンパイラが捕まえない architectural smell。
//
//	projectgraph(registry の depends_on = portfolio 宣言)とは別物。本 package は
//	実ソースの import から導く code-level の結合グラフ。
//
// stdlib の go/parser(ImportsOnly)のみ(ADR-0001)。test ファイルは production
// アーキテクチャではないので除外。module root から走査する前提。
package coupling

import (
	"errors"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// sdpMargin は SDP 違反として flag する instability 差の下限(微小差のノイズ抑制)。
const sdpMargin = 0.25

// Package は 1 package の結合度。
type Package struct {
	Name        string   `json:"name"`        // module root からの相対 dir(例: internal/registry)
	FanIn       int      `json:"fan_in"`      // Ca: この package に依存する package 数
	FanOut      int      `json:"fan_out"`     // Ce: この package が依存する内部 package 数
	Instability float64  `json:"instability"` // Ce/(Ca+Ce); 0=安定, 1=不安定
	Imports     []string `json:"imports"`     // 内部依存(sorted)
}

// Finding は 1 件の結合上の問題。
type Finding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	From     string `json:"from"`
	To       string `json:"to,omitempty"`
	Message  string `json:"message"`
}

// Report は Scan の集計。
type Report struct {
	ModulePath   string    `json:"module_path"`
	PackageCount int       `json:"package_count"`
	Packages     []Package `json:"packages"`
	Findings     []Finding `json:"findings"`
}

// Scan は files(path→content)の import から結合グラフを構築する。
// modulePath は go.mod の module path(内部 import の判定に使う)。決定論的。
func Scan(files map[string]string, modulePath string) Report {
	r := Report{ModulePath: modulePath}
	prefix := modulePath + "/"

	deps, allPkgs, parseFindings := buildDepGraph(files, prefix)
	r.Findings = append(r.Findings, parseFindings...)

	ca := computeFanIn(deps)
	instability := instabilityFunc(deps, ca)
	names := sortedKeys(allPkgs)

	r.Packages = buildPackages(names, deps, ca, instability)
	r.PackageCount = len(r.Packages)

	r.Findings = append(r.Findings, detectSDPViolations(names, deps, instability)...)
	sortFindings(r.Findings)
	return r
}

// buildDepGraph は files を module-relative の import グラフ(from-dir → set(to-dir))
// へ変換する。外部/stdlib import は無視。パスは決定論的な順で処理し、parse-error を
// 収集する(呼び出し順に依存しない安定した Findings 順のため)。
func buildDepGraph(files map[string]string, prefix string) (deps map[string]map[string]bool, allPkgs map[string]bool, findings []Finding) {
	deps = map[string]map[string]bool{}
	allPkgs = map[string]bool{}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		allPkgs[dir] = true

		imps, perr := parseImports(path, files[path])
		if perr != "" {
			findings = append(findings, Finding{
				Rule: "parse-error", Severity: "low", From: dir,
				Message: "Go source did not parse: " + perr,
			})
		}
		for _, imp := range imps {
			if !strings.HasPrefix(imp, prefix) {
				continue // 外部 / stdlib import は無視
			}
			to := strings.TrimPrefix(imp, prefix)
			if to == dir {
				continue
			}
			if deps[dir] == nil {
				deps[dir] = map[string]bool{}
			}
			deps[dir][to] = true
			allPkgs[to] = true
		}
	}
	return deps, allPkgs, findings
}

// computeFanIn は各 to-dir への Ca(fan-in、誰が自分に依存するか)を数える。
func computeFanIn(deps map[string]map[string]bool) map[string]int {
	ca := map[string]int{}
	for _, tos := range deps {
		for to := range tos {
			ca[to]++
		}
	}
	return ca
}

// instabilityFunc は I = Ce/(Ca+Ce) を返すクロージャを組み立てる。
func instabilityFunc(deps map[string]map[string]bool, ca map[string]int) func(string) float64 {
	return func(name string) float64 {
		ce := len(deps[name])
		denom := ca[name] + ce
		if denom == 0 {
			return 0
		}
		return float64(ce) / float64(denom)
	}
}

// sortedKeys は bool-set のキーをソート済み slice にする。
func sortedKeys(m map[string]bool) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// buildPackages は各 package の Ca/Ce/Instability/Imports を Package スライスへ集約する。
func buildPackages(names []string, deps map[string]map[string]bool, ca map[string]int, instability func(string) float64) []Package {
	pkgs := make([]Package, 0, len(names))
	for _, n := range names {
		imps := make([]string, 0, len(deps[n]))
		for to := range deps[n] {
			imps = append(imps, to)
		}
		sort.Strings(imps)
		pkgs = append(pkgs, Package{
			Name:        n,
			FanIn:       ca[n],
			FanOut:      len(deps[n]),
			Instability: instability(n),
			Imports:     imps,
		})
	}
	return pkgs
}

// detectSDPViolations は edge from→to で I[to] - I[from] >= margin
// (安定な from が より不安定な to に依存している)を Stable Dependencies Principle
// 違反として報告する。
func detectSDPViolations(names []string, deps map[string]map[string]bool, instability func(string) float64) []Finding {
	var findings []Finding
	for _, from := range names {
		fi := instability(from)
		tos := make([]string, 0, len(deps[from]))
		for to := range deps[from] {
			tos = append(tos, to)
		}
		sort.Strings(tos)
		for _, to := range tos {
			ti := instability(to)
			if ti-fi >= sdpMargin {
				findings = append(findings, Finding{
					Rule: "sdp-violation", Severity: "medium", From: from, To: to,
					Message: "stable package " + from + " (I=" + f2(fi) + ") depends on more-unstable " +
						to + " (I=" + f2(ti) + ") — changes in " + to + " ripple through " + from +
						" to its dependents; consider an interface or reversing the dependency",
				})
			}
		}
	}
	return findings
}

// parseImports は ImportsOnly で import パス一覧と(あれば)parse エラー文を返す。
func parseImports(path, src string) ([]string, string) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
	perr := ""
	if err != nil {
		var el scanner.ErrorList
		if errors.As(err, &el) && len(el) > 0 {
			perr = firstLine(el[0].Error())
		} else {
			perr = firstLine(err.Error())
		}
	}
	if f == nil {
		return nil, perr
	}
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		p, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil {
			continue
		}
		out = append(out, p)
	}
	return out, perr
}

func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
}

func f2(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
