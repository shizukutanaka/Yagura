package coupling_test

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/coupling"
)

const mod = "example.com/m"

// twoPkg: a imports b. b imports nothing.
func twoPkg() map[string]string {
	return map[string]string{
		"internal/a/a.go": `package a

import "example.com/m/internal/b"

var _ = b.X
`,
		"internal/b/b.go": `package b

var X = 1
`,
	}
}

func find(r coupling.Report, name string) (coupling.Package, bool) {
	for _, p := range r.Packages {
		if p.Name == name {
			return p, true
		}
	}
	return coupling.Package{}, false
}

func TestScan_FanInFanOut(t *testing.T) {
	r := coupling.Scan(twoPkg(), mod)
	a, ok := find(r, "internal/a")
	if !ok {
		t.Fatal("internal/a missing")
	}
	if a.FanOut != 1 || a.FanIn != 0 {
		t.Errorf("a: want fanout 1 fanin 0, got out=%d in=%d", a.FanOut, a.FanIn)
	}
	if a.Instability != 1.0 {
		t.Errorf("a instability: want 1.0 got %f", a.Instability)
	}
	b, _ := find(r, "internal/b")
	if b.FanIn != 1 || b.FanOut != 0 {
		t.Errorf("b: want fanin 1 fanout 0, got in=%d out=%d", b.FanIn, b.FanOut)
	}
	if b.Instability != 0.0 {
		t.Errorf("b instability: want 0.0 got %f", b.Instability)
	}
}

func TestScan_ExternalImportsIgnored(t *testing.T) {
	files := map[string]string{
		"internal/a/a.go": `package a

import (
	"fmt"
	"strings"
	"example.com/m/internal/b"
)

var _ = fmt.Sprint(strings.ToUpper("x"))
var _ = b.X
`,
		"internal/b/b.go": "package b\n\nvar X = 1\n",
	}
	r := coupling.Scan(files, mod)
	a, _ := find(r, "internal/a")
	if a.FanOut != 1 {
		t.Errorf("external imports must be ignored; fanout want 1 got %d (imports=%v)", a.FanOut, a.Imports)
	}
}

func TestScan_TestFilesExcluded(t *testing.T) {
	files := twoPkg()
	// b_test.go imports a — would create a back-edge if test files counted.
	files["internal/b/b_test.go"] = `package b

import "example.com/m/internal/a"

var _ = a
`
	r := coupling.Scan(files, mod)
	b, _ := find(r, "internal/b")
	if b.FanOut != 0 {
		t.Errorf("test-file imports must be excluded; b fanout want 0 got %d", b.FanOut)
	}
}

func TestScan_SDPViolation(t *testing.T) {
	// hub: imported by x,y (Ca=2), imports vol (Ce=1) → I=1/3≈0.33 (stable-ish)
	// vol: imported by hub (Ca=1), imports leaf1,leaf2 (Ce=2) → I=2/3≈0.67 (unstable)
	// edge hub→vol: I[vol]-I[hub] ≈ 0.34 ≥ margin → SDP violation.
	files := map[string]string{
		"internal/x/x.go":     `package x` + "\n\nimport \"example.com/m/internal/hub\"\n\nvar _ = hub.V\n",
		"internal/y/y.go":     `package y` + "\n\nimport \"example.com/m/internal/hub\"\n\nvar _ = hub.V\n",
		"internal/hub/hub.go": `package hub` + "\n\nimport \"example.com/m/internal/vol\"\n\nvar V = vol.W\n",
		"internal/vol/vol.go": `package vol` + "\n\nimport (\n\t\"example.com/m/internal/leaf1\"\n\t\"example.com/m/internal/leaf2\"\n)\n\nvar W = leaf1.A + leaf2.B\n",
		"internal/leaf1/l.go": "package leaf1\n\nvar A = 1\n",
		"internal/leaf2/l.go": "package leaf2\n\nvar B = 2\n",
	}
	r := coupling.Scan(files, mod)
	var got *coupling.Finding
	for i := range r.Findings {
		if r.Findings[i].Rule == "sdp-violation" {
			got = &r.Findings[i]
		}
	}
	if got == nil {
		t.Fatalf("expected an sdp-violation finding; findings=%+v", r.Findings)
	}
	if got.From != "internal/hub" || got.To != "internal/vol" {
		t.Errorf("violation should be hub→vol, got %s→%s", got.From, got.To)
	}
}

func TestScan_NoSDPViolation_WhenStableDeps(t *testing.T) {
	// a→b where b is more stable than a — dependency points toward stability, OK.
	r := coupling.Scan(twoPkg(), mod)
	for _, f := range r.Findings {
		if f.Rule == "sdp-violation" {
			t.Errorf("no SDP violation expected for a→b (b more stable); got %+v", f)
		}
	}
}

func TestScan_ParseErrorSurfaced(t *testing.T) {
	r := coupling.Scan(map[string]string{"internal/x/x.go": "package x\nimport {{{"}, mod)
	found := false
	for _, f := range r.Findings {
		if f.Rule == "parse-error" {
			found = true
		}
	}
	if !found {
		t.Error("parse error must be surfaced")
	}
}

func TestScan_Empty(t *testing.T) {
	r := coupling.Scan(map[string]string{}, mod)
	if r.PackageCount != 0 {
		t.Errorf("PackageCount: want 0 got %d", r.PackageCount)
	}
}

func TestScan_Deterministic(t *testing.T) {
	files := twoPkg()
	first := coupling.Scan(files, mod)
	for i := 0; i < 20; i++ {
		got := coupling.Scan(files, mod)
		if len(got.Packages) != len(first.Packages) {
			t.Fatalf("run %d: package count drift", i)
		}
		for j := range got.Packages {
			if got.Packages[j].Name != first.Packages[j].Name {
				t.Fatalf("run %d: pkg[%d] order mismatch %q vs %q", i, j, got.Packages[j].Name, first.Packages[j].Name)
			}
		}
	}
}

func TestScan_ImportsSortedAndCounted(t *testing.T) {
	files := map[string]string{
		"internal/a/a.go": `package a

import (
	"example.com/m/internal/z"
	"example.com/m/internal/b"
)

var _ = z.Z + b.X
`,
		"internal/b/b.go": "package b\n\nvar X = 1\n",
		"internal/z/z.go": "package z\n\nvar Z = 1\n",
	}
	r := coupling.Scan(files, mod)
	a, _ := find(r, "internal/a")
	if len(a.Imports) != 2 {
		t.Fatalf("a imports: want 2 got %d", len(a.Imports))
	}
	if a.Imports[0] != "internal/b" || a.Imports[1] != "internal/z" {
		t.Errorf("imports must be sorted; got %v", a.Imports)
	}
}
