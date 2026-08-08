// Package codehealth は Go の保守性レンズ群を 1 つの package 別ヘルス grade に
// 合成する(ソクラテス新視点 v0.36)。
//
// 動機:
//
//	yagura は保守性を多数の単機能レンズで測れるようになった(complexity / apidoc /
//	deadcode / recvcheck / assertcheck …)。だが保守者が実際に問うのは「この package は
//	*総合的に* 健全か?」であって 8 個の別々の問いではない。reviewgate が *security*
//	scanner 群を 1 つの判定に束ねたのと対に、本 package は *maintainability* シグナルを
//	package 別 grade(A–F)へ束ねる synthesis の軸。
//
//	Score は純関数(シグナル → grade)でテスト可能に保ち、Analyze が各レンズを実際に
//	走らせて PackageSignals を組み立てる(reviewgate と同じく aggregator なので
//	fan-out は高い ＝ instability も高い。これは設計上正しい)。
//
// stdlib のみ(ADR-0001)。決定論的。
package codehealth

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shizukutanaka/yagura/internal/apidoc"
	"github.com/shizukutanaka/yagura/internal/assertcheck"
	"github.com/shizukutanaka/yagura/internal/astcheck"
	"github.com/shizukutanaka/yagura/internal/complexity"
	"github.com/shizukutanaka/yagura/internal/deadcode"
	"github.com/shizukutanaka/yagura/internal/recvcheck"
)

// PackageSignals は 1 package の保守性シグナル(各レンズの集計)。
type PackageSignals struct {
	Package             string `json:"package"`
	Files               int    `json:"files"`
	ExportedTotal       int    `json:"exported_total"`
	UndocumentedExports int    `json:"undocumented_exports"`
	HighComplexityFuncs int    `json:"high_complexity_funcs"`
	DeadDecls           int    `json:"dead_decls"`
	RecvIssues          int    `json:"recv_issues"`
	HollowTestFiles     int    `json:"hollow_test_files"`
	StructuralHigh      int    `json:"structural_high"`   // astcheck high(例: library 内 os.Exit)
	StructuralMedium    int    `json:"structural_medium"` // astcheck medium(空 nil 分岐 / defer-in-loop / err 文字列比較)
}

// PackageGrade は 1 package の合成スコア。
type PackageGrade struct {
	Package string   `json:"package"`
	Score   int      `json:"score"` // 0-100
	Grade   string   `json:"grade"` // A-F
	Reasons []string `json:"reasons,omitempty"`
}

// Report は Score / Analyze の結果。
type Report struct {
	Packages     []PackageGrade `json:"packages"`
	OverallScore int            `json:"overall_score"`
	OverallGrade string         `json:"overall_grade"`
}

// Score はシグナル列を package 別 grade に変換する純関数。
// worst-first(score 昇順 → package 名昇順)で決定論的に並べる。
func Score(sigs []PackageSignals) Report {
	r := Report{}
	sum := 0
	for _, s := range sigs {
		g := scoreOne(s)
		r.Packages = append(r.Packages, g)
		sum += g.Score
	}
	sort.Slice(r.Packages, func(i, j int) bool {
		if r.Packages[i].Score != r.Packages[j].Score {
			return r.Packages[i].Score < r.Packages[j].Score
		}
		return r.Packages[i].Package < r.Packages[j].Package
	})
	if len(r.Packages) == 0 {
		r.OverallScore = 100
	} else {
		r.OverallScore = sum / len(r.Packages)
	}
	r.OverallGrade = grade(r.OverallScore)
	return r
}

// penalized は 1 つの減点とその説明。reasons を減点の大きい順に並べ、
// reasons[0](= 表示上の "top issue")が常に最大の要因になるようにする。
type penalized struct {
	penalty int
	text    string
}

func scoreOne(s PackageSignals) PackageGrade {
	var ps []penalized

	if s.ExportedTotal > 0 && s.UndocumentedExports > 0 {
		p := s.UndocumentedExports * 25 / s.ExportedTotal
		ps = append(ps, penalized{p, strconv.Itoa(s.UndocumentedExports) + " of " +
			strconv.Itoa(s.ExportedTotal) + " exported symbols undocumented (-" + strconv.Itoa(p) + ")"})
	}
	if s.HighComplexityFuncs > 0 {
		p := capPenalty(s.HighComplexityFuncs*4, 30)
		ps = append(ps, penalized{p, strconv.Itoa(s.HighComplexityFuncs) +
			" high-complexity func(s) (-" + strconv.Itoa(p) + ")"})
	}
	if s.DeadDecls > 0 {
		p := capPenalty(s.DeadDecls*5, 20)
		ps = append(ps, penalized{p, strconv.Itoa(s.DeadDecls) + " dead unexported decl(s) (-" + strconv.Itoa(p) + ")"})
	}
	if s.RecvIssues > 0 {
		p := capPenalty(s.RecvIssues*5, 15)
		ps = append(ps, penalized{p, strconv.Itoa(s.RecvIssues) + " receiver-consistency issue(s) (-" + strconv.Itoa(p) + ")"})
	}
	if s.HollowTestFiles > 0 {
		p := capPenalty(s.HollowTestFiles*5, 20)
		ps = append(ps, penalized{p, strconv.Itoa(s.HollowTestFiles) + " hollow test file(s) (-" + strconv.Itoa(p) + ")"})
	}
	if s.StructuralHigh > 0 {
		p := capPenalty(s.StructuralHigh*10, 30)
		ps = append(ps, penalized{p, strconv.Itoa(s.StructuralHigh) + " high-severity structural defect(s) (-" + strconv.Itoa(p) + ")"})
	}
	if s.StructuralMedium > 0 {
		p := capPenalty(s.StructuralMedium*3, 15)
		ps = append(ps, penalized{p, strconv.Itoa(s.StructuralMedium) + " medium structural issue(s) (-" + strconv.Itoa(p) + ")"})
	}

	// 減点の大きい順(同点は text 昇順)で決定論的に並べる。
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].penalty != ps[j].penalty {
			return ps[i].penalty > ps[j].penalty
		}
		return ps[i].text < ps[j].text
	})

	score := 100
	var reasons []string
	for _, p := range ps {
		score -= p.penalty
		reasons = append(reasons, p.text)
	}
	if score < 0 {
		score = 0
	}
	return PackageGrade{Package: s.Package, Score: score, Grade: grade(score), Reasons: reasons}
}

func capPenalty(p, maxVal int) int {
	if p > maxVal {
		return maxVal
	}
	return p
}

func grade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 80:
		return "B"
	case score >= 70:
		return "C"
	case score >= 60:
		return "D"
	default:
		return "F"
	}
}

// Analyze は files を package(ディレクトリ)単位に分け、各保守性レンズを走らせて
// PackageSignals を組み立て、Score で grade 化する。
func Analyze(files map[string]string) Report {
	byPkg := map[string]map[string]string{}
	for path, src := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		if byPkg[dir] == nil {
			byPkg[dir] = map[string]string{}
		}
		byPkg[dir][path] = src
	}

	dirs := make([]string, 0, len(byPkg))
	for d := range byPkg {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var sigs []PackageSignals
	for _, dir := range dirs {
		pkgFiles := byPkg[dir]

		ad := apidoc.Scan(pkgFiles)
		cx := complexity.Scan(pkgFiles, 0)
		dc := deadcode.Scan(pkgFiles)
		rc := recvcheck.Scan(pkgFiles)
		ac := assertcheck.Scan(pkgFiles)
		as := astcheck.ScanFiles(pkgFiles)
		high, med := astBySeverity(as.Findings)

		sigs = append(sigs, PackageSignals{
			Package:             pkgLabel(dir),
			Files:               len(pkgFiles),
			ExportedTotal:       ad.ExportedTotal,
			UndocumentedExports: ad.ExportedTotal - ad.Documented,
			HighComplexityFuncs: cx.OverThreshold,
			DeadDecls:           dc.Dead,
			RecvIssues:          countReal(rc.Findings),
			HollowTestFiles:     ac.HollowFiles,
			StructuralHigh:      high,
			StructuralMedium:    med,
		})
	}
	return Score(sigs)
}

// countReal は parse-error を除いた recvcheck finding 数を返す。
func countReal(fs []recvcheck.Finding) int {
	n := 0
	for _, f := range fs {
		if f.Rule != "parse-error" {
			n++
		}
	}
	return n
}

// astBySeverity は astcheck findings を high / medium に分けて数える
//(low の parse-error 等は健全性スコアに含めない)。
func astBySeverity(fs []astcheck.Finding) (high, medium int) {
	for _, f := range fs {
		switch f.Severity {
		case "high":
			high++
		case "medium":
			medium++
		}
	}
	return high, medium
}

func pkgLabel(dir string) string {
	if dir == "." || dir == "" {
		return "."
	}
	return dir
}
