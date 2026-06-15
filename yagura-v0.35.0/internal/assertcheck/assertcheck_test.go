package assertcheck_test

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/assertcheck"
)

const hollow = `package foo_test

import "testing"

func TestFoo(t *testing.T) {
	Foo()
}

func TestBar(t *testing.T) {
	_ = Bar()
}
`

const dense = `package foo_test

import "testing"

func TestOK(t *testing.T) {
	got := Add(1, 2)
	if got != 3 {
		t.Errorf("want 3 got %d", got)
	}
}

func TestFail(t *testing.T) {
	if err := Op(); err != nil {
		t.Fatal(err)
	}
}
`

const mixed = `package foo_test

import "testing"

func TestWithAssert(t *testing.T) {
	if Foo() != "ok" {
		t.Error("not ok")
	}
}

func TestHollow(t *testing.T) {
	Foo()
}
`

const noTests = `package foo

func helper() int { return 42 }
`

const benchmarkOnly = `package foo_test

import "testing"

func BenchmarkAdd(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Add(1, 2)
	}
}
`

func TestScan_HollowFile(t *testing.T) {
	r := assertcheck.Scan(map[string]string{"a_test.go": hollow})
	if r.TestFiles != 1 {
		t.Errorf("TestFiles: want 1 got %d", r.TestFiles)
	}
	if r.TotalTestFuncs != 2 {
		t.Errorf("TotalTestFuncs: want 2 got %d", r.TotalTestFuncs)
	}
	if r.TotalAssertions != 0 {
		t.Errorf("TotalAssertions: want 0 got %d", r.TotalAssertions)
	}
	if r.HollowFiles != 1 {
		t.Errorf("HollowFiles: want 1 got %d", r.HollowFiles)
	}
	if len(r.Files) != 1 || !r.Files[0].Hollow {
		t.Error("file should be hollow")
	}
}

func TestScan_DenseFile(t *testing.T) {
	r := assertcheck.Scan(map[string]string{"b_test.go": dense})
	if r.TotalTestFuncs != 2 {
		t.Errorf("TotalTestFuncs: want 2 got %d", r.TotalTestFuncs)
	}
	if r.TotalAssertions != 2 {
		t.Errorf("TotalAssertions: want 2 got %d", r.TotalAssertions)
	}
	if r.HollowFiles != 0 {
		t.Errorf("HollowFiles: want 0 got %d", r.HollowFiles)
	}
	if r.Files[0].Hollow {
		t.Error("dense file should not be hollow")
	}
	if r.Files[0].Density != 1.0 {
		t.Errorf("Density: want 1.0 got %f", r.Files[0].Density)
	}
}

func TestScan_MixedFile(t *testing.T) {
	r := assertcheck.Scan(map[string]string{"c_test.go": mixed})
	if r.TotalTestFuncs != 2 {
		t.Errorf("TotalTestFuncs: want 2 got %d", r.TotalTestFuncs)
	}
	if r.TotalAssertions != 1 {
		t.Errorf("TotalAssertions: want 1 got %d", r.TotalAssertions)
	}
	if r.HollowFiles != 0 {
		t.Error("mixed file has assertions so should not be hollow")
	}
	// density = 0.5
	if r.Files[0].Density != 0.5 {
		t.Errorf("Density: want 0.5 got %f", r.Files[0].Density)
	}
}

func TestScan_NonTestFile_Ignored(t *testing.T) {
	r := assertcheck.Scan(map[string]string{"foo.go": noTests})
	if r.TestFiles != 0 {
		t.Errorf("TestFiles: want 0 got %d", r.TestFiles)
	}
	if r.FilesScanned != 1 {
		t.Errorf("FilesScanned: want 1 got %d", r.FilesScanned)
	}
}

func TestScan_BenchmarkNotCountedAsTestFunc(t *testing.T) {
	r := assertcheck.Scan(map[string]string{"bench_test.go": benchmarkOnly})
	// Benchmark funcs are not Test funcs; TestFuncs should be 0
	if r.TotalTestFuncs != 0 {
		t.Errorf("TotalTestFuncs: want 0 got %d (benchmarks should not count)", r.TotalTestFuncs)
	}
	if r.TestFiles != 1 {
		t.Errorf("TestFiles: want 1 got %d", r.TestFiles)
	}
}

func TestScan_Empty(t *testing.T) {
	r := assertcheck.Scan(map[string]string{})
	if r.FilesScanned != 0 {
		t.Errorf("FilesScanned: want 0 got %d", r.FilesScanned)
	}
	if r.AvgDensity != 0 {
		t.Errorf("AvgDensity: want 0 got %f", r.AvgDensity)
	}
}

func TestScan_MultipleAssertionForms(t *testing.T) {
	src := `package foo_test
import "testing"

func TestAll(t *testing.T) {
	t.Error("e1")
	t.Errorf("e2 %s", "x")
	t.Fatal("f1")
	t.Fatalf("f2 %d", 1)
	t.Fail()
	t.FailNow()
}
`
	r := assertcheck.Scan(map[string]string{"all_test.go": src})
	if r.TotalAssertions != 6 {
		t.Errorf("TotalAssertions: want 6 got %d", r.TotalAssertions)
	}
}

func TestScan_Deterministic(t *testing.T) {
	files := map[string]string{
		"z_test.go": hollow,
		"a_test.go": dense,
		"m_test.go": mixed,
	}
	first := assertcheck.Scan(files)
	for i := 0; i < 20; i++ {
		got := assertcheck.Scan(files)
		for j, f := range got.Files {
			if f.Path != first.Files[j].Path {
				t.Fatalf("run %d: file[%d] path mismatch: %q vs %q", i, j, f.Path, first.Files[j].Path)
			}
		}
	}
}

func TestFileDensity_NoTestFuncs_DensityZero(t *testing.T) {
	r := assertcheck.Scan(map[string]string{"bench_test.go": benchmarkOnly})
	if len(r.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(r.Files))
	}
	if r.Files[0].Density != 0 {
		t.Errorf("Density: want 0 for no-test-func file, got %f", r.Files[0].Density)
	}
	if r.Files[0].Hollow {
		t.Error("file with no test funcs should not be hollow (nothing to assert)")
	}
}

func TestScan_AvgDensity(t *testing.T) {
	// hollow: 2 funcs, 0 assertions → density 0
	// dense:  2 funcs, 2 assertions → density 1.0
	// avg of file densities = (0 + 1.0) / 2 = 0.5
	r := assertcheck.Scan(map[string]string{
		"hollow_test.go": hollow,
		"dense_test.go":  dense,
	})
	if r.AvgDensity != 0.5 {
		t.Errorf("AvgDensity: want 0.5 got %f", r.AvgDensity)
	}
}
