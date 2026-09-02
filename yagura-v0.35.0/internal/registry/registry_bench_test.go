package registry

import (
	"fmt"
	"testing"

	"github.com/shizukutanaka/yagura/internal/project"
)

func BenchmarkAdd(b *testing.B) {
	dir := b.TempDir()
	r, _ := New(dir)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Add(&project.Project{
			Slug:        fmt.Sprintf("bench-%d", i),
			DisplayName: "Bench",
			Repository:  fmt.Sprintf("x/bench-%d", i),
			Stage:       project.StageActive,
		})
	}
}

func BenchmarkGet(b *testing.B) {
	dir := b.TempDir()
	r, _ := New(dir)
	// Pre-populate
	for i := 0; i < 100; i++ {
		_ = r.Add(&project.Project{
			Slug: fmt.Sprintf("p-%d", i), DisplayName: "P", Repository: fmt.Sprintf("x/p-%d", i),
			Stage: project.StageActive,
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Get(fmt.Sprintf("p-%d", i%100))
	}
}

func BenchmarkList(b *testing.B) {
	dir := b.TempDir()
	r, _ := New(dir)
	for i := 0; i < 50; i++ {
		_ = r.Add(&project.Project{
			Slug: fmt.Sprintf("p-%d", i), DisplayName: "P", Repository: fmt.Sprintf("x/p-%d", i),
			Stage: project.StageActive,
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.List()
	}
}
