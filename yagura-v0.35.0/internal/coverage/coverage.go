// Package coverage は scan の *盲点* を明示する meta 視点を提供する(新視点 v0.36)。
//
// ソクラテス的動機:
//
//	既存のレンズは「対象の中に何があるか」(findings)を答えるが、「その clean 判定は
//	どれだけのコードを実際に見たか」(判定そのものの信頼性)は答えない。yagura の
//	scanner は covered 言語(Go/TS/JS/Python/Rust/Java)だけを解析し、それ以外の
//	ソース(.rb/.php/.c/.sh …)は黙って捨てる。リポジトリの半分が Ruby なら "clean" は
//	誤導になる。本 package は全ファイルを「解析可能 / 未対応ソース(=盲点)/ 非ソース」に
//	分類し、coverage 比率を数値化する。これは findings の軸ではなく coverage の meta 軸。
//
// stdlib のみ(ADR-0001)。パスの拡張子のみで分類(内容不要・決定論的)。
package coverage

import (
	"path/filepath"
	"strings"
)

// Report は coverage 分類結果。
//
// Analyzable/CoverageRatio は *sensor 層*(qualitycheck/secretscan/testcoverage、
// polyglot)の視点であり、ASTLensAnalyzable/ASTLensCoverageRatio は *AST quality
// lens 層*(complexity/cognit/nestdepth/.../hotspot/lensoverlap、25+ レンズ、
// go/ast のみで Go 専用)の視点——両者を混同すると、純 Python プロジェクトが
// CoverageRatio=1.0(=「盲点なし」)と読めてしまうが、実際には AST レンズが
// 1 つも動かない(ソクラテス式自己監査、v0.100 lensoverlap に続く発見)。
type Report struct {
	TotalFiles      int            `json:"total_files"`
	Analyzable      int            `json:"analyzable"`       // sensor 層(polyglot)が解析できるソース数
	UncoveredSource int            `json:"uncovered_source"` // 未対応言語のソース数(= 盲点)
	NonSource       int            `json:"non_source"`       // docs/config/data 等
	ByLanguage      map[string]int `json:"by_language"`      // covered 言語別
	UncoveredByExt  map[string]int `json:"uncovered_by_ext"` // 盲点を拡張子別に
	CoverageRatio   float64        `json:"coverage_ratio"`   // analyzable / (analyzable + uncovered_source)、sensor 層
	// AST quality lens 層(Go 専用)の coverage。sensor 層の CoverageRatio が
	// 高くても、非 Go プロジェクトでは複雑度・命名規約等の 25+ レンズが 1 つも
	// 効いていない可能性があることを可視化する。
	ASTLensAnalyzable    int     `json:"ast_lens_analyzable"`
	ASTLensCoverageRatio float64 `json:"ast_lens_coverage_ratio"`
}

// coveredLang は scanner(sensor 層)が解析できる拡張子 → 言語名。
var coveredLang = map[string]string{
	".go": "go", ".ts": "ts", ".tsx": "ts", ".js": "js", ".jsx": "js",
	".py": "python", ".rs": "rust", ".java": "java",
}

// astLensExt は go/ast quality lens 群(complexity/cognit/nestdepth/…)が実際に
// 解析できる拡張子。現状 Go のみ(lens は全て go/ast ベース、ADR-0001)。
var astLensExt = map[string]bool{".go": true}

// uncoveredExt は「コードだが yagura が解析できない」拡張子(= 盲点)。
var uncoveredExt = map[string]bool{
	".rb": true, ".php": true, ".c": true, ".cc": true, ".cpp": true, ".cxx": true,
	".h": true, ".hpp": true, ".cs": true, ".kt": true, ".kts": true, ".swift": true,
	".scala": true, ".sh": true, ".bash": true, ".zsh": true, ".lua": true, ".pl": true,
	".pm": true, ".ex": true, ".exs": true, ".clj": true, ".erl": true, ".hs": true,
	".ml": true, ".r": true, ".dart": true, ".vue": true, ".svelte": true, ".m": true,
	".mm": true, ".groovy": true, ".jl": true, ".nim": true, ".zig": true,
}

// Classify は paths を拡張子で分類した coverage Report を返す。
func Classify(paths []string) Report {
	r := Report{ByLanguage: map[string]int{}, UncoveredByExt: map[string]int{}}
	for _, p := range paths {
		r.TotalFiles++
		ext := strings.ToLower(filepath.Ext(p))
		if lang, ok := coveredLang[ext]; ok {
			r.Analyzable++
			r.ByLanguage[lang]++
			if astLensExt[ext] {
				r.ASTLensAnalyzable++
			}
		} else if uncoveredExt[ext] {
			r.UncoveredSource++
			r.UncoveredByExt[ext]++
		} else {
			r.NonSource++
		}
	}
	code := r.Analyzable + r.UncoveredSource
	if code == 0 {
		r.CoverageRatio = 1.0 // コードが無ければ見落としようがない
		r.ASTLensCoverageRatio = 1.0
	} else {
		r.CoverageRatio = float64(r.Analyzable) / float64(code)
		r.ASTLensCoverageRatio = float64(r.ASTLensAnalyzable) / float64(code)
	}
	return r
}
