// Package deprank は Go パッケージの import グラフから各内部パッケージの
// in-degree(被参照数)を算出し、変更時の blast radius が大きいパッケージを
// 可視化する(ソクラテス的盲点 V — パッケージグラフ構造結合)。
//
// 動機:
//
//	errdiscard まで全てのレンズが「関数レベル」(complexity / paramcheck /
//	flagarg / returncheck)か「コールサイトレベル」(errdiscard)で動作しており、
//	*パッケージレベルの構造結合*を捉えるレンズが存在しなかった。
//
//	deprank の着眼点: 内部パッケージの in-degree(何個の内部パッケージが
//	そのパッケージを import しているか)が高いほど、そのパッケージを変更した
//	際にコンパイルエラー・型変更・API 変更の影響が広く伝播する。これを
//	「blast radius」と呼ぶ。in-degree = 5 超を高結合として flag する。
//
// アルゴリズム:
//
//  1. 全 .go ファイル(_test.go 除く)を go/parser(ImportsOnly)で parse。
//  2. 各ファイルのパッケージパスを導出: modulePrefix + "/" + filepath.Dir(relpath)
//     ("/./" を "/" に正規化)。
//  3. import 文を収集し、modulePrefix + "/" で始まる内部 import のみ記録。
//  4. importer pkg → imported pkg の隣接リストを構築。in-degree を集計。
//  5. in-degree 降順・import path 昇順でランク付け。threshold 超過を flag。
//
// stdlib の go/parser(ImportsOnly)のみ(ADR-0001 ゼロ依存)。決定論的。
package deprank

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

const defaultThreshold = 5

// PackageInfo は 1 パッケージの集計。
type PackageInfo struct {
	ImportPath string   `json:"import_path"`
	InDegree   int      `json:"in_degree"`  // 何個の内部パッケージがこのパッケージを import するか
	OutDegree  int      `json:"out_degree"` // このパッケージが import する内部パッケージ数
	Importers  []string `json:"importers"`  // このパッケージを import するパッケージの一覧(sorted)
}

// Finding は threshold 超過の高 in-degree パッケージ 1 件。
type Finding struct {
	ImportPath string `json:"import_path"`
	InDegree   int    `json:"in_degree"`
	Rule       string `json:"rule"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
}

// Report は Scan の集計結果。
type Report struct {
	PackagesScanned int           `json:"packages_scanned"`
	Threshold       int           `json:"threshold"`
	HighCoupling    int           `json:"high_coupling"`
	MaxInDegree     int           `json:"max_in_degree"`
	AvgInDegree     float64       `json:"avg_in_degree"`
	Packages        []PackageInfo `json:"packages"` // 全パッケージ(in_degree 降順 → import_path 昇順)
	Findings        []Finding     `json:"findings"` // threshold 超過のもの
}

// Scan は files(relpath→content)を走査し、内部パッケージの in-degree グラフを構築する。
// modulePrefix は go.mod の module path(例: "github.com/shizukutanaka/yagura")。
// threshold が 0 の場合は defaultThreshold(5)を使用。
// _test.go はスキップ。非 .go ファイルはスキップ。parse エラーはスキップ(クラッシュしない)。
// 出力は決定論的(in-degree 降順 → import_path 昇順)。
func Scan(files map[string]string, modulePrefix string, threshold int) Report {
	if threshold == 0 {
		threshold = defaultThreshold
	}
	internalPrefix := modulePrefix + "/"

	// 決定論的処理のためパスをソート
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// importer → set(imported) の隣接マップ
	edges := map[string]map[string]bool{}
	// 全内部パッケージ集合
	allPkgs := map[string]bool{}

	for _, relpath := range paths {
		content := files[relpath]
		if !strings.HasSuffix(relpath, ".go") {
			continue
		}
		if strings.HasSuffix(relpath, "_test.go") {
			continue
		}

		// このファイルのパッケージパスを導出
		dir := filepath.Dir(relpath)
		pkgPath := importPath(modulePrefix, dir)

		allPkgs[pkgPath] = true

		// go/parser(ImportsOnly)で import 文を取得
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, relpath, content, parser.ImportsOnly)
		if err != nil || f == nil {
			continue
		}

		for _, imp := range f.Imports {
			if imp.Path == nil {
				continue
			}
			raw := imp.Path.Value
			// クォートを除去
			if len(raw) < 2 {
				continue
			}
			imported := raw[1 : len(raw)-1]
			if !strings.HasPrefix(imported, internalPrefix) {
				continue
			}
			// 内部 import のみ記録
			allPkgs[imported] = true
			if edges[pkgPath] == nil {
				edges[pkgPath] = map[string]bool{}
			}
			edges[pkgPath][imported] = true
		}
	}

	// in-degree と importer リストを集計
	inDegree := map[string]int{}
	importers := map[string][]string{} // pkgPath → []importer
	outDegree := map[string]int{}

	for from, toSet := range edges {
		outDegree[from] = len(toSet)
		for to := range toSet {
			inDegree[to]++
			importers[to] = append(importers[to], from)
		}
	}

	// importers をソート
	for k := range importers {
		sort.Strings(importers[k])
	}

	// PackageInfo スライスを構築
	pkgList := make([]string, 0, len(allPkgs))
	for p := range allPkgs {
		pkgList = append(pkgList, p)
	}
	sort.Strings(pkgList)

	packages := make([]PackageInfo, 0, len(pkgList))
	var sumIn int
	var maxIn int
	for _, p := range pkgList {
		in := inDegree[p]
		out := outDegree[p]
		imp := importers[p]
		if imp == nil {
			imp = []string{}
		}
		packages = append(packages, PackageInfo{
			ImportPath: p,
			InDegree:   in,
			OutDegree:  out,
			Importers:  imp,
		})
		sumIn += in
		if in > maxIn {
			maxIn = in
		}
	}

	// in-degree 降順 → import_path 昇順でソート
	sort.SliceStable(packages, func(i, j int) bool {
		if packages[i].InDegree != packages[j].InDegree {
			return packages[i].InDegree > packages[j].InDegree
		}
		return packages[i].ImportPath < packages[j].ImportPath
	})

	// Findings: threshold 超過
	var findings []Finding
	for _, p := range packages {
		if p.InDegree >= threshold {
			findings = append(findings, Finding{
				ImportPath: p.ImportPath,
				InDegree:   p.InDegree,
				Rule:       "deprank",
				Severity:   severity(p.InDegree),
				Message:    "high in-degree " + itoa(p.InDegree) + ": changing this package has blast radius across " + itoa(p.InDegree) + " internal importers",
			})
		}
	}

	// Findings も in-degree 降順 → import_path 昇順でソート
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].InDegree != findings[j].InDegree {
			return findings[i].InDegree > findings[j].InDegree
		}
		return findings[i].ImportPath < findings[j].ImportPath
	})

	var avg float64
	if len(packages) > 0 {
		avg = float64(sumIn) / float64(len(packages))
	}

	return Report{
		PackagesScanned: len(packages),
		Threshold:       threshold,
		HighCoupling:    len(findings),
		MaxInDegree:     maxIn,
		AvgInDegree:     avg,
		Packages:        packages,
		Findings:        findings,
	}
}

// importPath は modulePrefix と dir(relpath の dirname)から Go import パスを導出する。
// "." は "" として扱い、modulePrefix そのものを返す。
func importPath(modulePrefix, dir string) string {
	if dir == "." || dir == "" {
		return modulePrefix
	}
	// filepath.Dir は OS パス区切りを返す可能性があるので "/" に統一
	dir = filepath.ToSlash(dir)
	return modulePrefix + "/" + dir
}

// severity は in-degree からレベルを決定する。
// low: 5-9, medium: 10-14, high: 15+
func severity(in int) string {
	switch {
	case in >= 15:
		return "high"
	case in >= 10:
		return "medium"
	default:
		return "low"
	}
}

// itoa は int を文字列に変換する(strconv 非 import 版)。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
