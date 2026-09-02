package apidoc_test

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/apidoc"
)

const documentedSrc = `package foo

// Add returns the sum of a and b.
func Add(a, b int) int { return a + b }

// Server holds state.
type Server struct{}

// Start begins serving.
func (s *Server) Start() error { return nil }
`

const undocumentedSrc = `package foo

func Naked() int { return 1 }

type Bare struct{}
`

const unexportedSrc = `package foo

func helper() int { return 1 }

type internal struct{}

func (i *internal) method() {}
`

const methodOnUnexportedSrc = `package foo

type secret struct{}

func (s *secret) Exported() {}
`

const groupedSrc = `package foo

const (
	// A is the first.
	A = 1
	B = 2
)

var (
	// X is documented.
	X = 1
	Y = 2
)
`

func hasFinding(r apidoc.Report, name string) bool {
	for _, f := range r.Findings {
		if f.Name == name && f.Rule == "exported-undocumented" {
			return true
		}
	}
	return false
}

func TestScan_DocumentedSymbols_NoFindings(t *testing.T) {
	r := apidoc.Scan(map[string]string{"a.go": documentedSrc})
	if r.ExportedTotal != 3 {
		t.Errorf("ExportedTotal: want 3 (Add, Server, Start) got %d", r.ExportedTotal)
	}
	if r.Documented != 3 {
		t.Errorf("Documented: want 3 got %d", r.Documented)
	}
	if r.DocumentedRatio != 1.0 {
		t.Errorf("DocumentedRatio: want 1.0 got %f", r.DocumentedRatio)
	}
	if len(r.Findings) != 0 {
		t.Errorf("Findings: want 0 got %d: %+v", len(r.Findings), r.Findings)
	}
}

func TestScan_UndocumentedExported_Flagged(t *testing.T) {
	r := apidoc.Scan(map[string]string{"a.go": undocumentedSrc})
	if r.ExportedTotal != 2 {
		t.Errorf("ExportedTotal: want 2 got %d", r.ExportedTotal)
	}
	if r.Documented != 0 {
		t.Errorf("Documented: want 0 got %d", r.Documented)
	}
	if !hasFinding(r, "Naked") {
		t.Error("Naked should be flagged undocumented")
	}
	if !hasFinding(r, "Bare") {
		t.Error("Bare should be flagged undocumented")
	}
	if r.DocumentedRatio != 0.0 {
		t.Errorf("DocumentedRatio: want 0.0 got %f", r.DocumentedRatio)
	}
}

func TestScan_UnexportedIgnored(t *testing.T) {
	r := apidoc.Scan(map[string]string{"a.go": unexportedSrc})
	if r.ExportedTotal != 0 {
		t.Errorf("unexported symbols must be ignored; ExportedTotal want 0 got %d", r.ExportedTotal)
	}
	if len(r.Findings) != 0 {
		t.Errorf("no findings for unexported; got %+v", r.Findings)
	}
}

func TestScan_MethodOnUnexportedType_Ignored(t *testing.T) {
	// An exported method on an unexported type is not part of the public API.
	r := apidoc.Scan(map[string]string{"a.go": methodOnUnexportedSrc})
	if r.ExportedTotal != 0 {
		t.Errorf("method on unexported type is not public API; ExportedTotal want 0 got %d", r.ExportedTotal)
	}
}

func TestScan_MethodNameFormat(t *testing.T) {
	r := apidoc.Scan(map[string]string{"a.go": documentedSrc})
	found := false
	for _, s := range r.Symbols {
		if s.Name == "Server.Start" && s.Kind == "method" {
			found = true
		}
	}
	if !found {
		var names []string
		for _, s := range r.Symbols {
			names = append(names, s.Name)
		}
		t.Errorf("method should be named Server.Start; got %v", names)
	}
}

func TestScan_GroupedConstVar(t *testing.T) {
	r := apidoc.Scan(map[string]string{"a.go": groupedSrc})
	// A (documented), B (not), X (documented), Y (not) → 4 exported, 2 documented
	if r.ExportedTotal != 4 {
		t.Errorf("ExportedTotal: want 4 got %d", r.ExportedTotal)
	}
	if r.Documented != 2 {
		t.Errorf("Documented: want 2 (A, X) got %d", r.Documented)
	}
	if !hasFinding(r, "B") || !hasFinding(r, "Y") {
		t.Errorf("B and Y should be flagged; findings=%+v", r.Findings)
	}
}

func TestScan_ByKind(t *testing.T) {
	r := apidoc.Scan(map[string]string{"a.go": documentedSrc})
	if r.ByKind["func"] != 1 || r.ByKind["type"] != 1 || r.ByKind["method"] != 1 {
		t.Errorf("ByKind: want func=1 type=1 method=1 got %v", r.ByKind)
	}
}

func TestScan_ParseErrorSurfaced(t *testing.T) {
	r := apidoc.Scan(map[string]string{"broken.go": "package foo\nfunc {{{"})
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
	r := apidoc.Scan(map[string]string{})
	if r.FilesScanned != 0 {
		t.Errorf("FilesScanned: want 0 got %d", r.FilesScanned)
	}
	if r.DocumentedRatio != 0 {
		t.Errorf("DocumentedRatio: want 0 got %f", r.DocumentedRatio)
	}
}

func TestScan_TestFilesExcluded(t *testing.T) {
	r := apidoc.Scan(map[string]string{"a_test.go": undocumentedSrc})
	if r.ExportedTotal != 0 {
		t.Errorf("test files are not public API; ExportedTotal want 0 got %d", r.ExportedTotal)
	}
}

func TestScan_Deterministic(t *testing.T) {
	files := map[string]string{
		"z.go": undocumentedSrc,
		"a.go": documentedSrc,
		"m.go": groupedSrc,
	}
	first := apidoc.Scan(files)
	for i := 0; i < 20; i++ {
		got := apidoc.Scan(files)
		if len(got.Findings) != len(first.Findings) {
			t.Fatalf("run %d: finding count drift", i)
		}
		for j := range got.Findings {
			if got.Findings[j] != first.Findings[j] {
				t.Fatalf("run %d: finding[%d] mismatch", i, j)
			}
		}
	}
}

// TestScan_GroupDocCountsForEveryMemberOfTheBlock は Go の慣行どおり
// **宣言グループに付いた doc コメント** をそのグループ全員の文書として数える。
//
// 発見の経緯(v1.89.0): 未検分だった 8 レンズの finding を全部読んだところ、
// `api_doc` の 8 件が **すべて同じ形** だった——doc コメントの付いた
// `const ( ... )` ブロックの中身が「未文書」と報告されていた。
//
//	// DefaultMaxFiles / DefaultMaxBytes は既定の走査上限。
//	const (
//		DefaultMaxFiles = 1000
//		DefaultMaxBytes = 50 * 1024 * 1024
//	)
//
// 原因は `single := len(d.Specs) == 1` ——親 doc の継承を **spec が 1 つの
// ときだけ** に限っていた。しかし `go doc` はグループの doc をメンバーの
// 説明として表示し、golint もブロックに doc があれば中身を咎めない。
// つまりこれは Go の慣行に反する **偽陽性** であり、自リポジトリでの
// 偽陽性率は 8/8 = 100% だった(err_discard / err_policy に続く 3 例目)。
func TestScan_GroupDocCountsForEveryMemberOfTheBlock(t *testing.T) {
	files := map[string]string{
		"p/p.go": `package p

// Limits は既定の上限。
const (
	MaxFiles = 1000
	MaxBytes = 50
)

// Solo は単独の定数。
const Solo = 1

const (
	// Own は自分の doc を持つ。
	Own = 2
	// Bare には doc が無く、グループにも doc が無い。
	Bare = 3
)

const (
	Undocumented = 4
)
`,
	}
	r := apidoc.Scan(files)
	flagged := map[string]bool{}
	for _, f := range r.Findings {
		flagged[f.Name] = true
	}

	// グループ doc はメンバー全員に効く。
	for _, name := range []string{"MaxFiles", "MaxBytes", "Solo", "Own", "Bare"} {
		if flagged[name] {
			t.Errorf("%s is documented (own doc or group doc) but was reported as undocumented", name)
		}
	}
	// doc がどこにも無いものは今までどおり報告する——**常に黙る検査器は何も検査しない**。
	if !flagged["Undocumented"] {
		t.Error("a const with neither its own doc nor a group doc must still be reported")
	}
}
