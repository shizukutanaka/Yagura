// pkgcount_test.go: the README status line, the README project-layout tree, and
// CLAUDE.md all advertise a fixed "N internal packages" count. Like the tool
// count (skilldoc_test.go), that prose drifts as packages are added — README
// once said 38 while there were 50. This guard ties every such claim to the
// actual number of buildable internal packages so a mismatch fails CI.
package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// countInternalPackages counts directories under internal/ that hold at least
// one non-test .go file (i.e. a buildable package) — the same notion the docs use.
func countInternalPackages(t *testing.T) int {
	t.Helper()
	const root = "../../internal"
	pkgs := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		pkgs[filepath.Dir(path)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return len(pkgs)
}

func TestDocs_InternalPackageCountMatches(t *testing.T) {
	want := countInternalPackages(t)

	checks := []struct {
		file string
		re   string
	}{
		{"../../README.md", `(\d+) internal packages`},
		{"../../README.md", `(\d+) packages, none exported`},
		{"../../CLAUDE.md", `(\d+) internal packages`},
	}
	for _, c := range checks {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("read %s: %v", c.file, err)
		}
		m := regexp.MustCompile(c.re).FindSubmatch(b)
		if m == nil {
			t.Errorf("%s: no claim matching %q to verify", c.file, c.re)
			continue
		}
		got, _ := strconv.Atoi(string(m[1]))
		if got != want {
			t.Errorf("%s claims %d packages (%q) but %d internal packages exist — update the doc",
				c.file, got, c.re, want)
		}
	}
}
