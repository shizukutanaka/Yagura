// Package assertcheck はテストのアサーション密度を分析する(新視点 v0.36)。
//
// ソクラテス的動機:
//
//	testcoverage はテストファイルの "存在" を検査する。しかし「テストが存在する」と
//	「テストが何かを主張している」は別問題。アサーション無しのテストは常に緑になる――
//	コードが壊れていても。この hollow test 問題を "アサーション密度"(assertions÷test関数数)
//	で数値化し、信頼できないテストスイートを可視化する。
//
// 検出するアサーション:
//   - stdlib: t.Error / t.Errorf / t.Fatal / t.Fatalf / t.Fail / t.FailNow
//   - 集計単位は Test 関数のみ(Benchmark/Fuzz は対象外)
//
// stdlib のみ(ADR-0001)。テキストスキャン。
package assertcheck

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// FileDensity は 1 テストファイルのアサーション密度。
type FileDensity struct {
	Path       string  `json:"path"`
	TestFuncs  int     `json:"test_funcs"`  // Test* 関数の数
	Assertions int     `json:"assertions"`  // assertion 呼び出し総数
	Density    float64 `json:"density"`     // Assertions / TestFuncs (TestFuncs==0 → 0)
	Hollow     bool    `json:"hollow"`      // TestFuncs>0 && Assertions==0
}

// Report は Scan の集計結果。
type Report struct {
	FilesScanned    int           `json:"files_scanned"`
	TestFiles       int           `json:"test_files"`
	TotalTestFuncs  int           `json:"total_test_funcs"`
	TotalAssertions int           `json:"total_assertions"`
	HollowFiles     int           `json:"hollow_files"`
	AvgDensity      float64       `json:"avg_density"` // テストファイル密度の平均
	Files           []FileDensity `json:"files"`
}

// testFuncRe は Go の Test 関数宣言を検出。Benchmark/Fuzz は含まない。
var testFuncRe = regexp.MustCompile(`(?m)^func (Test[A-Z]\w*)\s*\(`)

// assertionRe は stdlib testing の assertion 呼び出しを検出。
var assertionRe = regexp.MustCompile(`\bt\.(Error|Errorf|Fatal|Fatalf|Fail|FailNow)\s*[(\n]`)

// isTestFile は *_test.go ファイルかを判定する(Go 固有)。
func isTestFile(path string) bool {
	return strings.HasSuffix(filepath.Base(path), "_test.go")
}

// Scan は files (path→content) を解析し Report を返す。
// テストファイル以外はカウントのみ(density 計算対象外)。
// 出力は path でソート済み(決定論的)。
func Scan(files map[string]string) Report {
	r := Report{FilesScanned: len(files)}

	var densities []FileDensity
	var testFileCount int
	for path, content := range files {
		if !isTestFile(path) {
			continue
		}
		testFileCount++
		fd := scanFile(path, content)
		densities = append(densities, fd)
		r.TotalTestFuncs += fd.TestFuncs
		r.TotalAssertions += fd.Assertions
		if fd.Hollow {
			r.HollowFiles++
		}
	}

	sort.Slice(densities, func(i, j int) bool {
		return densities[i].Path < densities[j].Path
	})

	r.TestFiles = testFileCount
	r.Files = densities
	r.AvgDensity = avgDensity(densities)
	return r
}

func scanFile(path, content string) FileDensity {
	funcs := len(testFuncRe.FindAllString(content, -1))
	asserts := len(assertionRe.FindAllString(content, -1))
	density := 0.0
	if funcs > 0 {
		density = float64(asserts) / float64(funcs)
	}
	return FileDensity{
		Path:       path,
		TestFuncs:  funcs,
		Assertions: asserts,
		Density:    density,
		Hollow:     funcs > 0 && asserts == 0,
	}
}

func avgDensity(files []FileDensity) float64 {
	// テスト関数が1つ以上あるファイルのみ平均に含める
	n := 0
	sum := 0.0
	for _, f := range files {
		if f.TestFuncs > 0 {
			sum += f.Density
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
