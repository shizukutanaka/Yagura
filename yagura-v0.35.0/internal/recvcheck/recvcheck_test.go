package recvcheck_test

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/recvcheck"
)

func findRule(r recvcheck.Report, typ, rule string) bool {
	for _, f := range r.Findings {
		if f.Type == typ && f.Rule == rule {
			return true
		}
	}
	return false
}

const consistentSrc = `package foo

type Server struct{}

func (s *Server) Start() {}
func (s *Server) Stop()  {}
func (s *Server) Run()   {}
`

const inconsistentNameSrc = `package foo

type Server struct{}

func (s *Server) Start() {}
func (srv *Server) Stop() {}
`

const mixedPointerSrc = `package foo

type Box struct{}

func (b Box) Get() int   { return 0 }
func (b *Box) Set(v int) {}
`

const badNameSrc = `package foo

type Widget struct{}

func (this *Widget) Draw() {}
`

const unnamedRecvSrc = `package foo

type Marker struct{}

func (Marker) Mark()  {}
func (m Marker) Tag() {}
`

func TestScan_Consistent_NoFindings(t *testing.T) {
	r := recvcheck.Scan(map[string]string{"a.go": consistentSrc})
	if len(r.Findings) != 0 {
		t.Errorf("consistent receivers should yield no findings; got %+v", r.Findings)
	}
	if r.TypesWithMethods != 1 {
		t.Errorf("TypesWithMethods: want 1 got %d", r.TypesWithMethods)
	}
}

func TestScan_InconsistentName(t *testing.T) {
	r := recvcheck.Scan(map[string]string{"a.go": inconsistentNameSrc})
	if !findRule(r, "Server", "inconsistent-receiver-name") {
		t.Errorf("expected inconsistent-receiver-name for Server; got %+v", r.Findings)
	}
}

func TestScan_MixedPointerValue(t *testing.T) {
	r := recvcheck.Scan(map[string]string{"a.go": mixedPointerSrc})
	if !findRule(r, "Box", "mixed-receiver-type") {
		t.Errorf("expected mixed-receiver-type for Box; got %+v", r.Findings)
	}
}

func TestScan_BadReceiverName(t *testing.T) {
	r := recvcheck.Scan(map[string]string{"a.go": badNameSrc})
	if !findRule(r, "Widget", "bad-receiver-name") {
		t.Errorf("expected bad-receiver-name for Widget (this); got %+v", r.Findings)
	}
}

func TestScan_UnnamedReceiverIgnoredForName(t *testing.T) {
	// `func (Marker) Mark()` has no receiver name; it must not trigger an
	// inconsistent-name finding against the named `m`.
	r := recvcheck.Scan(map[string]string{"a.go": unnamedRecvSrc})
	if findRule(r, "Marker", "inconsistent-receiver-name") {
		t.Errorf("unnamed receiver must not count as a distinct name; got %+v", r.Findings)
	}
}

func TestScan_CrossFileType(t *testing.T) {
	files := map[string]string{
		"a.go": "package foo\n\ntype T struct{}\n\nfunc (t *T) A() {}\n",
		"b.go": "package foo\n\nfunc (x *T) B() {}\n",
	}
	r := recvcheck.Scan(files)
	if !findRule(r, "T", "inconsistent-receiver-name") {
		t.Errorf("inconsistency across files should be detected; got %+v", r.Findings)
	}
}

func TestScan_GenericReceiver(t *testing.T) {
	src := `package foo

type Stack[E any] struct{}

func (s *Stack[E]) Push(e E) {}
func (st *Stack[E]) Pop() E  { var z E; return z }
`
	r := recvcheck.Scan(map[string]string{"a.go": src})
	if !findRule(r, "Stack", "inconsistent-receiver-name") {
		t.Errorf("generic receiver type should resolve to Stack; got %+v", r.Findings)
	}
}

func TestScan_SeparateTypesIndependent(t *testing.T) {
	src := `package foo

type A struct{}
type B struct{}

func (a *A) M() {}
func (b *B) N() {}
`
	r := recvcheck.Scan(map[string]string{"a.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("two consistent types should be clean; got %+v", r.Findings)
	}
	if r.TypesWithMethods != 2 {
		t.Errorf("TypesWithMethods: want 2 got %d", r.TypesWithMethods)
	}
}

func TestScan_SameTypeNameDifferentPackages(t *testing.T) {
	// aiverify.Result (value receiver) and qualitycheck.Result (pointer receiver)
	// are distinct types; analysis must be package-scoped, not name-global, so
	// this must NOT report a mixed-receiver-type.
	files := map[string]string{
		"aiverify/a.go":     "package aiverify\n\ntype Result struct{}\n\nfunc (r Result) Summary() string { return \"\" }\n",
		"qualitycheck/q.go": "package qualitycheck\n\ntype Result struct{}\n\nfunc (r *Result) Summary() string { return \"\" }\n",
	}
	r := recvcheck.Scan(files)
	for _, f := range r.Findings {
		if f.Rule == "mixed-receiver-type" {
			t.Errorf("same type name in different packages must not be merged; got %+v", f)
		}
	}
	if r.TypesWithMethods != 2 {
		t.Errorf("TypesWithMethods: want 2 (one per package) got %d", r.TypesWithMethods)
	}
}

func TestScan_SameTypeNameSamePackageMixed(t *testing.T) {
	// Within one package, two files contributing value+pointer receivers to the
	// same type IS a real mixed-receiver finding.
	files := map[string]string{
		"p/a.go": "package p\n\ntype X struct{}\n\nfunc (x X) A() {}\n",
		"p/b.go": "package p\n\nfunc (x *X) B() {}\n",
	}
	r := recvcheck.Scan(files)
	if !findRule(r, "X", "mixed-receiver-type") {
		t.Errorf("intra-package mixed receivers should be flagged; got %+v", r.Findings)
	}
}

func TestScan_ParseErrorSurfaced(t *testing.T) {
	r := recvcheck.Scan(map[string]string{"broken.go": "package foo\nfunc ("})
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
	r := recvcheck.Scan(map[string]string{})
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Errorf("empty: files=%d findings=%d", r.FilesScanned, len(r.Findings))
	}
}

func TestScan_Deterministic(t *testing.T) {
	files := map[string]string{
		"z.go": inconsistentNameSrc,
		"a.go": mixedPointerSrc,
		"m.go": badNameSrc,
	}
	first := recvcheck.Scan(files)
	for i := 0; i < 20; i++ {
		got := recvcheck.Scan(files)
		if len(got.Findings) != len(first.Findings) {
			t.Fatalf("run %d: finding count drift", i)
		}
		for j := range got.Findings {
			if got.Findings[j] != first.Findings[j] {
				t.Fatalf("run %d: finding[%d] mismatch %+v vs %+v", i, j, got.Findings[j], first.Findings[j])
			}
		}
	}
}
