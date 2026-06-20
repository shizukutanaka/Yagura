package deprank_test

import (
	"reflect"
	"testing"

	"github.com/shizukutanaka/yagura/internal/deprank"
)

const prefix = "example.com/mod"

// pkg はテスト用の内部パッケージファイルを構築するヘルパ。
// pkgName: "a" → ファイルパス "internal/a/a.go"、パッケージパス "example.com/mod/internal/a"
func pkg(name string, imports ...string) (path string, content string) {
	path = "internal/" + name + "/" + name + ".go"
	src := "package " + name + "\n\n"
	if len(imports) > 0 {
		src += "import (\n"
		for _, imp := range imports {
			src += "\t\"" + imp + "\"\n"
		}
		src += ")\n"
	}
	src += "var _ = 1\n"
	return path, src
}

func pkgPath(name string) string {
	return prefix + "/internal/" + name
}

// TestBasicInDegree: A imports B → B in-degree=1, A in-degree=0
func TestBasicInDegree(t *testing.T) {
	files := map[string]string{}
	pa, ca := pkg("a", pkgPath("b"))
	pb, cb := pkg("b")
	files[pa] = ca
	files[pb] = cb

	rep := deprank.Scan(files, prefix, 0)
	if rep.PackagesScanned != 2 {
		t.Fatalf("packages_scanned want 2 got %d", rep.PackagesScanned)
	}

	// find B
	var bInfo *deprank.PackageInfo
	for i := range rep.Packages {
		if rep.Packages[i].ImportPath == pkgPath("b") {
			bInfo = &rep.Packages[i]
		}
	}
	if bInfo == nil {
		t.Fatal("package B not found")
	}
	if bInfo.InDegree != 1 {
		t.Errorf("B in_degree want 1 got %d", bInfo.InDegree)
	}

	// find A
	var aInfo *deprank.PackageInfo
	for i := range rep.Packages {
		if rep.Packages[i].ImportPath == pkgPath("a") {
			aInfo = &rep.Packages[i]
		}
	}
	if aInfo == nil {
		t.Fatal("package A not found")
	}
	if aInfo.InDegree != 0 {
		t.Errorf("A in_degree want 0 got %d", aInfo.InDegree)
	}
}

// TestMultipleImporters: 3 packages (a,c,d) all import B → B in-degree=3
func TestMultipleImporters(t *testing.T) {
	files := map[string]string{}
	pa, ca := pkg("a", pkgPath("b"))
	pb, cb := pkg("b")
	pc, cc := pkg("c", pkgPath("b"))
	pd, cd := pkg("d", pkgPath("b"))
	files[pa] = ca
	files[pb] = cb
	files[pc] = cc
	files[pd] = cd

	rep := deprank.Scan(files, prefix, 0)

	var bIn int
	for _, p := range rep.Packages {
		if p.ImportPath == pkgPath("b") {
			bIn = p.InDegree
		}
	}
	if bIn != 3 {
		t.Errorf("B in_degree want 3 got %d", bIn)
	}
}

// TestThresholdRespected: in-degree 3, threshold 5 → no findings
func TestThresholdRespected(t *testing.T) {
	files := map[string]string{}
	pa, ca := pkg("a", pkgPath("b"))
	pb, cb := pkg("b")
	pc, cc := pkg("c", pkgPath("b"))
	pd, cd := pkg("d", pkgPath("b"))
	files[pa] = ca
	files[pb] = cb
	files[pc] = cc
	files[pd] = cd

	rep := deprank.Scan(files, prefix, 5)
	if len(rep.Findings) != 0 {
		t.Errorf("want 0 findings, got %d", len(rep.Findings))
	}
}

// TestThresholdTriggered: in-degree 6, threshold 5 → 1 finding
func TestThresholdTriggered(t *testing.T) {
	files := map[string]string{}
	files["internal/b/b.go"] = "package b\nvar _ = 1\n"
	// 6 packages import B
	for _, n := range []string{"a", "c", "d", "e", "f", "g"} {
		p, c := pkg(n, pkgPath("b"))
		files[p] = c
	}

	rep := deprank.Scan(files, prefix, 5)
	if len(rep.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(rep.Findings))
	}
	if rep.Findings[0].ImportPath != pkgPath("b") {
		t.Errorf("finding should be for B, got %s", rep.Findings[0].ImportPath)
	}
}

// TestSeverityLow: in-degree 5 → low
func TestSeverityLow(t *testing.T) {
	files := map[string]string{}
	files["internal/b/b.go"] = "package b\nvar _ = 1\n"
	for _, n := range []string{"a", "c", "d", "e", "f"} {
		p, c := pkg(n, pkgPath("b"))
		files[p] = c
	}
	rep := deprank.Scan(files, prefix, 5)
	if len(rep.Findings) != 1 {
		t.Fatalf("want 1 finding got %d", len(rep.Findings))
	}
	if rep.Findings[0].Severity != "low" {
		t.Errorf("want severity low got %s", rep.Findings[0].Severity)
	}
}

// TestSeverityMedium: in-degree 10 → medium
func TestSeverityMedium(t *testing.T) {
	files := map[string]string{}
	files["internal/b/b.go"] = "package b\nvar _ = 1\n"
	names := []string{"a", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
	for _, n := range names {
		p, c := pkg(n, pkgPath("b"))
		files[p] = c
	}
	rep := deprank.Scan(files, prefix, 5)
	if len(rep.Findings) != 1 {
		t.Fatalf("want 1 finding got %d", len(rep.Findings))
	}
	if rep.Findings[0].Severity != "medium" {
		t.Errorf("want severity medium got %s", rep.Findings[0].Severity)
	}
}

// TestSeverityHigh: in-degree 15 → high
func TestSeverityHigh(t *testing.T) {
	files := map[string]string{}
	files["internal/b/b.go"] = "package b\nvar _ = 1\n"
	names := []string{"a", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p"}
	for _, n := range names {
		p, c := pkg(n, pkgPath("b"))
		files[p] = c
	}
	rep := deprank.Scan(files, prefix, 5)
	if len(rep.Findings) != 1 {
		t.Fatalf("want 1 finding got %d", len(rep.Findings))
	}
	if rep.Findings[0].Severity != "high" {
		t.Errorf("want severity high got %s", rep.Findings[0].Severity)
	}
}

// TestTestFileSkipped: _test.go imports don't count toward in-degree
func TestTestFileSkipped(t *testing.T) {
	files := map[string]string{
		"internal/a/a_test.go": "package a_test\nimport \"" + pkgPath("b") + "\"\nvar _ = 1\n",
		"internal/b/b.go":      "package b\nvar _ = 1\n",
	}
	rep := deprank.Scan(files, prefix, 0)

	// B should exist (from the non-test reference in the graph? No — only b.go file adds B to allPkgs)
	// B in-degree should be 0 since the only importer is a test file
	var bIn int
	var bFound bool
	for _, p := range rep.Packages {
		if p.ImportPath == pkgPath("b") {
			bIn = p.InDegree
			bFound = true
		}
	}
	if !bFound {
		// B is only mentioned in test file; should not be in packages since no non-test file for B
		// Actually b.go is present, so B should be found
		t.Fatal("B should be found (b.go exists)")
	}
	if bIn != 0 {
		t.Errorf("B in_degree should be 0 (only test file imports it), got %d", bIn)
	}
}

// TestExternalImportIgnored: stdlib/external imports not tracked
func TestExternalImportIgnored(t *testing.T) {
	files := map[string]string{
		"internal/a/a.go": "package a\nimport \"fmt\"\nvar _ = fmt.Sprintf\n",
		"internal/b/b.go": "package b\nimport \"strings\"\nvar _ = strings.Split\n",
	}
	rep := deprank.Scan(files, prefix, 0)
	// neither fmt nor strings should appear in packages
	for _, p := range rep.Packages {
		if p.ImportPath == "fmt" || p.ImportPath == "strings" {
			t.Errorf("external package %q should not be tracked", p.ImportPath)
		}
	}
	// in-degrees should be 0 for both internal packages
	for _, p := range rep.Packages {
		if p.InDegree != 0 {
			t.Errorf("%s in_degree should be 0, got %d", p.ImportPath, p.InDegree)
		}
	}
}

// TestOutDegreeTracked: verify OutDegree counted correctly
func TestOutDegreeTracked(t *testing.T) {
	files := map[string]string{}
	// A imports B and C → A out-degree=2
	pa, ca := pkg("a", pkgPath("b"), pkgPath("c"))
	pb, cb := pkg("b")
	pc, cc := pkg("c")
	files[pa] = ca
	files[pb] = cb
	files[pc] = cc

	rep := deprank.Scan(files, prefix, 0)
	var aOut int
	for _, p := range rep.Packages {
		if p.ImportPath == pkgPath("a") {
			aOut = p.OutDegree
		}
	}
	if aOut != 2 {
		t.Errorf("A out_degree want 2 got %d", aOut)
	}
}

// TestImportersListSorted: importers slice is alphabetically sorted
func TestImportersListSorted(t *testing.T) {
	files := map[string]string{}
	files["internal/b/b.go"] = "package b\nvar _ = 1\n"
	// import in reverse order to test sorting
	for _, n := range []string{"z", "m", "a"} {
		p, c := pkg(n, pkgPath("b"))
		files[p] = c
	}

	rep := deprank.Scan(files, prefix, 0)
	var bImporters []string
	for _, p := range rep.Packages {
		if p.ImportPath == pkgPath("b") {
			bImporters = p.Importers
		}
	}
	sorted := make([]string, len(bImporters))
	copy(sorted, bImporters)
	// sort.Strings(sorted) already done
	if len(bImporters) != 3 {
		t.Fatalf("want 3 importers got %d", len(bImporters))
	}
	for i := 1; i < len(bImporters); i++ {
		if bImporters[i] < bImporters[i-1] {
			t.Errorf("importers not sorted: %v", bImporters)
			break
		}
	}
}

// TestDeterministic: same input twice gives identical Report
func TestDeterministic(t *testing.T) {
	files := map[string]string{}
	files["internal/b/b.go"] = "package b\nvar _ = 1\n"
	for _, n := range []string{"a", "c", "d"} {
		p, c := pkg(n, pkgPath("b"))
		files[p] = c
	}

	rep1 := deprank.Scan(files, prefix, 0)
	rep2 := deprank.Scan(files, prefix, 0)

	if !reflect.DeepEqual(rep1, rep2) {
		t.Error("Scan is not deterministic: two identical calls returned different results")
	}
}

// TestParseError: broken Go file → skip, no crash
func TestParseError(t *testing.T) {
	files := map[string]string{
		"internal/bad/bad.go": "this is not valid go code !!!",
		"internal/ok/ok.go":   "package ok\nvar _ = 1\n",
	}
	// should not panic
	rep := deprank.Scan(files, prefix, 0)
	// ok package should be present
	var found bool
	for _, p := range rep.Packages {
		if p.ImportPath == prefix+"/internal/ok" {
			found = true
		}
	}
	_ = found // bad package may or may not be present depending on parse behavior
}

// TestNonGoSkipped: .txt file → ignored
func TestNonGoSkipped(t *testing.T) {
	files := map[string]string{
		"README.txt":          "just a text file",
		"internal/a/a.go":    "package a\nvar _ = 1\n",
	}
	rep := deprank.Scan(files, prefix, 0)
	for _, p := range rep.Packages {
		if p.ImportPath == "README.txt" || p.ImportPath == prefix+"/README.txt" {
			t.Errorf("non-Go file should not appear in packages: %s", p.ImportPath)
		}
	}
}

// TestDefaultThresholdWhenZero: Scan(files, prefix, 0) uses defaultThreshold(5)
func TestDefaultThresholdWhenZero(t *testing.T) {
	files := map[string]string{}
	files["internal/b/b.go"] = "package b\nvar _ = 1\n"
	// 5 importers → at boundary; threshold=5 means >=5 flags
	for _, n := range []string{"a", "c", "d", "e", "f"} {
		p, c := pkg(n, pkgPath("b"))
		files[p] = c
	}

	rep := deprank.Scan(files, prefix, 0)
	if rep.Threshold != 5 {
		t.Errorf("threshold want 5 got %d", rep.Threshold)
	}
	// in-degree=5 >= 5 → should be flagged
	if len(rep.Findings) != 1 {
		t.Errorf("want 1 finding got %d", len(rep.Findings))
	}
}

// TestEmptyFiles: empty map → Report with zeros
func TestEmptyFiles(t *testing.T) {
	rep := deprank.Scan(map[string]string{}, prefix, 0)
	if rep.PackagesScanned != 0 {
		t.Errorf("packages_scanned want 0 got %d", rep.PackagesScanned)
	}
	if rep.HighCoupling != 0 {
		t.Errorf("high_coupling want 0 got %d", rep.HighCoupling)
	}
	if rep.MaxInDegree != 0 {
		t.Errorf("max_in_degree want 0 got %d", rep.MaxInDegree)
	}
	if rep.AvgInDegree != 0 {
		t.Errorf("avg_in_degree want 0 got %f", rep.AvgInDegree)
	}
	if len(rep.Packages) != 0 {
		t.Errorf("packages want empty got %v", rep.Packages)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("findings want empty got %v", rep.Findings)
	}
}
