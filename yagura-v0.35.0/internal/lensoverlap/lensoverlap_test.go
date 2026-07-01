package lensoverlap

import "testing"

func fk(file, fn string) funcKey { return funcKey{file: file, fn: fn} }

// ─── overlapStats (pure core) ─────────────────────────────────

func TestOverlapStats_FullOverlap(t *testing.T) {
	flagged := map[string]map[funcKey]bool{
		"a": {fk("x.go", "F"): true, fk("x.go", "G"): true},
		"b": {fk("x.go", "F"): true, fk("x.go", "G"): true},
	}
	pairs := overlapStats(flagged)
	if len(pairs) != 1 {
		t.Fatalf("want 1 pair, got %d", len(pairs))
	}
	p := pairs[0]
	if p.Jaccard != 1.0 {
		t.Errorf("want Jaccard 1.0, got %v", p.Jaccard)
	}
	if p.Severity != "high" {
		t.Errorf("want high severity for full overlap, got %q", p.Severity)
	}
}

func TestOverlapStats_NoOverlap(t *testing.T) {
	flagged := map[string]map[funcKey]bool{
		"a": {fk("x.go", "F"): true},
		"b": {fk("x.go", "G"): true},
	}
	pairs := overlapStats(flagged)
	p := pairs[0]
	if p.Jaccard != 0 {
		t.Errorf("want Jaccard 0 for disjoint sets, got %v", p.Jaccard)
	}
	if p.Severity != "" {
		t.Errorf("want no severity for zero overlap, got %q", p.Severity)
	}
}

func TestOverlapStats_PartialOverlap(t *testing.T) {
	// a={F,G,H}, b={G,H,I} -> intersection=2, union=4 -> jaccard=0.5
	flagged := map[string]map[funcKey]bool{
		"a": {fk("x.go", "F"): true, fk("x.go", "G"): true, fk("x.go", "H"): true},
		"b": {fk("x.go", "G"): true, fk("x.go", "H"): true, fk("x.go", "I"): true},
	}
	pairs := overlapStats(flagged)
	p := pairs[0]
	if p.Intersection != 2 || p.Union != 4 {
		t.Fatalf("want intersection=2 union=4, got intersection=%d union=%d", p.Intersection, p.Union)
	}
	if p.Jaccard != 0.5 {
		t.Errorf("want Jaccard 0.5, got %v", p.Jaccard)
	}
	if p.Severity != "medium" {
		t.Errorf("want medium severity (0.5 >= 0.4 threshold), got %q", p.Severity)
	}
}

func TestOverlapStats_BothEmptyIsZeroNotNaN(t *testing.T) {
	flagged := map[string]map[funcKey]bool{
		"a": {},
		"b": {},
	}
	pairs := overlapStats(flagged)
	p := pairs[0]
	if p.Union != 0 {
		t.Fatalf("want union 0, got %d", p.Union)
	}
	if p.Jaccard != 0 {
		t.Errorf("want Jaccard 0 (not NaN) when both sets are empty, got %v", p.Jaccard)
	}
}

func TestOverlapStats_AllPairsCovered(t *testing.T) {
	flagged := map[string]map[funcKey]bool{
		"a": {}, "b": {}, "c": {}, "d": {},
	}
	pairs := overlapStats(flagged)
	if len(pairs) != 6 { // C(4,2)
		t.Fatalf("want 6 pairs for 4 lenses, got %d", len(pairs))
	}
}

func TestOverlapStats_SortedJaccardDescending(t *testing.T) {
	flagged := map[string]map[funcKey]bool{
		"a": {fk("x.go", "F"): true},
		"b": {fk("x.go", "F"): true}, // full overlap w/ a -> jaccard 1.0
		"c": {fk("y.go", "G"): true}, // no overlap w/ a or b -> jaccard 0
	}
	pairs := overlapStats(flagged)
	if len(pairs) != 3 {
		t.Fatalf("want 3 pairs, got %d", len(pairs))
	}
	if pairs[0].Jaccard < pairs[1].Jaccard || pairs[1].Jaccard < pairs[2].Jaccard {
		t.Errorf("pairs must be sorted Jaccard descending, got %+v", pairs)
	}
	if pairs[0].Jaccard != 1.0 {
		t.Errorf("top pair should be the full-overlap (a,b) pair, got %+v", pairs[0])
	}
}

func TestOverlapStats_DeterministicTieBreakByLensNames(t *testing.T) {
	// three lenses pairwise disjoint -> all tie at jaccard=0, must break by LensA then LensB.
	flagged := map[string]map[funcKey]bool{
		"z": {fk("a.go", "A"): true},
		"a": {fk("b.go", "B"): true},
		"m": {fk("c.go", "C"): true},
	}
	pairs := overlapStats(flagged)
	want := [][2]string{{"a", "m"}, {"a", "z"}, {"m", "z"}}
	for i, w := range want {
		if pairs[i].LensA != w[0] || pairs[i].LensB != w[1] {
			t.Errorf("pair %d: want (%s,%s), got (%s,%s)", i, w[0], w[1], pairs[i].LensA, pairs[i].LensB)
		}
	}
}

func TestOverlapStats_SingleLensNoPairs(t *testing.T) {
	pairs := overlapStats(map[string]map[funcKey]bool{"solo": {}})
	if len(pairs) != 0 {
		t.Errorf("single lens must produce no pairs, got %+v", pairs)
	}
}

func TestOverlapStats_EmptyInput(t *testing.T) {
	pairs := overlapStats(map[string]map[funcKey]bool{})
	if len(pairs) != 0 {
		t.Errorf("empty input must produce no pairs, got %+v", pairs)
	}
}

// ─── Scan (real-lens integration) ─────────────────────────────

func TestScan_LensesComparedIsTwelve(t *testing.T) {
	rep := Scan(map[string]string{"x.go": "package p\nfunc F() {}\n"})
	if rep.LensesCompared != 12 {
		t.Errorf("want 12 lenses compared, got %d", rep.LensesCompared)
	}
	wantPairs := 12 * 11 / 2
	if len(rep.Pairs) != wantPairs {
		t.Errorf("want %d pairs (C(12,2)), got %d", wantPairs, len(rep.Pairs))
	}
}

func TestScan_TestFileExcludedFromScope(t *testing.T) {
	rep := Scan(map[string]string{"x_test.go": monsterSrc})
	if rep.FilesScanned != 0 {
		t.Errorf("_test.go must be excluded from scope, got FilesScanned=%d", rep.FilesScanned)
	}
}

func TestScan_NonGoFileIgnored(t *testing.T) {
	rep := Scan(map[string]string{"readme.md": "not go source"})
	if rep.FilesScanned != 0 {
		t.Errorf("non-.go file must be ignored, got FilesScanned=%d", rep.FilesScanned)
	}
}

func TestScan_ParseErrorFileExcluded(t *testing.T) {
	rep := Scan(map[string]string{"broken.go": "package p\nfunc ("})
	if rep.FilesScanned != 0 {
		t.Errorf("unparseable file must be excluded from scope, got FilesScanned=%d", rep.FilesScanned)
	}
}

// monsterSrc trips complexity+flagarg+paramcheck+returncheck+cognit
// simultaneously (proven convergence fixture, same as hotspot's
// TestScan_QuadHotspotHighSeverity), used to verify real lenses produce a
// genuine non-zero intersection for at least one pair.
const monsterSrc = `package bad

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

func TestScan_RealLensesShowGenuineOverlap(t *testing.T) {
	rep := Scan(map[string]string{"x.go": monsterSrc})
	var found bool
	for _, p := range rep.Pairs {
		if p.LensA == "cognit" && p.LensB == "complexity" {
			found = true
			if p.Intersection == 0 {
				t.Errorf("expected cognit and complexity to co-flag Monster, got intersection=0")
			}
		}
	}
	if !found {
		t.Fatalf("cognit/complexity pair not found in report")
	}
}

func TestScan_Deterministic(t *testing.T) {
	files := map[string]string{"x.go": monsterSrc}
	r1 := Scan(files)
	r2 := Scan(files)
	if len(r1.Pairs) != len(r2.Pairs) {
		t.Fatalf("nondeterministic pair count: %d vs %d", len(r1.Pairs), len(r2.Pairs))
	}
	for i := range r1.Pairs {
		if r1.Pairs[i] != r2.Pairs[i] {
			t.Errorf("nondeterministic at pair %d: %+v vs %+v", i, r1.Pairs[i], r2.Pairs[i])
		}
	}
}

func TestScan_EmptyInput(t *testing.T) {
	rep := Scan(map[string]string{})
	if rep.FilesScanned != 0 {
		t.Errorf("empty input -> 0 files scanned, got %d", rep.FilesScanned)
	}
	if rep.LensesCompared != 12 {
		t.Errorf("LensesCompared is fixed at 12 regardless of input, got %d", rep.LensesCompared)
	}
}
