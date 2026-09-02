package astcheck

import (
	"testing"
)

func TestSurface_ExecAndNetwork(t *testing.T) {
	files := map[string]string{
		"run.go":  "package foo\nimport \"os/exec\"\nvar _ = exec.Command\n",
		"http.go": "package foo\nimport \"net/http\"\nvar _ = http.Get\n",
	}
	res := Surface(files)
	if res.FilesScanned != 2 {
		t.Errorf("FilesScanned=%d want 2", res.FilesScanned)
	}
	if got := res.ByCapability["exec"]; len(got) != 1 || got[0] != "run.go" {
		t.Errorf("exec capability: %v", got)
	}
	if got := res.ByCapability["network"]; len(got) != 1 || got[0] != "http.go" {
		t.Errorf("network capability: %v", got)
	}
}

func TestSurface_UnsafeReflectCrypto(t *testing.T) {
	files := map[string]string{
		"u.go": "package foo\nimport \"unsafe\"\nvar _ unsafe.Pointer\n",
		"r.go": "package foo\nimport \"reflect\"\nvar _ = reflect.TypeOf\n",
		"c.go": "package foo\nimport \"crypto/sha256\"\nvar _ = sha256.New\n",
	}
	res := Surface(files)
	for cap, file := range map[string]string{"unsafe": "u.go", "reflect": "r.go", "crypto": "c.go"} {
		if got := res.ByCapability[cap]; len(got) != 1 || got[0] != file {
			t.Errorf("%s capability: got %v want [%s]", cap, got, file)
		}
	}
}

// net/url is pure parsing, not a network capability — must not be flagged.
func TestSurface_NetURLNotNetwork(t *testing.T) {
	files := map[string]string{"p.go": "package foo\nimport \"net/url\"\nvar _ = url.Parse\n"}
	res := Surface(files)
	if got := res.ByCapability["network"]; len(got) != 0 {
		t.Errorf("net/url must not count as network, got %v", got)
	}
}

func TestSurface_SkipsNonGoAndPlain(t *testing.T) {
	files := map[string]string{
		"x.txt":   "import \"os/exec\"",                         // not Go
		"safe.go": "package foo\nfunc Add() int { return 1 }\n", // no risky imports
	}
	res := Surface(files)
	if res.FilesScanned != 1 {
		t.Errorf("only safe.go is Go; FilesScanned=%d want 1", res.FilesScanned)
	}
	if len(res.ByCapability) != 0 {
		t.Errorf("no capabilities expected, got %v", res.ByCapability)
	}
}

// per-capability file lists must be sorted (deterministic over the input map).
func TestSurface_DeterministicSortedFiles(t *testing.T) {
	files := map[string]string{
		"c.go": "package foo\nimport \"net\"\n",
		"a.go": "package foo\nimport \"net\"\n",
		"b.go": "package foo\nimport \"net\"\n",
	}
	got := Surface(files).ByCapability["network"]
	want := []string{"a.go", "b.go", "c.go"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("network files not sorted deterministically: %v", got)
	}
}
