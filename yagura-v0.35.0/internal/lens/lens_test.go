package lens

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

const complexFile = `package p

func tangled(a, b, c int) int {
	if a > 0 {
		for i := 0; i < b; i++ {
			if i%2 == 0 && a > b {
				switch c {
				case 1:
					a++
				case 2:
					a--
				}
			} else if i%3 == 0 || b > c {
				a += i
			}
		}
	}
	return a
}
`

// レンズ表が単一の情報源であること。**数を固定する**——増減が気づかれず起きないように。
func TestNames_AreSortedAndPinned(t *testing.T) {
	names := Names()
	if len(names) != 29 {
		t.Errorf("lens count: want 29, got %d (%v)", len(names), names)
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("Names() must be sorted for deterministic output: %v", names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("duplicate lens name %q", n)
		}
		seen[n] = true
		if n != strings.ToLower(n) || strings.Contains(n, " ") {
			t.Errorf("lens name %q must be lowercase and space-free", n)
		}
	}
	for _, must := range []string{"complexity", "cognit", "nest_depth", "hotspot", "dead_code"} {
		if !seen[must] {
			t.Errorf("expected lens %q to be registered", must)
		}
	}
}

func TestRun_DispatchesToTheNamedLens(t *testing.T) {
	files := map[string]string{"a.go": complexFile}
	res, err := Run("complexity", files, Options{Threshold: 1})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Lens != "complexity" {
		t.Errorf("lens name: want complexity, got %s", res.Lens)
	}
	if res.Findings == 0 {
		t.Error("a deliberately tangled function should produce a complexity finding")
	}
	if res.Report == nil {
		t.Error("a single-lens run must carry the full report, not just a count")
	}
}

// しきい値が効かないなら、それは値を受け取っているふりをしているだけ。
func TestRun_ThresholdIsHonored(t *testing.T) {
	files := map[string]string{"a.go": complexFile}
	low, err := Run("complexity", files, Options{Threshold: 1})
	if err != nil {
		t.Fatal(err)
	}
	high, err := Run("complexity", files, Options{Threshold: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if low.Findings == 0 {
		t.Fatal("threshold=1 should flag the tangled function")
	}
	if high.Findings != 0 {
		t.Errorf("threshold=1000 should flag nothing, got %d", high.Findings)
	}
}

// 未知のレンズ名は、黙って空を返さず **選べる名前を挙げて** 失敗する。
func TestRun_UnknownLensNamesValidOptions(t *testing.T) {
	_, err := Run("no-such-lens", map[string]string{"a.go": "package p"}, Options{})
	if err == nil {
		t.Fatal("unknown lens must be an error, not an empty result")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no-such-lens") {
		t.Errorf("error should name the bad input: %q", msg)
	}
	if !strings.Contains(msg, "complexity") {
		t.Errorf("error should list valid lens names so the caller can recover: %q", msg)
	}
}

// RunAll は **件数だけ** を返す。全レンズの本文を返すと、統合した意味(context 節約)が消える。
func TestRunAll_ReturnsCountsWithoutBodies(t *testing.T) {
	files := map[string]string{"a.go": complexFile}
	all := RunAll(files, Options{})
	if len(all) != len(Names()) {
		t.Fatalf("RunAll must cover every lens: %d vs %d", len(all), len(Names()))
	}
	for _, r := range all {
		if r.Report != nil {
			t.Errorf("lens %s: RunAll must omit the full report (that is the whole point)", r.Lens)
		}
	}
	var total int
	for _, r := range all {
		total += r.Findings
	}
	if total == 0 {
		t.Error("a tangled file should trip at least one lens across all 29")
	}
	// 決定論: 並びは Names() と一致する
	for i, n := range Names() {
		if all[i].Lens != n {
			t.Errorf("RunAll order must match Names(): %d %s vs %s", i, all[i].Lens, n)
		}
	}
}

// 空入力はエラーではない(空のディレクトリを走査しただけ)。ただし黙って
// 「問題なし」に見えないよう、件数 0 は 0 として返る。
func TestRun_EmptyFilesIsNotAnError(t *testing.T) {
	for _, n := range Names() {
		res, err := Run(n, map[string]string{}, Options{})
		if err != nil {
			t.Errorf("lens %s: empty input should not error: %v", n, err)
			continue
		}
		if res.Findings != 0 {
			t.Errorf("lens %s: empty input should yield 0 findings, got %d", n, res.Findings)
		}
	}
}

// 全レンズが実際に走ること(表に名前だけ在って nil の Run が混ざらない)。
func TestRun_EveryRegisteredLensExecutes(t *testing.T) {
	files := map[string]string{
		"a.go":      complexFile,
		"a_test.go": "package p\n\nimport \"testing\"\n\nfunc helper(t *testing.T) {}\n\nfunc TestX(t *testing.T) { helper(t) }\n",
	}
	for _, n := range Names() {
		res, err := Run(n, files, Options{})
		if err != nil {
			t.Errorf("lens %s failed: %v", n, err)
			continue
		}
		if res.Lens != n {
			t.Errorf("lens %s reported itself as %s", n, res.Lens)
		}
		if res.Report == nil {
			t.Errorf("lens %s returned no report", n)
		}
	}
}

func TestRun_IsDeterministic(t *testing.T) {
	files := map[string]string{"a.go": complexFile}
	first := RunAll(files, Options{})
	for i := 0; i < 3; i++ {
		again := RunAll(files, Options{})
		for j := range first {
			if again[j].Lens != first[j].Lens || again[j].Findings != first[j].Findings {
				t.Fatalf("unstable at %d: %+v vs %+v", j, again[j], first[j])
			}
		}
	}
}

// TestRunAll_IsDeterministicUnderConcurrency は並列化しても出力が
// **バイト単位で同一** であることを固定する。
//
// v1.88.0 で RunAll をレンズ単位で並列化した。理由は v1.87.0 の実測:
// RunAll は 1,000 ファイルで 10 秒、3,843 ファイルで 40 秒かかる
// (自リポジトリ 352 ファイルの「約 4 秒」は小さすぎる標本だった)。
// discovery call としては実害の水準に達している。
//
// レンズは純関数(files map を受け取り、互いに結合しない)なので
// 並列化は安全なはずだが、**「はず」を出荷しない**のがこのリポジトリの規約。
// 順序・件数・要約のすべてが逐次実行と一致することを要求する。
func TestRunAll_IsDeterministicUnderConcurrency(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 40; i++ {
		files[fmt.Sprintf("pkg%02d/file.go", i)] = fmt.Sprintf(`package pkg%02d

import "fmt"

func Do%02d(a, b, c, d, e, f int, flag bool) (int, int, int, int) {
	if a > 0 {
		for i := 0; i < b; i++ {
			if c > i {
				switch d {
				case 1:
					fmt.Println(i)
				}
			}
		}
	}
	var xs []int
	for i := range []int{1, 2, 3} {
		xs = append(xs, i)
	}
	_ = xs
	return a, b, c, d
}
`, i, i)
	}

	first := RunAll(files, Options{})
	if len(first) != len(Names()) {
		t.Fatalf("expected one result per lens: got %d want %d", len(first), len(Names()))
	}
	// 何も指摘しない入力では並列性のバグが隠れる。実際に findings が出ること。
	total := 0
	for _, r := range first {
		total += r.Findings
	}
	if total == 0 {
		t.Fatal("fixture produced no findings at all; concurrency bugs would be invisible")
	}

	for run := 0; run < 8; run++ {
		got := RunAll(files, Options{})
		if len(got) != len(first) {
			t.Fatalf("run %d: length changed: %d vs %d", run, len(got), len(first))
		}
		for i := range got {
			if got[i] != first[i] {
				t.Errorf("run %d: result %d differs: %+v vs %+v", run, i, got[i], first[i])
			}
		}
	}
}
