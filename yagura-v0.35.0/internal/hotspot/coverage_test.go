// coverage_test.go: hotspot が **関数キーを持つ全レンズ** を束ねていることを固定する。
//
// なぜ必要か:
//
//	hotspot は「複数のレンズが独立に同じ関数を指摘した」収束を報告する。束ねる母数が
//	古いと、収束の判定そのものが静かに弱くなる——findings が減っても「きれいになった」
//	としか見えず、**母数が痩せたせいだと気づけない**。
//
//	これは実際に起きている。CLAUDE.md より: hotspot は発足時の 4 レンズのまま
//	21 レンズ中 4(19%)まで陳腐化し、v0.95 で 12 に拡張されて初めて収束ホットスポットが
//	0 件 → 69 件に急増した。**同じ劣化を二度目は自動で捕まえる。**
//
// 判定方法: レンズ表(internal/lens)を全走査し、Finding が `Func` フィールドを
// 持つ = 関数キーを持つレンズを reflect で特定する。それが hotspot の
// BundledLenses() に含まれていなければ落とす。
package hotspot_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/hotspot"
	"github.com/shizukutanaka/yagura/internal/lens"
)

// lensName → hotspot が使う内部名。表記ゆれ(nest_depth vs nestdepth)を吸収する。
func normalize(s string) string { return strings.ReplaceAll(s, "_", "") }

// hotspot が意図的に除外するレンズと、その理由。
//
// thelper は **テスト専用ファイル** が主題で、hotspot は非テストファイルに
// scope を絞ってから委譲するため、束ねても常に 0 件になる(CLAUDE.md 記載の既定)。
// hotspot 自身は当然対象外(自己参照)。
var deliberatelyExcluded = map[string]string{
	"thelper": "subject is _test.go files; hotspot scopes to non-test files",
	"hotspot": "self",
}

func TestHotspot_BundlesEveryFunctionKeyedLens(t *testing.T) {
	sample := map[string]string{
		"a.go": `package p

func tangled(a, b, c int, verbose bool) (int, int, int, error) {
	if a > 0 {
		for i := 0; i < b; i++ {
			if i%2 == 0 && a > b {
				switch c {
				case 1:
					a++
				default:
					a--
				}
			}
		}
	}
	return a, b, c, nil
}
`,
	}

	bundled := map[string]bool{}
	for _, n := range hotspot.BundledLenses() {
		bundled[normalize(n)] = true
	}

	var missing []string
	for _, name := range lens.Names() {
		if _, skip := deliberatelyExcluded[name]; skip {
			continue
		}
		res, err := lens.Run(name, sample, lens.Options{})
		if err != nil {
			t.Fatalf("lens %s: %v", name, err)
		}
		if !hasFuncKeyedFindings(res.Report) {
			continue // 関数キーを持たないレンズは収束判定に参加できない
		}
		if !bundled[normalize(name)] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("hotspot does not bundle %d function-keyed lens(es): %v\n"+
			"Convergence is measured over a stale population, which silently weakens every "+
			"hotspot result. Add them in internal/hotspot/hotspot.go AND to BundledLenses(), "+
			"or record a deliberate exclusion with its reason in deliberatelyExcluded.",
			len(missing), missing)
	}
}

// BundledLenses() が実在するレンズだけを挙げていること(綴り間違いで
// 「束ねているつもり」になるのを防ぐ)。
func TestHotspot_BundledNamesAllExist(t *testing.T) {
	known := map[string]bool{}
	for _, n := range lens.Names() {
		known[normalize(n)] = true
	}
	for _, n := range hotspot.BundledLenses() {
		if !known[normalize(n)] {
			t.Errorf("BundledLenses() claims %q, which is not a registered lens", n)
		}
	}
}

// hasFuncKeyedFindings は report.Findings の要素が Func フィールドを持つかを返す。
func hasFuncKeyedFindings(report any) bool {
	v := reflect.ValueOf(report)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}
	f := v.FieldByName("Findings")
	if !f.IsValid() || f.Kind() != reflect.Slice {
		return false
	}
	elem := f.Type().Elem()
	if elem.Kind() != reflect.Struct {
		return false
	}
	fld, ok := elem.FieldByName("Func")
	return ok && fld.Type.Kind() == reflect.String
}
