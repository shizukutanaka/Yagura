package synccheck

import "testing"

func has(r Report, name, rule string) bool {
	for _, f := range r.Findings {
		if f.Name == name && f.Rule == rule {
			return true
		}
	}
	return false
}

// ─── mutex-value-receiver ────────────────────────────────

func TestScan_ValueReceiverOnLockyTypeFlagged(t *testing.T) {
	src := `package p
import "sync"
type Server struct{ mu sync.Mutex }
func (s Server) Do() {}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "(Server).Do", "mutex-value-receiver") {
		t.Errorf("value receiver on lock-bearing struct should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_PointerReceiverOnLockyTypeClean(t *testing.T) {
	src := `package p
import "sync"
type Server struct{ mu sync.Mutex }
func (s *Server) Do() {}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("pointer receiver should be clean, got: %+v", r.Findings)
	}
}

func TestScan_ValueReceiverOnPlainTypeClean(t *testing.T) {
	src := `package p
type Coord struct{ X, Y int }
func (c Coord) Sum() int { return c.X + c.Y }
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("plain struct value receiver is fine, got: %+v", r.Findings)
	}
}

// sync.RWMutex, sync.WaitGroup, sync.Once, sync.Cond also count.
func TestScan_AllSyncLockyTypesDetected(t *testing.T) {
	src := `package p
import "sync"
type A struct{ mu sync.RWMutex }
func (a A) Do() {}
type B struct{ wg sync.WaitGroup }
func (b B) Do() {}
type C struct{ once sync.Once }
func (c C) Do() {}
type D struct{ cond sync.Cond }
func (d D) Do() {}
`
	r := Scan(map[string]string{"x.go": src})
	for _, want := range []string{"(A).Do", "(B).Do", "(C).Do", "(D).Do"} {
		if !has(r, want, "mutex-value-receiver") {
			t.Errorf("%s should be flagged for sync-locky field", want)
		}
	}
}

// Embedded (anonymous) sync.Mutex also counts.
func TestScan_EmbeddedMutexCounts(t *testing.T) {
	src := `package p
import "sync"
type Server struct{ sync.Mutex }
func (s Server) Do() {}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "(Server).Do", "mutex-value-receiver") {
		t.Errorf("embedded sync.Mutex must count, got: %+v", r.Findings)
	}
}

// ─── mutex-by-value-param ────────────────────────────────

func TestScan_LockyTypeAsValueParamFlagged(t *testing.T) {
	src := `package p
import "sync"
type Server struct{ mu sync.Mutex }
func Process(s Server) {}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "Process", "mutex-by-value-param") {
		t.Errorf("Server-by-value param should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_PointerToLockyTypeClean(t *testing.T) {
	src := `package p
import "sync"
type Server struct{ mu sync.Mutex }
func Process(s *Server) {}
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("*Server param should be clean, got: %+v", r.Findings)
	}
}

// Direct sync.Mutex param (not via a containing struct) is also flagged.
func TestScan_DirectSyncMutexParamFlagged(t *testing.T) {
	src := `package p
import "sync"
func Bad(m sync.Mutex) {}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "Bad", "mutex-by-value-param") {
		t.Errorf("sync.Mutex by value param should be flagged, got: %+v", r.Findings)
	}
}

// ─── mutex-by-value-return ───────────────────────────────

func TestScan_LockyTypeAsValueReturnFlagged(t *testing.T) {
	src := `package p
import "sync"
type Server struct{ mu sync.Mutex }
func New() Server { return Server{} }
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "New", "mutex-by-value-return") {
		t.Errorf("Server-by-value return should be flagged, got: %+v", r.Findings)
	}
}

func TestScan_PointerReturnClean(t *testing.T) {
	src := `package p
import "sync"
type Server struct{ mu sync.Mutex }
func New() *Server { return &Server{} }
`
	r := Scan(map[string]string{"x.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("*Server return should be clean, got: %+v", r.Findings)
	}
}

// ─── transitivity (one hop, same package) ────────────────

func TestScan_StructContainingLockyStructFlagged(t *testing.T) {
	src := `package p
import "sync"
type Inner struct{ mu sync.Mutex }
type Outer struct{ inner Inner }
func (o Outer) Do() {}
`
	r := Scan(map[string]string{"x.go": src})
	if !has(r, "(Outer).Do", "mutex-value-receiver") {
		t.Errorf("Outer transitively contains a Mutex; value receiver should be flagged, got: %+v", r.Findings)
	}
}

// ─── contract: skips & robustness ────────────────────────

func TestScan_TestFileSkipped(t *testing.T) {
	src := `package p
import "sync"
type Server struct{ mu sync.Mutex }
func (s Server) Do() {}
`
	r := Scan(map[string]string{"x_test.go": src})
	if len(r.Findings) != 0 {
		t.Errorf("_test.go must be skipped, got: %+v", r.Findings)
	}
}

func TestScan_NonGoSkipped(t *testing.T) {
	r := Scan(map[string]string{"README.md": "func (s Server) Do() {}"})
	if r.FilesScanned != 0 {
		t.Errorf("non-go must not be scanned, FilesScanned=%d", r.FilesScanned)
	}
}

func TestScan_ParseErrorNoCrash(t *testing.T) {
	r := Scan(map[string]string{"bad.go": "package p\nfunc ("})
	found := false
	for _, f := range r.Findings {
		if f.Rule == "parse-error" {
			found = true
		}
	}
	if !found {
		t.Error("broken source should yield a parse-error finding, not a crash")
	}
}

func TestScan_EmptyInput(t *testing.T) {
	r := Scan(map[string]string{})
	if r.FilesScanned != 0 || len(r.Findings) != 0 {
		t.Errorf("empty input should be empty report, got %+v", r)
	}
}

func TestScan_Deterministic(t *testing.T) {
	src := `package p
import "sync"
type Server struct{ mu sync.Mutex }
func (s Server) A() {}
func Process(s Server) {}
func New() Server { return Server{} }
`
	a := Scan(map[string]string{"x.go": src})
	b := Scan(map[string]string{"x.go": src})
	if len(a.Findings) != len(b.Findings) {
		t.Fatalf("non-deterministic count: %d vs %d", len(a.Findings), len(b.Findings))
	}
	for i := range a.Findings {
		if a.Findings[i] != b.Findings[i] {
			t.Errorf("finding %d differs: %+v vs %+v", i, a.Findings[i], b.Findings[i])
		}
	}
}

func TestScan_SummaryCounts(t *testing.T) {
	src := `package p
import "sync"
type Server struct{ mu sync.Mutex }
func (s Server) A() {}
func Process(s Server) {}
func New() Server { return Server{} }
type Plain struct{ x int }
func (p Plain) Clean() {}
`
	r := Scan(map[string]string{"x.go": src})
	if r.Flagged != 3 {
		t.Errorf("Flagged: want 3, got %d", r.Flagged)
	}
}

// Cross-file: type defined in one file, method declared in another.
func TestScan_CrossFileTypeDetection(t *testing.T) {
	defs := `package p
import "sync"
type Server struct{ mu sync.Mutex }
`
	methods := `package p
func (s Server) Do() {}
`
	r := Scan(map[string]string{"types.go": defs, "methods.go": methods})
	if !has(r, "(Server).Do", "mutex-value-receiver") {
		t.Errorf("cross-file type detection failed, got: %+v", r.Findings)
	}
}
