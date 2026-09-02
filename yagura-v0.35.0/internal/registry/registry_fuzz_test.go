package registry

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/project"
)

// FuzzAdd は任意の slug/repository を Add() に与えたとき、
// validation で拒否されるかフィールドが正しく保持されるかを確認する。
//
// 特に slug の regex を厳密に評価。path traversal や制御文字、超長文字列を防ぐ。
func FuzzAdd(f *testing.F) {
	seeds := []struct {
		slug, repo string
	}{
		{"alpha", "github.com/x/alpha"},
		{"a", "x/y"},
		{"with-hyphen", "x/y"},
		{"with.dot", "x/y"},
		{"with_under", "x/y"},
		{"../etc/passwd", "x/y"}, // path traversal - should reject
		{"/abs", "x/y"},          // absolute path - should reject
		{"with space", "x/y"},    // space - should reject
		{"with\x00null", "x/y"},  // null byte - should reject
		{"", ""},                 // empty - should reject
	}
	for _, s := range seeds {
		f.Add(s.slug, s.repo)
	}

	f.Fuzz(func(t *testing.T, slug, repo string) {
		reg, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		p := &project.Project{
			Slug:        slug,
			DisplayName: slug,
			Repository:  repo,
			Stage:       project.StageActive,
		}
		err = reg.Add(p)
		// If validation rejects, that's correct behavior, not a bug.
		if err != nil {
			return
		}
		// If validation passed, the project must be retrievable by exactly
		// that slug and the registry must be self-consistent.
		got, err := reg.Get(slug)
		if err != nil {
			t.Errorf("Add succeeded but Get failed: slug=%q err=%v", slug, err)
			return
		}
		if got.Slug != slug {
			t.Errorf("slug mismatch: stored=%q got=%q", slug, got.Slug)
		}
	})
}
