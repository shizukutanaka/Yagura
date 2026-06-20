package hotspot

import (
	"reflect"
	"strings"
	"testing"
)

// hugeFunc は 1 関数で複数レンズ(複雑度・引数数・戻り値数)を同時に踏む合成ソース。
// 引数 6 個(paramcheck>5)、戻り値 4 個(returncheck>3)、bool 引数(flagarg>=1)、
// if/for を多数(complexity>10)で 4 レンズすべてに引っかかる。
const quadFlagSrc = `package bad

func Monster(a, b, c, d, e int, verbose bool) (int, int, int, error) {
	x := 0
	if a > 0 { x++ } else { x-- }
	if b > 0 { x++ } else { x-- }
	if c > 0 { x++ } else { x-- }
	if d > 0 { x++ } else { x-- }
	if e > 0 { x++ } else { x-- }
	for i := 0; i < a; i++ {
		if i%2 == 0 { x++ }
		if i%3 == 0 { x-- }
	}
	switch {
	case verbose && a > b:
		x *= 2
	case a < b:
		x -= 1
	}
	return x, a, b, nil
}
`

// cleanSrc は どのレンズにも引っかからない健全な関数。
const cleanSrc = `package good

func Add(a, b int) int {
	return a + b
}
`

// twoLensSrc は 2 レンズ(引数数 + 戻り値数)だけ踏む関数。複雑度は低く、bool 無し。
const twoLensSrc = `package mid

func Wide(a, b, c, d, e, f int) (int, int, int, int) {
	return a + b, c + d, e + f, a
}
`

func TestScan_QuadHotspotHighSeverity(t *testing.T) {
	rep := Scan(map[string]string{"bad.go": quadFlagSrc}, 2)
	if len(rep.Hotspots) != 1 {
		t.Fatalf("expected 1 hotspot, got %d: %+v", len(rep.Hotspots), rep.Hotspots)
	}
	h := rep.Hotspots[0]
	if h.Func != "Monster" {
		t.Errorf("expected func Monster, got %q", h.Func)
	}
	if h.Count < 3 {
		t.Errorf("Monster should be flagged by 3+ lenses, got %d (%v)", h.Count, h.Lenses)
	}
	if h.Severity != "high" {
		t.Errorf("3+ lenses should be high severity, got %q", h.Severity)
	}
	// lenses must be sorted and contain the expected set
	want := []string{"complexity", "flagarg", "paramcheck", "returncheck"}
	if !reflect.DeepEqual(h.Lenses, want) {
		t.Errorf("expected lenses %v, got %v", want, h.Lenses)
	}
}

func TestScan_CleanFuncNoHotspot(t *testing.T) {
	rep := Scan(map[string]string{"good.go": cleanSrc}, 2)
	if len(rep.Hotspots) != 0 {
		t.Errorf("clean func should produce no hotspots, got %+v", rep.Hotspots)
	}
}

func TestScan_TwoLensMediumSeverity(t *testing.T) {
	rep := Scan(map[string]string{"mid.go": twoLensSrc}, 2)
	if len(rep.Hotspots) != 1 {
		t.Fatalf("expected 1 hotspot, got %d: %+v", len(rep.Hotspots), rep.Hotspots)
	}
	h := rep.Hotspots[0]
	if h.Count != 2 {
		t.Errorf("Wide should be flagged by exactly 2 lenses, got %d (%v)", h.Count, h.Lenses)
	}
	if h.Severity != "medium" {
		t.Errorf("2 lenses should be medium severity, got %q", h.Severity)
	}
	want := []string{"paramcheck", "returncheck"}
	if !reflect.DeepEqual(h.Lenses, want) {
		t.Errorf("expected lenses %v, got %v", want, h.Lenses)
	}
}

func TestScan_MinLensesThreshold(t *testing.T) {
	// With minLenses=3, the 2-lens "Wide" func must NOT be reported.
	rep := Scan(map[string]string{"mid.go": twoLensSrc}, 3)
	if len(rep.Hotspots) != 0 {
		t.Errorf("minLenses=3 should exclude 2-lens func, got %+v", rep.Hotspots)
	}
}

func TestScan_DefaultMinLensesWhenZero(t *testing.T) {
	// minLenses<=1 is meaningless (every single-lens finding would be a "hotspot");
	// Scan must clamp to the default of 2.
	rep := Scan(map[string]string{"mid.go": twoLensSrc}, 0)
	if rep.MinLenses != 2 {
		t.Errorf("expected MinLenses clamped to 2, got %d", rep.MinLenses)
	}
	if len(rep.Hotspots) != 1 {
		t.Errorf("default minLenses=2 should report the 2-lens func, got %+v", rep.Hotspots)
	}
}

func TestScan_FuncsFlaggedCountsSingleLens(t *testing.T) {
	// A func flagged by exactly 1 lens is counted in FuncsFlagged but is not a hotspot.
	// Monster (multi) + a single-param-only func.
	singleParam := `package p

func OnlyWide(a, b, c, d, e, f int) int { return a }
`
	rep := Scan(map[string]string{"bad.go": quadFlagSrc, "sp.go": singleParam}, 2)
	if rep.FuncsFlagged < 2 {
		t.Errorf("expected at least 2 funcs flagged by >=1 lens, got %d", rep.FuncsFlagged)
	}
	// OnlyWide is single-lens (paramcheck only) → not a hotspot
	for _, h := range rep.Hotspots {
		if h.Func == "OnlyWide" {
			t.Errorf("OnlyWide is single-lens, should not be a hotspot: %+v", h)
		}
	}
}

func TestScan_DeterministicOrder(t *testing.T) {
	files := map[string]string{"bad.go": quadFlagSrc, "mid.go": twoLensSrc}
	r1 := Scan(files, 2)
	r2 := Scan(files, 2)
	if !reflect.DeepEqual(r1, r2) {
		t.Errorf("Scan must be deterministic:\n%+v\n%+v", r1, r2)
	}
	// Higher count must sort first (Monster 3+ before Wide 2).
	if len(r1.Hotspots) != 2 {
		t.Fatalf("expected 2 hotspots, got %d", len(r1.Hotspots))
	}
	if r1.Hotspots[0].Count < r1.Hotspots[1].Count {
		t.Errorf("hotspots must be sorted by count desc: %+v", r1.Hotspots)
	}
	if r1.Hotspots[0].Func != "Monster" {
		t.Errorf("Monster (higher count) should sort first, got %q", r1.Hotspots[0].Func)
	}
}

func TestScan_MessageMentionsLenses(t *testing.T) {
	rep := Scan(map[string]string{"mid.go": twoLensSrc}, 2)
	if len(rep.Hotspots) != 1 {
		t.Fatalf("expected 1 hotspot")
	}
	msg := rep.Hotspots[0].Message
	if !strings.Contains(msg, "2") || !strings.Contains(strings.ToLower(msg), "lens") {
		t.Errorf("message should mention the lens count: %q", msg)
	}
}

func TestScan_TestFileSkipped(t *testing.T) {
	// The sub-lenses skip _test.go, so a monster in a _test.go file yields no hotspots.
	rep := Scan(map[string]string{"bad_test.go": quadFlagSrc}, 2)
	if len(rep.Hotspots) != 0 {
		t.Errorf("_test.go funcs should be skipped, got %+v", rep.Hotspots)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	rep := Scan(map[string]string{"readme.txt": quadFlagSrc}, 2)
	if len(rep.Hotspots) != 0 {
		t.Errorf("non-Go files should be ignored, got %+v", rep.Hotspots)
	}
}

func TestScan_ParseErrorNoCrash(t *testing.T) {
	rep := Scan(map[string]string{"broken.go": "package p\nfunc ("}, 2)
	if len(rep.Hotspots) != 0 {
		t.Errorf("broken file should yield no hotspots, got %+v", rep.Hotspots)
	}
}

func TestScan_EmptyInput(t *testing.T) {
	rep := Scan(map[string]string{}, 2)
	if rep.FuncsFlagged != 0 || len(rep.Hotspots) != 0 {
		t.Errorf("empty input should give empty report, got %+v", rep)
	}
}

func TestScan_FilesScannedTracked(t *testing.T) {
	rep := Scan(map[string]string{"bad.go": quadFlagSrc, "good.go": cleanSrc}, 2)
	if rep.FilesScanned != 2 {
		t.Errorf("expected 2 files scanned, got %d", rep.FilesScanned)
	}
}
